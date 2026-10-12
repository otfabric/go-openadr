// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

// fakeVTN counts token requests and accepts only the latest token.
type fakeVTN struct {
	*httptest.Server
	issued   atomic.Int64
	expires  atomic.Int64 // expires_in of issued tokens; 0 omits it
	lastForm atomic.Value
	requests atomic.Int64
}

func newFakeVTN(t *testing.T) *fakeVTN {
	f := &fakeVTN{}
	f.expires.Store(3600)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/server", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"tokenURL":"auth/token"}`)
	})
	mux.HandleFunc("POST /api/auth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastForm.Store(r.PostForm)
		if r.PostForm.Get("client_secret") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":"invalid_client","error_description":"no"}`)
			return
		}
		resp := map[string]any{"access_token": fmt.Sprintf("token-%d", f.issued.Add(1)), "token_type": "bearer"}
		if e := f.expires.Load(); e != 0 {
			resp["expires_in"] = e
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /api/programs", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", f.issued.Load()) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"title":"Unauthorized","status":401,"detail":"stale token"}`)
			return
		}
		_, _ = fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `<html>bad gateway</html>`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeVTN) ven(t *testing.T, creds Credentials) *VEN {
	t.Helper()
	v, err := NewVEN(Config{BaseURL: f.URL + "/api", Credentials: &creds, UserAgent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTokenIsCachedAndRefreshed(t *testing.T) {
	ctx := context.Background()
	f := newFakeVTN(t)
	ven := f.ven(t, Credentials{ClientID: "id", ClientSecret: "secret", Scopes: []string{"read_targets", "write_reports"}})

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ven.Programs(ctx, ProgramQuery{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.issued.Load(); n != 1 {
		t.Errorf("%d tokens requested for 20 concurrent calls, want 1", n)
	}
	if scope := f.lastForm.Load().(interface{ Get(string) string }).Get("scope"); scope != "read_targets write_reports" {
		t.Errorf("scope sent = %q", scope)
	}

	// The VTN stops accepting the token: one retry with a fresh one.
	f.issued.Add(1)
	before := f.requests.Load()
	if _, err := ven.Programs(ctx, ProgramQuery{}); err != nil {
		t.Fatalf("call after the token was revoked: %v", err)
	}
	if got := f.requests.Load() - before; got != 2 {
		t.Errorf("%d requests, want the rejected one and one retry", got)
	}
	if n := f.issued.Load(); n != 3 {
		t.Errorf("issued = %d, want 3", n)
	}

	// A token about to expire is not used.
	f.expires.Store(5)
	f.issued.Add(1)
	if _, err := ven.Programs(ctx, ProgramQuery{}); err != nil {
		t.Fatal(err)
	}
	n := f.issued.Load()
	if _, err := ven.Programs(ctx, ProgramQuery{}); err != nil {
		t.Fatal(err)
	}
	if f.issued.Load() != n+1 {
		t.Errorf("a token within %v of its expiry was reused", expirySlack)
	}
}

func TestStaticTokenIsNotRetried(t *testing.T) {
	f := newFakeVTN(t)
	ven, err := NewVEN(Config{BaseURL: f.URL + "/api/", Tokens: StaticToken("wrong")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ven.Programs(context.Background(), ProgramQuery{})
	var apiErr *openadr.Error
	if !errors.Is(err, openadr.ErrUnauthorized) || !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if apiErr.Problem.Detail != "stale token" || apiErr.Method != "GET" || apiErr.Path != "/programs" {
		t.Errorf("error = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "401 Unauthorized: stale token") {
		t.Errorf("message = %q", err)
	}
	if f.requests.Load() != 1 {
		t.Errorf("%d requests, want 1", f.requests.Load())
	}
}

func TestTokenAndResponseErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeVTN(t)

	_, err := f.ven(t, Credentials{ClientID: "id", ClientSecret: "wrong"}).Programs(ctx, ProgramQuery{})
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) || tokenErr.StatusCode != 401 || tokenErr.Code != "invalid_client" {
		t.Errorf("wrong secret: %v", err)
	}

	// An error without a problem body still reports its status.
	_, err = f.ven(t, Credentials{ClientID: "id", ClientSecret: "secret"}).Events(ctx, EventQuery{})
	var apiErr *openadr.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway || apiErr.Problem != (openadr.Problem{}) {
		t.Errorf("502 with an HTML body: %v", err)
	}

	// Nothing is sent for an object or ID that is invalid.
	before := f.issued.Load()
	ven := f.ven(t, Credentials{ClientID: "id", ClientSecret: "secret"})
	var invalid *openadr.ValidationError
	if _, err := ven.CreateReport(ctx, openadr.Report{}); !errors.As(err, &invalid) || invalid.Field != "eventID" {
		t.Errorf("CreateReport of an empty report: %v", err)
	}
	if _, err := ven.Program(ctx, "../vens"); !errors.As(err, &invalid) {
		t.Errorf("Program with a path as ID: %v", err)
	}
	if _, err := ven.UpdateReport(ctx, openadr.Report{EventID: "e", ClientName: "c"}); !errors.As(err, &invalid) || invalid.Field != "id" {
		t.Errorf("UpdateReport without an ID: %v", err)
	}
	if f.issued.Load() != before {
		t.Error("an invalid call reached the network")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ven.Programs(canceled, ProgramQuery{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context: %v", err)
	}
}

func TestConfig(t *testing.T) {
	creds := &Credentials{ClientID: "id", ClientSecret: "secret"}
	for name, cfg := range map[string]Config{
		"no base URL":           {},
		"relative base URL":     {BaseURL: "/api"},
		"other scheme":          {BaseURL: "ftp://vtn.example"},
		"credentials and token": {BaseURL: "https://vtn.example", Credentials: creds, Tokens: StaticToken("t")},
		"no secret":             {BaseURL: "https://vtn.example", Credentials: &Credentials{ClientID: "id"}},
		"relative token URL":    {BaseURL: "https://vtn.example", Credentials: &Credentials{ClientID: "id", ClientSecret: "s", TokenURL: "auth/token"}},
	} {
		if _, err := NewVEN(cfg); err == nil {
			t.Errorf("%s: NewVEN succeeded", name)
		}
		if _, err := NewBL(cfg); err == nil {
			t.Errorf("%s: NewBL succeeded", name)
		}
	}
}

// A VTN reached over TLS must not be able to redirect the client secret to
// a cleartext token endpoint.
func TestDiscoveryRefusesDowngrade(t *testing.T) {
	var tokenRequests atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { tokenRequests.Add(1) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"tokenURL":%q}`, plain.URL+"/token")
	}))
	defer secure.Close()

	ven, err := NewVEN(Config{BaseURL: secure.URL, HTTPClient: secure.Client(), Credentials: &Credentials{ClientID: "id", ClientSecret: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ven.Programs(context.Background(), ProgramQuery{}); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Errorf("err = %v, want a refusal", err)
	}
	if tokenRequests.Load() != 0 {
		t.Error("the client secret was sent to the cleartext endpoint")
	}
}
