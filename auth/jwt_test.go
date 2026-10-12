// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	jwtNow     = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	rsaKey, _  = rsa.GenerateKey(rand.Reader, 2048)
	rsaKey2, _ = rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _   = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ec384, _   = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	edPub, ed  = func() (ed25519.PublicKey, ed25519.PrivateKey) {
		p, k, _ := ed25519.GenerateKey(rand.Reader)
		return p, k
	}()
	hmacSecret = []byte("0123456789abcdef0123456789abcdef-hmac")
)

type claims = map[string]any

// good are the claims of a token that every test validator accepts.
func good() claims {
	return claims{"iss": "https://idp.example", "aud": "https://vtn.example", "client_id": "site-42",
		"scope": "read_targets write_reports", "exp": jwtNow.Add(time.Hour).Unix(), "nbf": jwtNow.Add(-time.Minute).Unix()}
}

// sign makes a JWT. key is a private key, or a []byte for an HMAC.
func sign(t testing.TB, alg, kid string, key any, c claims) string {
	t.Helper()
	header := claims{"alg": alg, "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(c)
	signed := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	var alg2 crypto.Hash
	var digest []byte
	switch {
	case strings.HasSuffix(alg, "384"):
		d := sha512.Sum384([]byte(signed))
		alg2, digest = crypto.SHA384, d[:]
	case strings.HasSuffix(alg, "512"):
		d := sha512.Sum512([]byte(signed))
		alg2, digest = crypto.SHA512, d[:]
	default:
		d := sha256.Sum256([]byte(signed))
		alg2, digest = crypto.SHA256, d[:]
	}
	var sig []byte
	var err error
	switch key := key.(type) {
	case *rsa.PrivateKey:
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, alg2, digest)
	case *ecdsa.PrivateKey:
		var r, s *big.Int
		r, s, err = ecdsa.Sign(rand.Reader, key, digest)
		size := (key.Curve.Params().BitSize + 7) / 8
		sig = append(r.FillBytes(make([]byte, size)), s.FillBytes(make([]byte, size))...)
	case ed25519.PrivateKey:
		sig = ed25519.Sign(key, []byte(signed))
	case []byte:
		mac := hmac.New(map[crypto.Hash]func() hash.Hash{crypto.SHA256: sha256.New, crypto.SHA384: sha512.New384, crypto.SHA512: sha512.New}[alg2], key)
		mac.Write([]byte(signed))
		sig = mac.Sum(nil)
	case nil:
	default:
		t.Fatalf("cannot sign with a %T", key)
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64.EncodeToString(sig)
}

func validator(t testing.TB, cfg JWTConfig) *JWTValidator {
	t.Helper()
	cfg.Now = func() time.Time { return jwtNow }
	v, err := NewJWTValidator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestJWTAlgorithms(t *testing.T) {
	ctx := context.Background()
	v := validator(t, JWTConfig{Keys: []JWTKey{
		{ID: "rsa", Key: &rsaKey.PublicKey}, {ID: "ec", Key: &ecKey.PublicKey}, {ID: "ec384", Key: &ec384.PublicKey},
		{ID: "ed", Key: edPub}, {ID: "hmac", Key: hmacSecret},
	}})
	for _, tc := range []struct {
		alg, kid string
		key      any
	}{
		{"RS256", "rsa", rsaKey}, {"RS384", "rsa", rsaKey}, {"RS512", "rsa", rsaKey},
		{"ES256", "ec", ecKey}, {"ES384", "ec384", ec384}, {"EdDSA", "ed", ed},
		{"HS256", "hmac", hmacSecret}, {"HS384", "hmac", hmacSecret}, {"HS512", "hmac", hmacSecret},
	} {
		id, err := v.Validate(ctx, sign(t, tc.alg, tc.kid, tc.key, good()))
		if err != nil || id.ClientID != "site-42" || !slices.Equal(id.Scopes, []string{"read_targets", "write_reports"}) {
			t.Errorf("%s: %+v, %v", tc.alg, id, err)
		}
	}
}

func TestJWTRejects(t *testing.T) {
	ctx := context.Background()
	v := validator(t, JWTConfig{
		Keys:   []JWTKey{{ID: "rsa", Key: &rsaKey.PublicKey}, {ID: "ec", Key: &ecKey.PublicKey}, {ID: "hmac", Key: hmacSecret}},
		Issuer: "https://idp.example", Audiences: []string{"https://vtn.example", "https://other.example"},
	})
	with := func(change func(claims)) claims { c := good(); change(c); return c }
	valid := sign(t, "RS256", "rsa", rsaKey, good())
	parts := strings.Split(valid, ".")
	// The public key, as an attacker would use it for an HMAC.
	public, _ := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)

	for name, token := range map[string]string{
		"empty":                      "",
		"not a JWT":                  "opaque-token",
		"two parts":                  parts[0] + "." + parts[1],
		"no signature":               parts[0] + "." + parts[1] + ".",
		"alg none":                   sign(t, "none", "rsa", nil, good()),
		"alg none, upper case":       sign(t, "NONE", "", nil, good()),
		"HMAC with the public key":   sign(t, "HS256", "rsa", public, good()),
		"signed by another key":      sign(t, "RS256", "rsa", rsaKey2, good()),
		"key of another kind":        sign(t, "RS256", "ec", rsaKey, good()),
		"unknown key ID":             sign(t, "RS256", "nope", rsaKey, good()),
		"no key ID, several keys":    sign(t, "RS256", "", rsaKey, good()),
		"curve of another algorithm": sign(t, "ES384", "ec", ecKey, good()),
		"algorithm of another key":   sign(t, "ES256", "rsa", ecKey, good()),
		"payload changed":            parts[0] + "." + b64.EncodeToString([]byte(`{"client_id":"bl","scope":"read_all","exp":9999999999}`)) + "." + parts[2],
		"signature cut":              valid[:len(valid)-4],
		"expired":                    sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { c["exp"] = jwtNow.Add(-time.Second).Unix() })),
		"expires now":                sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { c["exp"] = jwtNow.Unix() })),
		"no expiry":                  sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { delete(c, "exp") })),
		"expiry as a string":         sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { c["exp"] = "tomorrow" })),
		"not yet valid":              sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { c["nbf"] = jwtNow.Add(time.Minute).Unix() })),
		"another issuer":             sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { c["iss"] = "https://evil.example" })),
		"no issuer":                  sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { delete(c, "iss") })),
		"another audience":           sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { c["aud"] = "https://billing.example" })),
		"no audience":                sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { delete(c, "aud") })),
		"no client":                  sign(t, "RS256", "rsa", rsaKey, with(func(c claims) { delete(c, "client_id") })),
		"payload not JSON":           parts[0] + "." + b64.EncodeToString([]byte("hello")) + "." + parts[2],
	} {
		if id, err := v.Validate(ctx, token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: accepted as %+v (%v)", name, id, err)
		}
	}
	if _, err := v.Validate(ctx, valid); err != nil {
		t.Fatalf("the control token: %v", err)
	}
}

func TestJWTClaims(t *testing.T) {
	ctx := context.Background()
	key := []JWTKey{{Key: hmacSecret}}
	tok := func(c claims) string {
		c["exp"] = jwtNow.Add(time.Hour).Unix()
		return sign(t, "HS256", "", hmacSecret, c)
	}

	v := validator(t, JWTConfig{Keys: key, Audiences: []string{"vtn"}, Leeway: 30 * time.Second})
	for name, tc := range map[string]struct {
		claims claims
		client string
		scopes []string
	}{
		"client_id first":    {claims{"aud": "vtn", "client_id": "a", "azp": "b", "sub": "c"}, "a", nil},
		"then azp":           {claims{"aud": "vtn", "azp": "b", "sub": "c"}, "b", nil},
		"then sub":           {claims{"aud": "vtn", "sub": "c"}, "c", nil},
		"scope as a string":  {claims{"aud": "vtn", "sub": "c", "scope": "read_all  write_events"}, "c", []string{"read_all", "write_events"}},
		"scp as an array":    {claims{"aud": "vtn", "sub": "c", "scp": []string{"read_all"}}, "c", []string{"read_all"}},
		"scope before scp":   {claims{"aud": "vtn", "sub": "c", "scope": "a", "scp": []string{"b"}}, "c", []string{"a"}},
		"audience in a list": {claims{"aud": []string{"other", "vtn"}, "sub": "c"}, "c", nil},
	} {
		id, err := v.Validate(ctx, tok(tc.claims))
		if err != nil || id.ClientID != tc.client || !slices.Equal(id.Scopes, tc.scopes) {
			t.Errorf("%s: %+v, %v", name, id, err)
		}
	}
	// Leeway, both ways.
	c := claims{"aud": "vtn", "sub": "c", "exp": jwtNow.Add(-10 * time.Second).Unix(), "nbf": jwtNow.Add(10 * time.Second).Unix()}
	if _, err := v.Validate(ctx, sign(t, "HS256", "", hmacSecret, c)); err != nil {
		t.Errorf("a token 10 s outside its time with 30 s leeway: %v", err)
	}
	// Named claims are the only ones looked at.
	named := validator(t, JWTConfig{Keys: key, ClientIDClaim: "cid", ScopeClaim: "permissions"})
	id, err := named.Validate(ctx, tok(claims{"cid": "x", "client_id": "y", "permissions": []string{"read_all"}, "scope": "write_events"}))
	if err != nil || id.ClientID != "x" || !slices.Equal(id.Scopes, []string{"read_all"}) {
		t.Errorf("named claims: %+v, %v", id, err)
	}
	if _, err := named.Validate(ctx, tok(claims{"client_id": "y"})); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token without the named client claim: %v", err)
	}
}

func TestJWTConfig(t *testing.T) {
	for name, cfg := range map[string]JWTConfig{
		"nothing to verify with": {},
		"short HMAC secret":      {Keys: []JWTKey{{Key: []byte("short")}}},
		"a private key":          {Keys: []JWTKey{{Key: rsaKey}}},
		"a key by value":         {Keys: []JWTKey{{Key: rsaKey.PublicKey}}},
		"a key set by file":      {JWKSURL: "file:///etc/keys.json"},
	} {
		if _, err := NewJWTValidator(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func jwk(kid string, key any) claims {
	switch key := key.(type) {
	case *rsa.PublicKey:
		return claims{"kty": "RSA", "kid": kid, "use": "sig", "n": b64.EncodeToString(key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
	case *ecdsa.PublicKey:
		size := (key.Curve.Params().BitSize + 7) / 8
		return claims{"kty": "EC", "kid": kid, "crv": key.Curve.Params().Name,
			"x": b64.EncodeToString(key.X.FillBytes(make([]byte, size))), "y": b64.EncodeToString(key.Y.FillBytes(make([]byte, size)))}
	case ed25519.PublicKey:
		return claims{"kty": "OKP", "kid": kid, "crv": "Ed25519", "x": b64.EncodeToString(key)}
	}
	return nil
}

func TestJWKS(t *testing.T) {
	ctx := context.Background()
	var (
		fetches atomic.Int64
		down    atomic.Bool
		set     atomic.Value
	)
	set.Store([]claims{
		jwk("rsa-1", &rsaKey.PublicKey), jwk("ec-1", &ecKey.PublicKey), jwk("ed-1", edPub),
		{"kty": "oct", "kid": "sym", "k": b64.EncodeToString(hmacSecret)},       // never trusted from a URL
		{"kty": "RSA", "kid": "enc", "use": "enc", "n": "AQAB", "e": "AQAB"},    // not for signatures
		{"kty": "EC", "kid": "off-curve", "crv": "P-256", "x": "AQ", "y": "Ag"}, // not a point
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(claims{"keys": set.Load()})
	}))
	defer srv.Close()

	now := jwtNow
	v, err := NewJWTValidator(JWTConfig{JWKSURL: srv.URL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 0 {
		t.Error("NewJWTValidator contacted the provider")
	}
	for kid, key := range map[string]any{"rsa-1": rsaKey, "ec-1": ecKey, "ed-1": ed} {
		alg := map[string]string{"rsa-1": "RS256", "ec-1": "ES256", "ed-1": "EdDSA"}[kid]
		if _, err := v.Validate(ctx, sign(t, alg, kid, key, good())); err != nil {
			t.Errorf("token signed with %s from the key set: %v", kid, err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("%d fetches for three tokens with known keys, want 1", n)
	}
	if _, err := v.Validate(ctx, sign(t, "HS256", "sym", hmacSecret, good())); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a symmetric key from the key set was used: %v", err)
	}

	// Tokens with made-up key IDs do not make the validator hammer the provider.
	for i := range 20 {
		_, _ = v.Validate(ctx, sign(t, "RS256", fmt.Sprintf("made-up-%d", i), rsaKey2, good()))
	}
	if n := fetches.Load(); n > 2 {
		t.Errorf("%d fetches after 20 tokens with unknown key IDs", n)
	}

	// The provider rotates its key: found once the interval has passed.
	set.Store([]claims{jwk("rsa-2", &rsaKey2.PublicKey)})
	rotated := sign(t, "RS256", "rsa-2", rsaKey2, good())
	if _, err := v.Validate(ctx, rotated); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a new key within the refetch interval: %v", err)
	}
	now = now.Add(2 * refetchAfter)
	if _, err := v.Validate(ctx, rotated); err != nil {
		t.Errorf("a new key after the refetch interval: %v", err)
	}
	if _, err := v.Validate(ctx, sign(t, "RS256", "rsa-1", rsaKey, good())); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a key the provider withdrew is still accepted: %v", err)
	}

	// The provider goes down: known keys keep working.
	down.Store(true)
	now = now.Add(2 * refetchAfter)
	if _, err := v.Validate(ctx, rotated); err != nil {
		t.Errorf("known key while the provider is down: %v", err)
	}
	_, _ = v.Validate(ctx, sign(t, "RS256", "another", rsaKey2, good()))
	if _, err := v.Validate(ctx, rotated); err != nil {
		t.Errorf("known key after a failed refetch: %v", err)
	}
	// A validator that never had keys cannot say a token is invalid: that
	// is an error of the VTN, not of the client.
	cold, _ := NewJWTValidator(JWTConfig{JWKSURL: srv.URL, Now: func() time.Time { return now }})
	if _, err := cold.Validate(ctx, rotated); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Errorf("no key set available: %v, want an error that is not ErrInvalidToken", err)
	}
}
