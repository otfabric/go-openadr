// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// thief records what reaches it.
type thief struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newThief(t *testing.T) *thief {
	t.Helper()
	th := &thief{}
	th.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		th.mu.Lock()
		th.seen = append(th.seen, fmt.Sprintf("%s %s auth=%q body=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), body))
		th.mu.Unlock()
		_, _ = fmt.Fprint(w, `{"access_token":"stolen","token_type":"Bearer"}`)
	}))
	t.Cleanup(th.Close)
	return th
}

func (th *thief) got() []string {
	th.mu.Lock()
	defer th.mu.Unlock()
	return append([]string(nil), th.seen...)
}

// A VTN that redirects elsewhere: to another port of the same host, where
// net/http keeps the Authorization header, and with 307, which sends the
// body of a POST again, the client secret of a token request included.
// Neither the token nor the secret may follow; a redirect within the VTN
// is still followed.
func TestRedirectsStayWithTheVTN(t *testing.T) {
	th := newThief(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/server", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"tokenURL":"auth/token"}`)
	})
	mux.HandleFunc("POST /api/auth/token", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, th.URL+"/token", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /api/programs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, th.URL+"/programs", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/moved/events", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /api/moved/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer static" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `[]`)
	})
	vtn := httptest.NewServer(mux)
	defer vtn.Close()
	ctx := context.Background()

	withSecret, err := NewVEN(Config{BaseURL: vtn.URL + "/api", Credentials: &Credentials{ClientID: "id", ClientSecret: "the-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	var tokenErr *TokenError
	if _, err := withSecret.Programs(ctx, ProgramQuery{}); !errors.As(err, &tokenErr) || tokenErr.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("a token endpoint that redirects elsewhere: %v, want a TokenError of 307", err)
	}

	withToken, err := NewVEN(Config{BaseURL: vtn.URL + "/api", Tokens: StaticToken("static"), HTTPClient: &http.Client{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	var apiErr *openadr.Error
	if _, err := withToken.Programs(ctx, ProgramQuery{}); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("an API that redirects elsewhere: %v, want an openadr.Error of 307", err)
	}
	if got := th.got(); len(got) != 0 {
		t.Errorf("the redirects were followed: %q", got)
	}
	if _, err := withToken.Events(ctx, EventQuery{}); err != nil {
		t.Errorf("a redirect within the VTN: %v", err)
	}
}

// A client given an HTTP client with redirect rules of its own keeps them.
func TestOwnRedirectPolicyIsKept(t *testing.T) {
	th := newThief(t)
	vtn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, th.URL+"/programs", http.StatusFound)
	}))
	defer vtn.Close()
	followed := false
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { followed = true; return nil }}
	ven, err := NewVEN(Config{BaseURL: vtn.URL, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ven.Programs(context.Background(), ProgramQuery{})
	if !followed || len(th.got()) != 1 {
		t.Errorf("the caller's redirect policy was replaced: followed %v, %q", followed, th.got())
	}
}

// An expires_in that no Duration holds keeps the token, rather than
// wrapping into a lifetime in the past that would fetch a token for every
// request.
func TestExpiresInBeyondADuration(t *testing.T) {
	var issued atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		issued.Add(1)
		_, _ = fmt.Fprint(w, `{"access_token":"t","token_type":"Bearer","expires_in":1e300}`)
	})
	mux.HandleFunc("GET /programs", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `[]`) })
	vtn := httptest.NewServer(mux)
	defer vtn.Close()
	ven, err := NewVEN(Config{BaseURL: vtn.URL, Credentials: &Credentials{ClientID: "id", ClientSecret: "s", TokenURL: vtn.URL + "/token"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := ven.Programs(context.Background(), ProgramQuery{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := issued.Load(); n != 1 {
		t.Errorf("%d tokens for 3 calls, want 1", n)
	}
	if got := tokenLifetime(1e300); got != maxTokenLifetime-expirySlack {
		t.Errorf("time.Duration(0) = %v", got)
	}
}

// A VTN that asks for a wait longer than the call has left: the call fails
// at once instead of keeping the caller until its deadline.
func TestRetryAfterBeyondTheDeadline(t *testing.T) {
	for _, after := range []string{"3600", time.Now().Add(240 * time.Hour).UTC().Format(http.TimeFormat)} {
		vtn := newScript(t, 503, 503)
		vtn.header.Set("Retry-After", after)
		bl := newBL(t, vtn, Config{RetryPolicy: fast})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		start := time.Now()
		_, err := bl.Programs(ctx, ProgramQuery{})
		cancel()
		if !errors.Is(err, openadr.ErrUnavailable) || vtn.requests() != 1 {
			t.Errorf("Retry-After %q: %v after %d requests", after, err, vtn.requests())
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("Retry-After %q: the call took %v, waiting for a retry it could not make", after, took)
		}
	}
}

// A body without end, and one that breaks off: the first is cut at
// MaxResponseBytes, the second is not taken for an answer.
func TestHostileResponseBodies(t *testing.T) {
	mux := http.NewServeMux()
	chunk := []byte("[" + strings.Repeat(" ", 4095))
	mux.HandleFunc("GET /programs", func(w http.ResponseWriter, r *http.Request) {
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `[{"id":"e1","objectType":"EV`)
	})
	vtn := httptest.NewServer(mux)
	defer vtn.Close()
	ven, err := NewVEN(Config{BaseURL: vtn.URL, MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ven.Programs(ctx, ProgramQuery{}); !errors.Is(err, openadr.ErrProtocol) {
		t.Errorf("an endless body: %v, want ErrProtocol", err)
	}
	if _, err := ven.Events(ctx, EventQuery{}); !errors.Is(err, openadr.ErrProtocol) {
		t.Errorf("truncated JSON: %v, want ErrProtocol", err)
	}
}

// A VTN that redirects to itself for ever is left after ten redirects.
func TestRedirectLoop(t *testing.T) {
	var hops atomic.Int64
	vtn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, fmt.Sprintf("/api/programs?hop=%d", hops.Load()), http.StatusFound)
	}))
	defer vtn.Close()
	ven, err := NewVEN(Config{BaseURL: vtn.URL + "/api", Tokens: StaticToken("static")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ven.Programs(context.Background(), ProgramQuery{}); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("a redirect loop: %v", err)
	}
	if n := hops.Load(); n != 10 {
		t.Errorf("%d requests, want 10: the first and nine redirects", n)
	}
}
