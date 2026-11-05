// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// signHS256 makes a token with the given header, for headers sign does not write.
func signHS256(header, c claims) string {
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(c)
	signed := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	mac := hmac.New(sha256.New, hmacSecret)
	mac.Write([]byte(signed))
	return signed + "." + b64.EncodeToString(mac.Sum(nil))
}

// signRaw makes an HS256 token of the given header and payload, as they are.
func signRaw(header, payload string) string {
	signed := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, hmacSecret)
	mac.Write([]byte(signed))
	return signed + "." + b64.EncodeToString(mac.Sum(nil))
}

// TestJWTConfigKeys checks that keys verify could not use are refused when
// the validator is made. A short Ed25519 key made ed25519.Verify panic with
// every EdDSA token, and a nil RSA or ECDSA key did the same.
func TestJWTConfigKeys(t *testing.T) {
	for name, key := range map[string]any{
		"nil RSA key":           (*rsa.PublicKey)(nil),
		"RSA key without N":     &rsa.PublicKey{E: 65537},
		"RSA key with N zero":   &rsa.PublicKey{N: new(big.Int), E: 65537},
		"RSA key with E 1":      &rsa.PublicKey{N: rsaKey.N, E: 1},
		"nil ECDSA key":         (*ecdsa.PublicKey)(nil),
		"ECDSA key, no curve":   &ecdsa.PublicKey{},
		"ECDSA key, no point":   &ecdsa.PublicKey{Curve: elliptic.P256()},
		"short Ed25519 key":     edPub[:31],
		"long Ed25519 key":      append(append(ed25519.PublicKey{}, edPub...), 0),
		"empty Ed25519 key":     ed25519.PublicKey{},
		"nil":                   nil,
		"string as HMAC secret": strings.Repeat("x", 64),
	} {
		if _, err := NewJWTValidator(JWTConfig{Keys: []JWTKey{{ID: "k", Key: key}}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestJWTEdges(t *testing.T) {
	ctx := context.Background()
	v := validator(t, JWTConfig{Keys: []JWTKey{{Key: hmacSecret}}, Issuer: "https://idp.example", Audiences: []string{"https://vtn.example"}})
	with := func(change func(claims)) claims { c := good(); change(c); return c }
	header := claims{"alg": "HS256", "typ": "JWT"}
	valid := signHS256(header, good())
	parts := strings.Split(valid, ".")

	for name, token := range map[string]string{
		"crit":                     signHS256(claims{"alg": "HS256", "crit": []string{"exp"}, "exp": 1}, good()),
		"crit null":                signHS256(claims{"alg": "HS256", "crit": nil}, good()),
		"b64 false":                signHS256(claims{"alg": "HS256", "b64": false, "crit": []string{"b64"}}, good()),
		"header an array":          b64.EncodeToString([]byte(`["HS256"]`)) + "." + parts[1] + "." + parts[2],
		"header not base64":        "!!." + parts[1] + "." + parts[2],
		"header padded":            parts[0] + "=." + parts[1] + "." + parts[2],
		"payload not base64":       parts[0] + ".%%." + parts[2],
		"signature not base64":     parts[0] + "." + parts[1] + ".**",
		"four parts":               valid + ".x",
		"alg empty":                signHS256(claims{"alg": ""}, good()),
		"alg short":                signHS256(claims{"alg": "HS"}, good()),
		"alg lower case":           signHS256(claims{"alg": "hs256"}, good()),
		"nbf a string":             signHS256(header, with(func(c claims) { c["nbf"] = "yesterday" })),
		"nbf null":                 signHS256(header, with(func(c claims) { c["nbf"] = nil })),
		"nbf negative":             signHS256(header, with(func(c claims) { c["nbf"] = -1 })),
		"exp beyond NumericDate":   signHS256(header, with(func(c claims) { c["exp"] = 1e12 + 1 })),
		"exp negative":             signHS256(header, with(func(c claims) { c["exp"] = -1 })),
		"exp null":                 signHS256(header, with(func(c claims) { c["exp"] = nil })),
		"iss not a string":         signHS256(header, with(func(c claims) { c["iss"] = []string{"https://idp.example"} })),
		"aud of other types":       signHS256(header, with(func(c claims) { c["aud"] = []any{1, true, nil} })),
		"aud an object":            signHS256(header, with(func(c claims) { c["aud"] = claims{"https://vtn.example": true} })),
		"client_id empty":          signHS256(header, with(func(c claims) { c["client_id"] = "" })),
		"client_id not a string":   signHS256(header, with(func(c claims) { c["client_id"] = 42 })),
		"payload a JSON array":     signRaw(`{"alg":"HS256"}`, `[1]`),
		"payload JSON with junk":   signRaw(`{"alg":"HS256"}`, `{"exp":9999999999,"client_id":"x"}x`),
		"one megabyte of nonsense": strings.Repeat("A", 1<<20) + "." + strings.Repeat("B", 1<<20) + "." + strings.Repeat("C", 1<<20),
	} {
		if id, err := v.Validate(ctx, token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: accepted as %+v (%v)", name, id, err)
		}
	}

	// What is accepted at the edges.
	for name, c := range map[string]claims{
		"exp with a fraction":  with(func(c claims) { c["exp"] = float64(jwtNow.Unix()) + 0.5 + 3600 }),
		"aud among others":     with(func(c claims) { c["aud"] = []any{1, "https://vtn.example"} }),
		"no nbf":               with(func(c claims) { delete(c, "nbf") }),
		"nbf now":              with(func(c claims) { c["nbf"] = jwtNow.Unix() }),
		"scope of other types": with(func(c claims) { c["scope"] = []any{"read_all", 7} }),
	} {
		if _, err := v.Validate(ctx, signHS256(header, c)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Scopes that are neither a string nor a list are no scopes.
	id, err := v.Validate(ctx, signHS256(header, with(func(c claims) { c["scope"] = 12 })))
	if err != nil || id.Scopes != nil {
		t.Errorf("scope a number: %+v, %v", id, err)
	}
}

// TestJWKSDown checks that a provider that is down is not asked again with
// every token. Before the key set had been had once, every request fetched
// it, holding up all the others for as long as the fetch took.
func TestJWKSDown(t *testing.T) {
	ctx := context.Background()
	var fetches atomic.Int64
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(claims{"keys": []claims{jwk("rsa-1", &rsaKey.PublicKey)}})
	}))
	defer srv.Close()
	now := jwtNow
	v, err := NewJWTValidator(JWTConfig{JWKSURL: srv.URL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	token := sign(t, "RS256", "rsa-1", rsaKey, good())
	for range 20 {
		if _, err := v.Validate(ctx, token); err == nil || errors.Is(err, ErrInvalidToken) {
			t.Fatalf("no key set: %v, want an error that is not ErrInvalidToken", err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("%d fetches for 20 tokens while the provider is down, want 1", n)
	}
	up.Store(true)
	if _, err := v.Validate(ctx, token); err == nil {
		t.Error("the key set was fetched again within the interval")
	}
	now = now.Add(refetchAfter)
	if _, err := v.Validate(ctx, token); err != nil {
		t.Errorf("after the interval, with the provider back: %v", err)
	}

	// A validator with keys of its own uses them while the provider is down.
	up.Store(false)
	both, err := NewJWTValidator(JWTConfig{Keys: []JWTKey{{ID: "local", Key: hmacSecret}}, JWKSURL: srv.URL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := both.Validate(ctx, sign(t, "HS256", "local", hmacSecret, good())); err != nil {
		t.Errorf("a configured key while the provider is down: %v", err)
	}

	// A provider that cannot be reached at all.
	srv.Close()
	gone, _ := NewJWTValidator(JWTConfig{JWKSURL: srv.URL, Now: func() time.Time { return now }})
	if _, err := gone.Validate(ctx, token); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Errorf("unreachable provider: %v", err)
	}
	bad, _ := NewJWTValidator(JWTConfig{JWKSURL: "http://[::1]:namedport", Now: func() time.Time { return now }})
	if _, err := bad.Validate(ctx, token); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Errorf("a key set URL that is not a URL: %v", err)
	}
}

// paddedKey returns a P-256 key whose x coordinate begins with a zero byte,
// found deterministically, so that a key set may write x in fewer bytes.
func paddedKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	scalar := make([]byte, 32)
	for i := 1; i < 1<<16; i++ {
		scalar[30], scalar[31] = byte(i>>8), byte(i)
		k, err := ecdh.P256().NewPrivateKey(scalar)
		if err != nil {
			continue
		}
		if point := k.PublicKey().Bytes(); point[1] == 0 {
			key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
			if err != nil {
				t.Fatal(err)
			}
			return key
		}
	}
	t.Fatal("no P-256 key with a short x below 2^16")
	return nil
}

func TestParseKeySet(t *testing.T) {
	ecPoint, _ := ecKey.PublicKey.Bytes()
	ecX, ecY := ecPoint[1:33], ecPoint[33:]
	offCurveY := new(big.Int).Add(new(big.Int).SetBytes(ecY), big.NewInt(1)).FillBytes(make([]byte, 32))
	padded := paddedKey(t)
	paddedPoint, _ := padded.PublicKey.Bytes()
	small := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 1023), big.NewInt(1)) // 1024 bits
	huge := new(big.Int).Lsh(big.NewInt(1), maxRSABits)                             // maxRSABits+1 bits
	enc := b64.EncodeToString

	for name, tc := range map[string]struct {
		key  claims
		kept bool
	}{
		"RSA":                      {jwk("k", &rsaKey.PublicKey), true},
		"RSA of 1024 bits":         {claims{"kty": "RSA", "kid": "k", "n": enc(small.Bytes()), "e": "AQAB"}, false},
		"RSA too large":            {claims{"kty": "RSA", "kid": "k", "n": enc(huge.Bytes()), "e": "AQAB"}, false},
		"RSA with e 1":             {claims{"kty": "RSA", "kid": "k", "n": enc(rsaKey.N.Bytes()), "e": "AQ"}, false},
		"RSA with e of 2^31":       {claims{"kty": "RSA", "kid": "k", "n": enc(rsaKey.N.Bytes()), "e": enc(big.NewInt(1 << 31).Bytes())}, false},
		"RSA with e of 9 bytes":    {claims{"kty": "RSA", "kid": "k", "n": enc(rsaKey.N.Bytes()), "e": enc(make([]byte, 9))}, false},
		"RSA with n not base64":    {claims{"kty": "RSA", "kid": "k", "n": "*", "e": "AQAB"}, false},
		"RSA without e":            {claims{"kty": "RSA", "kid": "k", "n": enc(rsaKey.N.Bytes())}, false},
		"EC":                       {jwk("k", &ecKey.PublicKey), true},
		"EC P-384":                 {jwk("k", &ec384.PublicKey), true},
		"EC off the curve":         {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(ecX), "y": enc(offCurveY)}, false},
		"EC x too long":            {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(append([]byte{1}, ecX...)), "y": enc(ecY)}, false},
		"EC y too long":            {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(ecX), "y": enc(append([]byte{1}, ecY...))}, false},
		"EC P-384 x on P-256":      {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(make([]byte, 48)), "y": enc(ecY)}, false},
		"EC x with a leading zero": {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(append([]byte{0}, ecX...)), "y": enc(ecY)}, true},
		"EC x short, padded":       {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(paddedPoint[2:33]), "y": enc(paddedPoint[33:])}, true},
		"EC unknown curve":         {claims{"kty": "EC", "kid": "k", "crv": "P-192", "x": enc(ecX), "y": enc(ecY)}, false},
		"EC secp256k1":             {claims{"kty": "EC", "kid": "k", "crv": "secp256k1", "x": enc(ecX), "y": enc(ecY)}, false},
		"EC without crv":           {claims{"kty": "EC", "kid": "k", "x": enc(ecX), "y": enc(ecY)}, false},
		"EC without y":             {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": enc(ecX)}, false},
		"EC at infinity":           {claims{"kty": "EC", "kid": "k", "crv": "P-256", "x": "AA", "y": "AA"}, false},
		"Ed25519":                  {jwk("k", edPub), true},
		"X25519":                   {claims{"kty": "OKP", "kid": "k", "crv": "X25519", "x": enc(edPub)}, false},
		"Ed25519 short":            {claims{"kty": "OKP", "kid": "k", "crv": "Ed25519", "x": enc(edPub[:31])}, false},
		"Ed25519 not base64":       {claims{"kty": "OKP", "kid": "k", "crv": "Ed25519", "x": "*"}, false},
		"for encryption":           {claims{"kty": "RSA", "kid": "k", "use": "enc", "n": enc(rsaKey.N.Bytes()), "e": "AQAB"}, false},
		"symmetric":                {claims{"kty": "oct", "kid": "k", "k": enc(hmacSecret)}, false},
		"no kty":                   {claims{"kid": "k", "n": enc(rsaKey.N.Bytes()), "e": "AQAB"}, false},
	} {
		body, _ := json.Marshal(claims{"keys": []claims{tc.key}})
		keys, err := parseKeySet(strings.NewReader(string(body)))
		if tc.kept && (err != nil || keys["k"] == nil) || !tc.kept && err == nil {
			t.Errorf("%s: %v, %v; kept should be %v", name, keys, err, tc.kept)
		}
	}

	// The padded key verifies what its private key signs.
	body, _ := json.Marshal(claims{"keys": []claims{{"kty": "EC", "kid": "p", "crv": "P-256", "x": enc(paddedPoint[2:33]), "y": enc(paddedPoint[33:])}}})
	keys, err := parseKeySet(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	v := validator(t, JWTConfig{Keys: []JWTKey{{ID: "p", Key: keys["p"]}}})
	if _, err := v.Validate(context.Background(), sign(t, "ES256", "p", padded, good())); err != nil {
		t.Errorf("a token signed by the key with the short x: %v", err)
	}

	for name, body := range map[string]string{
		"not JSON":     "keys",
		"keys absent":  "{}",
		"keys empty":   `{"keys":[]}`,
		"keys object":  `{"keys":{}}`,
		"over a limit": `{"keys":[` + strings.Repeat(`{"kty":"none"},`, maxKeySet/15+1) + `{}]}`,
	} {
		if keys, err := parseKeySet(strings.NewReader(body)); err == nil {
			t.Errorf("%s: %v", name, keys)
		}
	}
}

func TestIssuerHardening(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := "a+b/c=%41 d"
	is, err := NewIssuer(IssuerConfig{
		Clients: []Client{
			{ID: "ven 1", Secret: secret, Scopes: VENScopes()},
			{ID: "other", Secret: "other-secret", Scopes: VENScopes()},
		},
		TokenTTL: time.Minute,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string, header ...string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", "/auth/token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if len(header) == 1 {
			req.Header.Set("Authorization", header[0])
		} else if len(header) == 2 {
			req.SetBasicAuth(header[0], header[1])
		}
		rec := httptest.NewRecorder()
		is.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	grant := "grant_type=client_credentials"

	// RFC 6749 section 2.3.1: Basic credentials are form-encoded first, as
	// golang.org/x/oauth2 sends them. Those of a client that does not encode
	// them work too.
	for name, creds := range map[string][2]string{
		"form-encoded": {url.QueryEscape("ven 1"), url.QueryEscape(secret)},
		"as they are":  {"ven 1", secret},
	} {
		if rec, body := post(grant, creds[0], creds[1]); rec.Code != 200 {
			t.Errorf("Basic %s: %d %v", name, rec.Code, body)
		}
	}
	for name, header := range map[string][]string{
		"wrong secret":          {"ven 1", "nope"},
		"bad escape":            {"ven%zz", "%zz"},
		"other client's secret": {"ven 1", "other-secret"},
		"not Basic":             {"Bearer x"},
		"Basic not base64":      {"Basic !!!"},
	} {
		rec, body := post(grant, header...)
		if rec.Code != 401 || body["error"] != "invalid_client" {
			t.Errorf("%s: %d %v", name, rec.Code, body)
		}
		if len(header) == 2 && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate", name)
		}
	}

	// The form.
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"bad escape":       {"grant_type=client_credentials&client_id=%zz", 400},
		"too large":        {grant + "&x=" + strings.Repeat("a", 1<<16), 400},
		"grant in query":   {"client_id=other&client_secret=other-secret", 400},
		"credentials only": {grant + "&client_id=other&client_secret=other-secret", 200},
	} {
		if rec, body := post(tc.body); rec.Code != tc.status {
			t.Errorf("%s: %d %v, want %d", name, rec.Code, body, tc.status)
		}
	}

	// Scopes are a set, and what is not a scope-token is not repeated.
	rec, body := post(grant+"&scope="+url.QueryEscape("write_reports  write_reports\tread_targets"), "other", "other-secret")
	if rec.Code != 200 || body["scope"] != "write_reports read_targets" {
		t.Errorf("repeated scopes: %d %v", rec.Code, body)
	}
	for scope, named := range map[string]bool{
		"read_all":               true,
		`evil"scope`:             false,
		"café":                   false,
		strings.Repeat("s", 129): false,
		`back\slash`:             false,
		strings.Repeat("s", 128): true,
		"\x7f":                   false,
		"\x21#[]~":               true,
	} {
		rec, body := post(grant+"&scope="+url.QueryEscape(scope), "other", "other-secret")
		description, _ := body["error_description"].(string)
		if rec.Code != 400 || body["error"] != "invalid_scope" || strings.Contains(description, scope) != named {
			t.Errorf("scope %q: %d %v; named in the description should be %v", scope, rec.Code, body, named)
		}
	}

	// A client asking for token after token does not grow the map without
	// bound: its oldest makes room, and no other client loses one.
	_, body = post(grant + "&client_id=other&client_secret=other-secret")
	others := body["access_token"].(string)
	var first, last string
	for i := range maxTokensPerClient + 10 {
		now = now.Add(time.Millisecond)
		_, body := post(grant, "ven 1", secret)
		if last = body["access_token"].(string); i == 0 {
			first = last
		}
	}
	is.mu.Lock()
	mine := 0
	for _, g := range is.tokens {
		if g.identity.ClientID == "ven 1" {
			mine++
		}
	}
	is.mu.Unlock()
	if mine != maxTokensPerClient {
		t.Errorf("%d live tokens for one client, want %d", mine, maxTokensPerClient)
	}
	ctx := context.Background()
	if _, err := is.Validate(ctx, first); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the oldest token survived: %v", err)
	}
	if _, err := is.Validate(ctx, last); err != nil {
		t.Errorf("the newest token: %v", err)
	}
	if _, err := is.Validate(ctx, others); err != nil {
		t.Errorf("another client's token: %v", err)
	}
	// Expired tokens go with the next request.
	now = now.Add(time.Hour)
	post(grant, "other", "other-secret")
	is.mu.Lock()
	n := len(is.tokens)
	is.mu.Unlock()
	if n != 1 {
		t.Errorf("%d tokens after all but one expired", n)
	}
}

// TestJWKSOneKey checks that a provider with one key need not name it, and
// that a token naming no key is not tried against several.
func TestJWKSOneKey(t *testing.T) {
	ctx := context.Background()
	var set atomic.Value
	set.Store([]claims{jwk("only", &ecKey.PublicKey)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(claims{"keys": set.Load()})
	}))
	defer srv.Close()
	now := jwtNow
	v, err := NewJWTValidator(JWTConfig{JWKSURL: srv.URL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(ctx, sign(t, "ES256", "", ecKey, good())); err != nil {
		t.Errorf("no kid, one key: %v", err)
	}
	set.Store([]claims{jwk("a", &ecKey.PublicKey), jwk("b", &rsaKey.PublicKey)})
	now = now.Add(refetchAfter)
	if _, err := v.Validate(ctx, sign(t, "ES256", "", ecKey, good())); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("no kid, two keys: %v", err)
	}
	if _, err := v.Validate(ctx, sign(t, "ES256", "a", ecKey, good())); err != nil {
		t.Errorf("kid a of two keys: %v", err)
	}

	f := ValidatorFunc(func(context.Context, string) (Identity, error) { return Identity{ClientID: "x"}, nil })
	if id, err := f.Validate(ctx, "t"); err != nil || id.ClientID != "x" {
		t.Errorf("ValidatorFunc = %+v, %v", id, err)
	}
}
