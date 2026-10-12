// SPDX-License-Identifier: MIT

package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

func closeSender(t *testing.T, s *Sender) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34":        true,
		"2606:2800:220:1::1":   true,
		"127.0.0.1":            false,
		"::1":                  false,
		"10.1.2.3":             false,
		"172.16.0.1":           false,
		"192.168.1.1":          false,
		"169.254.169.254":      false, // cloud metadata
		"100.64.0.1":           false,
		"0.0.0.0":              false,
		"224.0.0.1":            false,
		"fc00::1":              false,
		"fe80::1":              false,
		"::ffff:127.0.0.1":     false,
		"::ffff:10.0.0.1":      false,
		"::ffff:93.184.216.34": true,
	} {
		if got := public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestVerify(t *testing.T) {
	echo := &Handler{}
	var query atomic.Value
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		echo.ServeHTTP(w, r)
	}))
	defer good.Close()
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "something else") }))
	defer wrong.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, good.URL+"/?"+r.URL.RawQuery, http.StatusFound)
	}))
	defer redirect.Close()
	secure := httptest.NewTLSServer(echo)
	defer secure.Close()

	lenient := NewSender(SenderConfig{AllowInsecure: true})
	defer closeSender(t, lenient)
	ctx := context.Background()

	if err := lenient.Verify(ctx, good.URL+"/hook?tenant=7"); err != nil {
		t.Errorf("a webhook that echoes: %v", err)
	}
	if q := query.Load().(string); !strings.Contains(q, "tenant=7") || !strings.Contains(q, "echo=") {
		t.Errorf("verification request had the query %q; the URL's own parameters must be kept", q)
	}
	for name, tc := range map[string]struct{ url, want string }{
		"wrong echo":      {wrong.URL, "did not answer"},
		"redirect":        {redirect.URL, "answered the verification request with 302"},
		"nobody there":    {"http://127.0.0.1:1/", "could not be reached"},
		"no host":         {"https:///hook", "not an absolute URL"},
		"relative":        {"/hook", "not an absolute URL"},
		"other scheme":    {"file:///etc/passwd", "not an absolute URL"},
		"gopher":          {"gopher://example.com/", "must use https"},
		"credentials":     {"https://user:pw@example.com/", "must not carry credentials"},
		"untrusted https": {secure.URL, "could not be reached"},
	} {
		err := lenient.Verify(ctx, tc.url)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, tc.want)
		}
		// What the client is told must not describe the VTN's network.
		if err != nil && (strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "connection refused")) {
			t.Errorf("%s: the error tells the client about the VTN's network: %v", name, err)
		}
	}

	// The default: https only, and no address inside the VTN's own network,
	// however the URL names it.
	strict := NewSender(SenderConfig{})
	defer closeSender(t, strict)
	if err := strict.Verify(ctx, good.URL); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Errorf("http with the default configuration: %v", err)
	}
	for _, url := range []string{secure.URL, "https://localhost:1/", "https://[::1]:1/", "https://169.254.169.254/latest/meta-data"} {
		if err := strict.Verify(ctx, url); err == nil || !strings.Contains(err.Error(), "could not be reached") {
			t.Errorf("%s with the default configuration: %v, want it refused", url, err)
		}
	}
	// With a client that trusts the test server, https on loopback is still
	// refused by the default client, and accepted by one that is given.
	trusting := NewSender(SenderConfig{HTTPClient: secure.Client()})
	defer closeSender(t, trusting)
	if err := trusting.Verify(ctx, secure.URL); err != nil {
		t.Errorf("https with a client of the caller's: %v", err)
	}
}

func TestDelivery(t *testing.T) {
	var (
		mu       sync.Mutex
		bodies   []string
		auths    []string
		failures = map[string]int{"flaky": 2, "down": 99}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/")
		if key == "gone" {
			w.WriteHeader(http.StatusGone)
			bodies = append(bodies, "gone:"+string(body))
			return
		}
		if failures[key] > 0 {
			failures[key]--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		bodies = append(bodies, key+":"+string(body))
		auths = append(auths, r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	var logged bytes.Buffer
	s := NewSender(SenderConfig{
		AllowInsecure: true, Attempts: 3, Backoff: 5 * time.Millisecond, Workers: 3,
		Logger: slog.New(slog.NewTextHandler(&logged, nil)),
	})
	// In order per callback, also across a retry.
	for i := range 20 {
		s.Send(srv.URL+"/steady", "tok", fmt.Appendf(nil, "%02d", i))
	}
	s.Send(srv.URL+"/flaky", "", []byte("a"))
	s.Send(srv.URL+"/flaky", "", []byte("b"))
	// Given up on: after the attempts, and at once for a refusal.
	s.Send(srv.URL+"/down?secret=in-the-query", "", []byte("x"))
	s.Send(srv.URL+"/gone", "", []byte("y"))
	s.Send("ftp://example.com/", "", []byte("z"))
	closeSender(t, s)
	// After Close nothing is sent, and nothing blocks.
	s.Send(srv.URL+"/steady", "", []byte("late"))

	mu.Lock()
	defer mu.Unlock()
	var steady, flaky []string
	gone := 0
	for _, b := range bodies {
		key, body, _ := strings.Cut(b, ":")
		switch key {
		case "steady":
			steady = append(steady, body)
		case "flaky":
			flaky = append(flaky, body)
		case "gone":
			gone++
		default:
			t.Errorf("unexpected delivery %q", b)
		}
	}
	if len(steady) != 20 {
		t.Fatalf("%d of 20 notifications delivered", len(steady))
	}
	for i, b := range steady {
		if b != fmt.Sprintf("%02d", i) {
			t.Fatalf("notifications of one callback out of order: %v", steady)
		}
	}
	if strings.Join(flaky, "") != "ab" {
		t.Errorf("deliveries to a webhook that failed twice = %v, want a then b", flaky)
	}
	if gone != 1 {
		t.Errorf("a 410 was retried: %d requests", gone)
	}
	if failures["down"] != 99-3 {
		t.Errorf("a webhook that is down got %d requests, want 3", 99-failures["down"])
	}
	if !contains(auths, "Bearer tok") || !contains(auths, "") {
		t.Errorf("Authorization headers = %v", auths[:3])
	}
	log := logged.String()
	if strings.Count(log, "not delivered") != 3 || strings.Contains(log, "in-the-query") {
		t.Errorf("log of failed deliveries (3 expected, without the query of the URL):\n%s", log)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestSendDoesNotBlockAndCloseGivesUp(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	defer close(release)

	s := NewSender(SenderConfig{AllowInsecure: true, Queue: 2, Workers: 1, Timeout: 200 * time.Millisecond, Backoff: time.Hour})
	done := make(chan struct{})
	go func() {
		// Far more than the queue holds: the rest is dropped, not waited for.
		for range 100 {
			s.Send(srv.URL, "", []byte("n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on a full queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close with undeliverable notifications: %v, want the context's error", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Close took %v: it waited out a backoff", time.Since(start))
	}
}

func TestHandler(t *testing.T) {
	var got []openadr.Notification
	fail := false
	h := &Handler{BearerToken: "s3cret", MaxBodyBytes: 512, Notify: func(_ context.Context, n openadr.Notification) error {
		if fail {
			return errors.New("database is down")
		}
		got = append(got, n)
		return nil
	}}
	call := func(method, target, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const event = `{"objectType":"EVENT","operation":"UPDATE","targets":["group-a"],"object":{"id":"e1","objectType":"EVENT",
		"createdDateTime":"2026-01-01T00:00:00Z","modificationDateTime":"2026-01-01T00:00:00Z","programID":"p1","eventName":"prices"}}`

	// The verification request needs no token: it proves nothing but presence.
	if rec := call("GET", "/hook?echo=abc123", "", ""); rec.Code != 200 || rec.Body.String() != "abc123" {
		t.Errorf("echo = %d %q", rec.Code, rec.Body)
	}
	if rec := call("GET", "/hook", "", ""); rec.Code != 400 {
		t.Errorf("GET without echo = %d", rec.Code)
	}
	if rec := call("POST", "/hook", "Bearer s3cret", event); rec.Code != 200 {
		t.Fatalf("notification = %d %s", rec.Code, rec.Body)
	}
	if len(got) != 1 || got[0].Operation != openadr.OperationUpdate || got[0].ObjectType != openadr.ObjectEvent ||
		got[0].Object.(openadr.Event).Name != "prices" || got[0].Targets[0] != "group-a" {
		t.Errorf("decoded notification = %+v", got)
	}
	for name, tc := range map[string]struct {
		method, auth, body string
		want               int
	}{
		"no token":            {"POST", "", event, 401},
		"wrong token":         {"POST", "Bearer nope", event, 401},
		"other scheme":        {"POST", "Basic s3cret", event, 401},
		"not JSON":            {"POST", "Bearer s3cret", "hello", 400},
		"no object":           {"POST", "Bearer s3cret", `{"objectType":"EVENT","operation":"CREATE"}`, 400},
		"unknown kind":        {"POST", "Bearer s3cret", `{"objectType":"TARIFF","operation":"CREATE","object":{}}`, 400},
		"object not an event": {"POST", "Bearer s3cret", `{"objectType":"EVENT","operation":"CREATE","object":{"programID":7}}`, 400},
		"too large":           {"POST", "Bearer s3cret", `{"objectType":"EVENT","x":"` + strings.Repeat("a", 600) + `"}`, 413},
		"other method":        {"PUT", "Bearer s3cret", event, 405},
	} {
		if rec := call(tc.method, "/hook", tc.auth, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, strings.TrimSpace(rec.Body.String()), tc.want)
		}
	}
	if len(got) != 1 {
		t.Errorf("a refused request reached Notify: %d notifications", len(got))
	}
	// A failure of the application asks the VTN to send it again, and says
	// nothing about why.
	fail = true
	if rec := call("POST", "/hook", "Bearer s3cret", event); rec.Code != 500 || strings.Contains(rec.Body.String(), "database") {
		t.Errorf("Notify failed: %d %q", rec.Code, rec.Body)
	}
	// Without a token configured, anyone is accepted.
	open := &Handler{Notify: func(context.Context, openadr.Notification) error { return nil }}
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(event)))
	if rec.Code != 200 {
		t.Errorf("handler without a token = %d", rec.Code)
	}
}
