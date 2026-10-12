// SPDX-License-Identifier: MIT

package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// tokens maps fixed bearer tokens to identities.
var tokens = auth.ValidatorFunc(func(_ context.Context, token string) (auth.Identity, error) {
	switch token {
	case "bl":
		return auth.Identity{ClientID: "bl", Scopes: auth.BusinessLogicScopes()}, nil
	case "ven":
		return auth.Identity{ClientID: "ven", Scopes: auth.VENScopes()}, nil
	case "readonly":
		return auth.Identity{ClientID: "ro", Scopes: []string{auth.ScopeReadAll}}, nil
	case "broken":
		return auth.Identity{}, errors.New("identity provider is down")
	}
	return auth.Identity{}, auth.ErrInvalidToken
})

func newServer(t *testing.T, cfg server.Config) http.Handler {
	t.Helper()
	if cfg.Store == nil {
		cfg.Store = storage.NewMemory()
	}
	cfg.Validator, cfg.TokenURL = tokens, "https://idp.example/token"
	s, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type response struct {
	status int
	header http.Header
	body   string
	json   map[string]any
}

func call(h http.Handler, method, target, token, body string) response {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	out := response{status: rec.Code, header: rec.Header(), body: string(raw)}
	_ = json.Unmarshal(raw, &out.json)
	return out
}

func TestNewRequiresConfig(t *testing.T) {
	for _, cfg := range []server.Config{
		{Validator: tokens, TokenURL: "x"},
		{Store: storage.NewMemory(), TokenURL: "x"},
		{Store: storage.NewMemory(), Validator: tokens},
		// GET /auth/server has to name an absolute URL.
		{Store: storage.NewMemory(), Validator: tokens, TokenURL: "auth/token"},
		{Store: storage.NewMemory(), Validator: tokens, TokenURL: "//idp.example/token"},
	} {
		if _, err := server.New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
}

// Every error carries a problem object whose status matches the response.
func TestProblems(t *testing.T) {
	h := newServer(t, server.Config{MaxBodyBytes: 256})
	created := call(h, "POST", "/programs", "bl", `{"programName":"p"}`)
	if created.status != http.StatusCreated {
		t.Fatalf("POST /programs = %d %s", created.status, created.body)
	}
	id := created.json["id"].(string)

	cases := []struct {
		name                        string
		method, target, token, body string
		want                        int
	}{
		{"no token", "GET", "/programs", "", "", 401},
		{"unknown token", "GET", "/programs", "nope", "", 401},
		{"validator failure", "GET", "/programs", "broken", "", 500},
		{"missing scope", "POST", "/programs", "ven", `{"programName":"x"}`, 403},
		{"read-only token writing", "DELETE", "/programs/" + id, "readonly", "", 403},
		{"unknown path", "GET", "/nowhere", "bl", "", 404},
		{"unknown object", "GET", "/programs/missing", "bl", "", 404},
		{"malformed ID", "GET", "/programs/a.b", "bl", "", 400},
		{"wrong method", "PATCH", "/programs/" + id, "bl", "", 405},
		{"wrong method on a collection", "DELETE", "/events", "bl", "", 405},
		{"not JSON", "POST", "/programs", "bl", `programName=p`, 400},
		{"missing required property", "POST", "/programs", "bl", `{}`, 400},
		{"wrong type", "POST", "/programs", "bl", `{"programName":7}`, 400},
		{"name too long", "POST", "/programs", "bl", `{"programName":"` + strings.Repeat("x", 129) + `"}`, 400},
		{"body too large", "POST", "/programs", "bl", `{"programName":"p2","targets":["` + strings.Repeat("x", 300) + `"]}`, 413},
		{"duplicate name", "POST", "/programs", "bl", `{"programName":"p"}`, 409},
		{"negative skip", "GET", "/programs?skip=-1", "bl", "", 400},
		{"limit not a number", "GET", "/programs?limit=many", "bl", "", 400},
		{"limit over the maximum", "GET", "/events?limit=51", "bl", "", 400},
		{"bad active", "GET", "/events?active=yes", "bl", "", 400},
		{"bad programID filter", "GET", "/events?programID=a/b", "bl", "", 400},
		{"empty target", "GET", "/programs?targets=", "bl", "", 400},
		{"missing discriminator", "POST", "/vens", "bl", `{"venName":"v","clientID":"c"}`, 400},
		{"object type as discriminator", "POST", "/vens", "bl", `{"objectType":"VEN","venName":"v","clientID":"c"}`, 400},
		{"resource without discriminator", "POST", "/resources", "ven", `{"resourceName":"r","venID":"v"}`, 400},
		{"subscriptions", "GET", "/subscriptions", "bl", "", 501},
		{"subscription by ID", "DELETE", "/subscriptions/abc", "bl", "", 501},
		{"notifiers", "GET", "/notifiers", "bl", "", 501},
		{"mqtt topics", "GET", "/notifiers/mqtt/topics/programs", "ven", "", 501},
		{"subscriptions need a token too", "GET", "/subscriptions", "", "", 401},
		{"token endpoint without an issuer", "POST", "/auth/token", "", "grant_type=client_credentials", 501},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := call(h, tc.method, tc.target, tc.token, tc.body)
			if got.status != tc.want {
				t.Fatalf("%s %s = %d %s; want %d", tc.method, tc.target, got.status, got.body, tc.want)
			}
			if got.header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q", got.header.Get("Content-Type"))
			}
			if status, _ := got.json["status"].(float64); int(status) != tc.want || got.json["title"] == "" {
				t.Errorf("problem = %s; want status %d and a title", got.body, tc.want)
			}
			if tc.want == 401 && got.header.Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
			if tc.want == 405 && got.header.Get("Allow") == "" {
				t.Error("405 without Allow")
			}
			if tc.want == 500 && strings.Contains(got.body, "identity provider") {
				t.Errorf("500 leaks the internal error: %s", got.body)
			}
		})
	}
}

func TestMetadataIsTheVTNs(t *testing.T) {
	h := newServer(t, server.Config{})
	// What a client sends as metadata is ignored, on create and on update.
	got := call(h, "POST", "/programs", "bl", `{"id":"mine","objectType":"EVENT","createdDateTime":"2000-01-01T00:00:00Z","programName":"p"}`)
	if got.status != 201 || got.json["id"] == "mine" || got.json["objectType"] != "PROGRAM" || got.json["createdDateTime"] == "2000-01-01T00:00:00Z" {
		t.Fatalf("create = %d %s", got.status, got.body)
	}
	id, created := got.json["id"].(string), got.json["createdDateTime"]
	got = call(h, "PUT", "/programs/"+id, "bl", `{"id":"other","createdDateTime":"2000-01-01T00:00:00Z","programName":"q"}`)
	if got.status != 200 || got.json["id"] != id || got.json["createdDateTime"] != created || got.json["programName"] != "q" {
		t.Errorf("update = %d %s", got.status, got.body)
	}
	if got = call(h, "DELETE", "/programs/"+id, "bl", ""); got.status != 200 || got.json["id"] != id {
		t.Errorf("delete = %d %s; want 200 with the deleted object", got.status, got.body)
	}
}

func TestAuthServerAndTokenEndpoint(t *testing.T) {
	h := newServer(t, server.Config{})
	got := call(h, "GET", "/auth/server", "", "")
	if got.status != 200 || got.json["tokenURL"] != "https://idp.example/token" {
		t.Errorf("GET /auth/server = %d %s", got.status, got.body)
	}

	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{{ID: "c", Secret: "s", Scopes: auth.VENScopes()}}})
	if err != nil {
		t.Fatal(err)
	}
	h = newServer(t, server.Config{TokenHandler: issuer})
	req := httptest.NewRequest("POST", "/auth/token", strings.NewReader("grant_type=client_credentials&client_id=c&client_secret=s"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"token_type":"Bearer"`) {
		t.Errorf("POST /auth/token = %d %s", rec.Code, rec.Body)
	}
}

func TestEmptySearchIsAnEmptyArray(t *testing.T) {
	h := newServer(t, server.Config{})
	for _, path := range []string{"/programs", "/events", "/reports", "/vens", "/resources", "/programs?skip=10", "/programs?limit=0"} {
		if got := call(h, "GET", path, "ven", ""); got.status != 200 || strings.TrimSpace(got.body) != "[]" {
			t.Errorf("GET %s = %d %q; want 200 []", path, got.status, got.body)
		}
	}
}
