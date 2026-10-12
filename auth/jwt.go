// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// JWTConfig configures a JWTValidator.
type JWTConfig struct {
	// Keys are keys to verify signatures with. See JWTKey.
	Keys []JWTKey
	// JWKSURL is where the identity provider publishes its signing keys as a
	// JSON Web Key Set (RFC 7517). Its keys are used in addition to Keys,
	// and fetched again when a token names a key that is not known.
	JWKSURL string
	// HTTPClient fetches the key set; a client with a 10 second timeout
	// when nil.
	HTTPClient *http.Client

	// Issuer, when set, is the iss a token must have.
	Issuer string
	// Audiences, when set, are the audiences this VTN answers to: the aud
	// of a token must name one of them. Set it whenever the identity
	// provider issues tokens for other services too.
	Audiences []string
	// ClientIDClaim names the claim that identifies the client. When empty,
	// the first of client_id, azp and sub that the token has.
	ClientIDClaim string
	// ScopeClaim names the claim that holds the scopes: a string of scopes
	// separated by spaces, or an array of them. When empty, scope, then scp.
	ScopeClaim string
	// Leeway is how far the clock of the issuer may differ; none when zero.
	Leeway time.Duration
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// JWTKey is a key to verify token signatures with.
type JWTKey struct {
	// ID is the kid of the tokens the key signs. A key without an ID is
	// tried for tokens without a kid, and for every token when it is the
	// only key.
	ID string
	// Key is an *rsa.PublicKey, an *ecdsa.PublicKey, an ed25519.PublicKey,
	// or a []byte holding the secret of an HMAC.
	//
	// The kind of key decides which algorithms it verifies: a public key is
	// never used as an HMAC secret, whatever the token claims.
	Key any
}

// JWTValidator is a Validator for bearer tokens that are JSON Web Tokens
// (RFC 7519) signed by an identity provider: the usual form of an OAuth2
// access token.
//
// It verifies the signature (RS256/384/512, ES256/384/512, EdDSA with
// Ed25519, HS256/384/512), requires an expiry that has not passed, honours
// nbf, and checks iss and aud as configured. A token without a signature is
// never accepted.
//
// A JWTValidator is safe for concurrent use.
type JWTValidator struct {
	cfg JWTConfig

	mu sync.Mutex
	// fetched are the keys of the key set, by kid.
	fetched   map[string]any
	lastFetch time.Time
}

// refetchAfter is the least time between two fetches of the key set, so
// that tokens with made-up key IDs cannot be used to flood the provider.
const refetchAfter = time.Minute

// NewJWTValidator returns a validator for cfg. It does not fetch the key
// set: that happens with the first token that needs it.
func NewJWTValidator(cfg JWTConfig) (*JWTValidator, error) {
	if len(cfg.Keys) == 0 && cfg.JWKSURL == "" {
		return nil, errors.New("auth: a JWTValidator needs Keys or a JWKSURL")
	}
	for i, k := range cfg.Keys {
		switch key := k.Key.(type) {
		case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
		case []byte:
			// RFC 7518 section 3.2: at least the size of the hash.
			if len(key) < 32 {
				return nil, fmt.Errorf("auth: JWTConfig.Keys[%d]: an HMAC secret needs at least 32 bytes", i)
			}
		default:
			return nil, fmt.Errorf("auth: JWTConfig.Keys[%d]: a %T is not a key this validator can use", i, k.Key)
		}
	}
	if cfg.JWKSURL != "" && !strings.HasPrefix(cfg.JWKSURL, "https://") && !strings.HasPrefix(cfg.JWKSURL, "http://") {
		return nil, fmt.Errorf("auth: JWTConfig.JWKSURL %q is not an http or https URL", cfg.JWKSURL)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWTValidator{cfg: cfg}, nil
}

var b64 = base64.RawURLEncoding

// Validate implements Validator. Every way a token can be wrong is
// ErrInvalidToken; an error that is not means the key set could not be had.
func (v *JWTValidator) Validate(ctx context.Context, token string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	headerJSON, err := b64.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerJSON, &header) != nil {
		return Identity{}, ErrInvalidToken
	}
	signature, err := b64.DecodeString(parts[2])
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return Identity{}, ErrInvalidToken
	}

	keys, err := v.keysFor(ctx, header.Kid)
	if err != nil {
		return Identity{}, err
	}
	signed := []byte(parts[0] + "." + parts[1])
	if !slices.ContainsFunc(keys, func(key any) bool { return verify(header.Alg, key, signed, signature) }) {
		return Identity{}, ErrInvalidToken
	}

	// Only now is anything in the payload believed.
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return Identity{}, ErrInvalidToken
	}
	now := v.cfg.Now()
	exp, ok := numeric(claims["exp"])
	if !ok || !now.Before(exp.Add(v.cfg.Leeway)) {
		return Identity{}, ErrInvalidToken
	}
	if nbf, ok := numeric(claims["nbf"]); ok && now.Before(nbf.Add(-v.cfg.Leeway)) {
		return Identity{}, ErrInvalidToken
	}
	if v.cfg.Issuer != "" && claims["iss"] != v.cfg.Issuer {
		return Identity{}, ErrInvalidToken
	}
	if len(v.cfg.Audiences) > 0 {
		aud := stringsOf(claims["aud"], false)
		if !slices.ContainsFunc(aud, func(a string) bool { return slices.Contains(v.cfg.Audiences, a) }) {
			return Identity{}, ErrInvalidToken
		}
	}

	id := Identity{}
	for _, name := range claimNames(v.cfg.ClientIDClaim, "client_id", "azp", "sub") {
		if s, ok := claims[name].(string); ok && s != "" {
			id.ClientID = s
			break
		}
	}
	if id.ClientID == "" {
		// Without it the VTN could not tell whose objects are whose.
		return Identity{}, ErrInvalidToken
	}
	for _, name := range claimNames(v.cfg.ScopeClaim, "scope", "scp") {
		if raw, ok := claims[name]; ok {
			id.Scopes = stringsOf(raw, true)
			break
		}
	}
	return id, nil
}

func claimNames(configured string, defaults ...string) []string {
	if configured != "" {
		return []string{configured}
	}
	return defaults
}

// numeric reads a NumericDate claim.
func numeric(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || f > 1e12 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// stringsOf reads a claim that is a string or an array of strings; with
// split, a string is a list separated by spaces.
func stringsOf(v any, split bool) []string {
	switch v := v.(type) {
	case string:
		if split {
			return strings.Fields(v)
		}
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// verify checks a signature with one key. The algorithm has to be one that
// goes with the kind of key: this is what keeps a token "signed" with a
// public key as an HMAC secret from being accepted.
func verify(alg string, key any, signed, signature []byte) bool {
	var h crypto.Hash
	switch alg[max(len(alg)-3, 0):] {
	case "256":
		h = crypto.SHA256
	case "384":
		h = crypto.SHA384
	case "512":
		h = crypto.SHA512
	}
	digest := func() []byte {
		var w hash.Hash
		switch h {
		case crypto.SHA256:
			w = sha256.New()
		case crypto.SHA384:
			w = sha512.New384()
		default:
			w = sha512.New()
		}
		w.Write(signed)
		return w.Sum(nil)
	}
	switch key := key.(type) {
	case *rsa.PublicKey:
		if alg != "RS256" && alg != "RS384" && alg != "RS512" {
			return false
		}
		return rsa.VerifyPKCS1v15(key, h, digest(), signature) == nil
	case *ecdsa.PublicKey:
		// The curve goes with the algorithm.
		want := map[string]elliptic.Curve{"ES256": elliptic.P256(), "ES384": elliptic.P384(), "ES512": elliptic.P521()}[alg]
		size := (key.Curve.Params().BitSize + 7) / 8
		if want == nil || key.Curve != want || len(signature) != 2*size {
			return false
		}
		r, s := new(big.Int).SetBytes(signature[:size]), new(big.Int).SetBytes(signature[size:])
		return ecdsa.Verify(key, digest(), r, s)
	case ed25519.PublicKey:
		return alg == "EdDSA" && ed25519.Verify(key, signed, signature)
	case []byte:
		var mac hash.Hash
		switch alg {
		case "HS256":
			mac = hmac.New(sha256.New, key)
		case "HS384":
			mac = hmac.New(sha512.New384, key)
		case "HS512":
			mac = hmac.New(sha512.New, key)
		default:
			return false
		}
		mac.Write(signed)
		return hmac.Equal(mac.Sum(nil), signature)
	}
	return false
}

// keysFor returns the keys a token with the given kid may be signed with.
func (v *JWTValidator) keysFor(ctx context.Context, kid string) ([]any, error) {
	var keys []any
	for _, k := range v.cfg.Keys {
		if k.ID == kid || k.ID == "" && len(v.cfg.Keys) == 1 {
			keys = append(keys, k.Key)
		}
	}
	if v.cfg.JWKSURL == "" {
		return keys, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, known := v.fetched[kid]; !known && (v.fetched == nil || v.cfg.Now().Sub(v.lastFetch) >= refetchAfter) {
		fetched, err := v.fetch(ctx)
		if err != nil {
			if v.fetched == nil && len(keys) == 0 {
				return nil, fmt.Errorf("auth: fetching the key set: %w", err)
			}
			// Keep verifying with what is known; the provider may be back
			// by the time a new key is really needed.
		} else {
			v.fetched = fetched
		}
		v.lastFetch = v.cfg.Now()
	}
	if key, ok := v.fetched[kid]; ok {
		keys = append(keys, key)
	} else if kid == "" && len(v.fetched) == 1 {
		// A provider with one key need not name it.
		for _, key := range v.fetched {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// fetch reads the key set. Keys it cannot use are skipped: a provider may
// publish keys for purposes other than signing tokens.
func (v *JWTValidator) fetch(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the key set answered %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Crv string `json:"crv"`
			N   string `json:"n"`
			E   string `json:"e"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("the key set is not JSON: %w", err)
	}
	keys := map[string]any{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		num := func(s string) *big.Int {
			b, err := b64.DecodeString(s)
			if err != nil || len(b) == 0 {
				return nil
			}
			return new(big.Int).SetBytes(b)
		}
		switch k.Kty {
		case "RSA":
			n, e := num(k.N), num(k.E)
			if n == nil || e == nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 || n.BitLen() < 2048 {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
		case "EC":
			curve := map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521()}[k.Crv]
			x, y := num(k.X), num(k.Y)
			if curve == nil || x == nil || y == nil {
				continue
			}
			key := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
			// Refuses a point that is not on the curve.
			if _, err := key.ECDH(); err != nil {
				continue
			}
			keys[k.Kid] = key
		case "OKP":
			x, err := b64.DecodeString(k.X)
			if k.Crv != "Ed25519" || err != nil || len(x) != ed25519.PublicKeySize {
				continue
			}
			keys[k.Kid] = ed25519.PublicKey(x)
		}
		// Symmetric keys ("oct") are never taken from a URL.
	}
	if len(keys) == 0 {
		return nil, errors.New("the key set has no key to verify signatures with")
	}
	return keys, nil
}
