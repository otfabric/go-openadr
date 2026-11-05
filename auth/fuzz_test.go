// SPDX-License-Identifier: MIT

package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// FuzzJWTValidate feeds untrusted tokens to a validator holding a key of
// every kind. It must not panic, and must accept nothing but the header
// and payload of a token that was signed: whatever the fuzzer does to the
// signature, the header or the payload, it cannot forge one.
func FuzzJWTValidate(f *testing.F) {
	public, _ := json.Marshal(claims{"n": rsaKey.N.String()})
	tokens := []string{
		sign(f, "RS256", "rsa", rsaKey, good()),
		sign(f, "ES256", "ec", ecKey, good()),
		sign(f, "ES384", "ec384", ec384, good()),
		sign(f, "EdDSA", "ed", ed, good()),
		sign(f, "HS256", "hmac", hmacSecret, good()),
		sign(f, "HS512", "hmac", hmacSecret, good()),
		sign(f, "none", "rsa", nil, good()),
		sign(f, "HS256", "rsa", public, good()),
		sign(f, "HS256", "", hmacSecret, good()),
		signHS256(claims{"alg": "HS256", "kid": "hmac", "crit": []string{"x"}}, good()),
	}
	signed := map[string]bool{}
	for _, tok := range tokens {
		f.Add(tok)
		signed[tok[:strings.LastIndexByte(tok, '.')]] = true
	}
	f.Add("")
	f.Add("..")
	f.Add("e30.e30.")
	v := validator(f, JWTConfig{Keys: []JWTKey{
		{ID: "rsa", Key: &rsaKey.PublicKey}, {ID: "ec", Key: &ecKey.PublicKey}, {ID: "ec384", Key: &ec384.PublicKey},
		{ID: "ed", Key: edPub}, {ID: "hmac", Key: hmacSecret},
	}})
	f.Fuzz(func(t *testing.T, token string) {
		id, err := v.Validate(context.Background(), token)
		if err != nil {
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Validate(%q): %v is not ErrInvalidToken", token, err)
			}
			return
		}
		if i := strings.LastIndexByte(token, '.'); i < 0 || !signed[token[:i]] {
			t.Fatalf("accepted a token that was not signed: %q as %+v", token, id)
		}
		if id.ClientID == "" {
			t.Fatalf("accepted %q without a client", token)
		}
	})
}

// FuzzParseKeySet reads untrusted key sets. It must not panic, and every
// key it returns must be one verify can use without panicking.
func FuzzParseKeySet(f *testing.F) {
	body, _ := json.Marshal(claims{"keys": []claims{
		jwk("rsa", &rsaKey.PublicKey), jwk("ec", &ecKey.PublicKey), jwk("ec384", &ec384.PublicKey), jwk("ed", edPub),
		{"kty": "oct", "kid": "sym", "k": b64.EncodeToString(hmacSecret)},
		{"kty": "EC", "kid": "off", "crv": "P-256", "x": "AQ", "y": "Ag"},
		{"kty": "EC", "kid": "long", "crv": "P-521", "x": strings.Repeat("A", 90), "y": strings.Repeat("_", 90)},
		{"kty": "RSA", "kid": "e", "n": "AQAB", "e": "AQAB"},
	}})
	f.Add(body)
	f.Add([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}]}`))
	f.Add([]byte(`{"keys":null}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		keys, err := parseKeySet(bytes.NewReader(data))
		if err != nil {
			if keys != nil {
				t.Fatal("keys with an error")
			}
			return
		}
		if len(keys) == 0 {
			t.Fatal("no keys and no error")
		}
		for kid, key := range keys {
			switch key := key.(type) {
			case *rsa.PublicKey:
				if bits := key.N.BitLen(); bits < 2048 || bits > maxRSABits || key.E < 3 {
					t.Fatalf("%s: RSA key of %d bits, e %d", kid, bits, key.E)
				}
			case *ecdsa.PublicKey:
				if _, err := key.Bytes(); err != nil {
					t.Fatalf("%s: ECDSA key that is not a point: %v", kid, err)
				}
			case ed25519.PublicKey:
				if len(key) != ed25519.PublicKeySize {
					t.Fatalf("%s: Ed25519 key of %d bytes", kid, len(key))
				}
			default:
				t.Fatalf("%s: a %T from a key set", kid, key)
			}
			for _, alg := range []string{"RS256", "ES256", "ES384", "ES512", "EdDSA", "HS256", ""} {
				if verify(alg, key, []byte("x.y"), make([]byte, 64)) {
					t.Fatalf("%s: verified a signature of zeros with %s", kid, alg)
				}
			}
		}
	})
}

// FuzzIssuer posts untrusted token requests. The endpoint must answer
// 200 only for the client's own credentials, with no more than its scopes,
// answer everything else as an OAuth2 error, and never hold more tokens
// than its bound.
func FuzzIssuer(f *testing.F) {
	f.Add("grant_type=client_credentials&client_id=ven&client_secret=s%2Bcret", "", "")
	f.Add("grant_type=client_credentials&scope=read_targets+read_targets", "ven", "s+cret")
	f.Add("grant_type=client_credentials", "ven", "s%2Bcret")
	f.Add("grant_type=client_credentials&scope=read_all", "ven", "s+cret")
	f.Add("grant_type=password&username=ven", "", "")
	f.Add("grant_type=client_credentials&client_id=%zz", "%zz", "%")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	is, err := NewIssuer(IssuerConfig{Clients: []Client{{ID: "ven", Secret: "s+cret", Scopes: VENScopes()}}, Now: func() time.Time { return now }})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, form, user, password string) {
		req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if user != "" || password != "" {
			req.SetBasicAuth(user, password)
		}
		rec := httptest.NewRecorder()
		is.ServeHTTP(rec, req)
		var body struct {
			AccessToken string `json:"access_token"`
			Scope       string `json:"scope"`
			Error       string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%d with a body that is not JSON: %q", rec.Code, rec.Body)
		}
		switch rec.Code {
		case http.StatusOK:
			id, err := is.Validate(context.Background(), body.AccessToken)
			if err != nil || id.ClientID != "ven" {
				t.Fatalf("issued %q, which validates as %+v, %v", body.AccessToken, id, err)
			}
			for _, s := range id.Scopes {
				if !id.HasScope(s) || !strings.Contains(" "+strings.Join(VENScopes(), " ")+" ", " "+s+" ") {
					t.Fatalf("issued scope %q", s)
				}
			}
		case http.StatusBadRequest, http.StatusUnauthorized:
			if body.Error == "" {
				t.Fatalf("%d without an error code", rec.Code)
			}
		default:
			t.Fatalf("answered %d", rec.Code)
		}
		is.mu.Lock()
		n := len(is.tokens)
		is.mu.Unlock()
		if n > maxTokensPerClient {
			t.Fatalf("%d tokens held", n)
		}
	})
}
