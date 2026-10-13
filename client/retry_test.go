// SPDX-License-Identifier: MIT

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// script is a VTN that answers each request with the next of a list of
// statuses, and with 200 when the list is used up. A status of 0 hangs up
// without an answer, and -1 does not answer for as long as the client waits.
type script struct {
	*httptest.Server
	mu       sync.Mutex
	statuses []int
	// header goes with every answer that is not a success.
	header http.Header
	seen   []string
}

func newScript(t *testing.T, statuses ...int) *script {
	t.Helper()
	s := &script{statuses: statuses, header: http.Header{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		status := http.StatusOK
		if len(s.statuses) > 0 {
			status, s.statuses = s.statuses[0], s.statuses[1:]
		}
		s.mu.Unlock()
		switch status {
		case 0:
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		case -1:
			<-r.Context().Done()
			return
		case http.StatusOK:
			switch {
			case r.Method == http.MethodGet && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/"), "/"):
				_, _ = fmt.Fprint(w, `[]`)
			default:
				_, _ = fmt.Fprint(w, `{"id":"p1","objectType":"PROGRAM","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","programName":"n"}`)
			}
			return
		}
		for k, v := range s.header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"title":%q,"status":%d}`, http.StatusText(status), status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *script) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// fast is a policy for tests: four attempts, a millisecond apart.
var fast = NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond})

func newBL(t *testing.T, s *script, cfg Config) *BL {
	t.Helper()
	cfg.BaseURL = s.URL
	bl, err := NewBL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return bl
}

func TestRetry(t *testing.T) {
	ctx := context.Background()
	program := openadr.Program{Metadata: openadr.Metadata{ID: "p1"}, Name: "n"}
	calls := map[string]func(*BL) error{
		"GET":    func(bl *BL) error { _, err := bl.Programs(ctx, ProgramQuery{}); return err },
		"POST":   func(bl *BL) error { _, err := bl.CreateProgram(ctx, openadr.Program{Name: "n"}); return err },
		"PUT":    func(bl *BL) error { _, err := bl.UpdateProgram(ctx, program); return err },
		"DELETE": func(bl *BL) error { _, err := bl.DeleteProgram(ctx, "p1"); return err },
	}
	tests := []struct {
		name     string
		method   string
		statuses []int
		creates  bool
		// requests is how many the VTN is to see, and status what the call
		// ends with: 200 for success, 0 for a failure without an answer.
		requests, status int
	}{
		{"a read comes back after 503", "GET", []int{503, 503}, false, 3, 200},
		{"and after 429, 502 and 504", "GET", []int{429, 502, 504}, false, 4, 200},
		{"and after a connection that was lost", "GET", []int{0, 0}, false, 3, 200},
		{"but gives up when its attempts are used up", "GET", []int{503, 503, 503, 503, 503}, false, 4, 503},
		{"a 500 is not retried: nothing says another attempt would help", "GET", []int{500}, false, 1, 500},
		{"nor a 404, a 409 or a 400", "GET", []int{404}, false, 1, 404},
		{"an update is repeated whatever became of it", "PUT", []int{0, 502}, false, 3, 200},
		{"and a delete", "DELETE", []int{504, 0}, false, 3, 200},
		{"a create is repeated when it was refused before anything was done", "POST", []int{503, 429}, false, 3, 200},
		{"but not when its fate is not known: a lost connection", "POST", []int{0}, false, 1, 0},
		{"or a gateway that got no answer", "POST", []int{502}, false, 1, 502},
		{"unless the caller takes that on", "POST", []int{0, 502, 504}, true, 4, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtn := newScript(t, tt.statuses...)
			err := calls[tt.method](newBL(t, vtn, Config{RetryPolicy: fast, RetryCreates: tt.creates}))
			if got := vtn.requests(); got != tt.requests {
				t.Errorf("the VTN saw %d requests, want %d", got, tt.requests)
			}
			var apiErr *openadr.Error
			switch tt.status {
			case 200:
				if err != nil {
					t.Errorf("the call failed: %v", err)
				}
			case 0:
				if err == nil || errors.As(err, &apiErr) {
					t.Errorf("got %v, want a failure of the transport", err)
				}
			default:
				if !errors.As(err, &apiErr) || apiErr.StatusCode != tt.status {
					t.Errorf("got %v, want a %d", err, tt.status)
				}
			}
		})
	}

	t.Run("without a policy nothing is retried", func(t *testing.T) {
		vtn := newScript(t, 503)
		if err := calls["GET"](newBL(t, vtn, Config{})); !errors.Is(err, openadr.ErrUnavailable) || vtn.requests() != 1 {
			t.Errorf("got %v after %d requests", err, vtn.requests())
		}
	})

	t.Run("a create to a VTN that is not there is repeated: it was never sent", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		counter := &attempts{}
		bl, err := NewBL(Config{BaseURL: "http://" + addr, RetryPolicy: fast, Metrics: counter})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bl.CreateProgram(ctx, openadr.Program{Name: "n"}); err == nil {
			t.Fatal("a create to nowhere succeeded")
		}
		if counter.attempts.Load() != 4 {
			t.Errorf("%d attempts, want 4", counter.attempts.Load())
		}
	})
}

func TestRetryAfter(t *testing.T) {
	vtn := newScript(t, 429, 503)
	vtn.header.Set("Retry-After", "1")
	bl := newBL(t, vtn, Config{})
	_, err := bl.Programs(context.Background(), ProgramQuery{})
	var apiErr *openadr.Error
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != time.Second || !errors.Is(err, openadr.ErrTooManyRequests) {
		t.Fatalf("got %v (%+v), want a 429 that says to wait a second", err, apiErr)
	}
	// With a policy, the VTN's word counts for more than the policy's.
	vtn = newScript(t, 503)
	vtn.header.Set("Retry-After", "1")
	bl = newBL(t, vtn, Config{RetryPolicy: fast})
	start := time.Now()
	if _, err := bl.Programs(context.Background(), ProgramQuery{}); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 900*time.Millisecond || waited > 5*time.Second {
		t.Errorf("waited %v before the second attempt, want the second the VTN asked for", waited)
	}

	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"": 0, "120": 2 * time.Minute, "0": 0, "-5": 0, "soon": 0, "1.5": 1500 * time.Millisecond,
		"Tue, 01 Jan 2030 12:00:30 GMT": 30 * time.Second, "Tue, 01 Jan 2030 11:00:00 GMT": 0,
	} {
		if got := parseRetryAfter(http.Header{"Retry-After": {value}}, now); got != want {
			t.Errorf("Retry-After %q: %v, want %v", value, got, want)
		}
	}
}

func TestRetryStopsWithTheContext(t *testing.T) {
	vtn := newScript(t, 503, 503, 503, 503)
	bl := newBL(t, vtn, Config{RetryPolicy: ExponentialBackoff(time.Hour, time.Hour, 10)})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := bl.Programs(ctx, ProgramQuery{})
	// The caller is told what went wrong with the call, not that the wait
	// for another attempt was cut short.
	if !errors.Is(err, openadr.ErrUnavailable) || vtn.requests() != 1 {
		t.Errorf("got %v after %d requests", err, vtn.requests())
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the wait for another attempt outlived the context")
	}
	// A context that is already done sends nothing more.
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := bl.Programs(cancelled, ProgramQuery{}); !errors.Is(err, context.Canceled) || vtn.requests() != 2 && vtn.requests() != 1 {
		t.Errorf("with a cancelled context: %v after %d requests", err, vtn.requests())
	}
}

func TestTimeouts(t *testing.T) {
	vtn := newScript(t, -1, -1, -1)
	// The deadline of the context.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := newBL(t, vtn, Config{RetryPolicy: fast}).Programs(ctx, ProgramQuery{})
	if !errors.Is(err, openadr.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a context that ran out: %v", err)
	}
	if vtn.requests() != 1 {
		t.Errorf("a timeout was retried by a policy that does not: %d requests", vtn.requests())
	}
	// The timeout of the HTTP client, retried by a policy that says so.
	vtn = newScript(t, -1, -1)
	patient := NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: time.Millisecond, RetryOnTimeout: true})
	bl := newBL(t, vtn, Config{HTTPClient: &http.Client{Timeout: 30 * time.Millisecond}, RetryPolicy: patient})
	if _, err := bl.Programs(context.Background(), ProgramQuery{}); err != nil || vtn.requests() != 3 {
		t.Errorf("after two timeouts: %v, %d requests", err, vtn.requests())
	}
	vtn = newScript(t, -1)
	_, err = newBL(t, vtn, Config{HTTPClient: &http.Client{Timeout: 30 * time.Millisecond}}).Programs(context.Background(), ProgramQuery{})
	if !errors.Is(err, openadr.ErrTimeout) {
		t.Errorf("a client that timed out: %v", err)
	}
}

func TestExponentialBackoff(t *testing.T) {
	p := NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second, MaxAttempts: 7, NoJitter: true})
	var waits []time.Duration
	for attempt := 1; ; attempt++ {
		again, wait := p.ShouldRetry(attempt, errors.New("x"))
		if !again {
			break
		}
		waits = append(waits, wait)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second, time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Errorf("waits %v, want %v", waits, want)
	}
	// With jitter: between half and all of it.
	jittered := ExponentialBackoff(100*time.Millisecond, time.Second, 0)
	for range 200 {
		if _, wait := jittered.ShouldRetry(2, errors.New("x")); wait < 100*time.Millisecond || wait > 200*time.Millisecond {
			t.Fatalf("a jittered wait of %v for a delay of 200 ms", wait)
		}
	}
	// The defaults: four attempts.
	if again, _ := jittered.ShouldRetry(3, errors.New("x")); !again {
		t.Error("the third attempt was the last")
	}
	if again, _ := jittered.ShouldRetry(4, errors.New("x")); again {
		t.Error("a fifth attempt")
	}
	// Without a limit, and huge delays that do not overflow.
	endless := NewExponentialBackoff(ExponentialBackoffConfig{MaxAttempts: -1, NoJitter: true})
	if again, wait := endless.ShouldRetry(1000, errors.New("x")); !again || wait != 30*time.Second {
		t.Errorf("attempt 1000: %v, %v", again, wait)
	}
	if again, _ := NoRetry().ShouldRetry(1, errors.New("x")); again {
		t.Error("NoRetry retries")
	}
}

// attempts counts what a client reports.
type attempts struct {
	mu       sync.Mutex
	events   []string
	attempts atomic.Int64
}

func (a *attempts) add(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, fmt.Sprintf(format, args...))
}

func (a *attempts) OnRequest(op string)                   { a.add("request %s", op) }
func (a *attempts) OnResponse(op string, _ time.Duration) { a.add("response %s", op) }
func (a *attempts) OnTimeout(op string, _ time.Duration)  { a.add("timeout %s", op) }
func (a *attempts) OnError(op string, _ time.Duration, err error) {
	a.add("error %s %d", op, statusOf(err))
}
func (a *attempts) OnAttempt(op string, attempt int, _ time.Duration, err error) {
	a.attempts.Add(1)
	a.add("attempt %d %s %d", attempt, op, statusOf(err))
}

func statusOf(err error) int {
	var apiErr *openadr.Error
	switch {
	case err == nil:
		return 200
	case errors.As(err, &apiErr):
		return apiErr.StatusCode
	}
	return 0
}

func TestMetricsAndLog(t *testing.T) {
	ctx := context.Background()
	vtn := newScript(t, 503, 200, 404, -1)
	m := &attempts{}
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bl := newBL(t, vtn, Config{RetryPolicy: fast, Metrics: m, Logger: logger})

	if _, err := bl.Programs(ctx, ProgramQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := bl.Program(ctx, "secret-id-1"); !errors.Is(err, openadr.ErrNotFound) {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := bl.DeleteEvent(short, "secret-id-2"); !errors.Is(err, openadr.ErrTimeout) {
		t.Fatal(err)
	}
	// Refused before anything is sent: not a call.
	if _, err := bl.Program(ctx, "not an id"); !errors.Is(err, openadr.ErrInvalid) {
		t.Fatal(err)
	}

	want := []string{
		"request GET /programs", "attempt 1 GET /programs 503", "attempt 2 GET /programs 200", "response GET /programs",
		"request GET /programs/{id}", "attempt 1 GET /programs/{id} 404", "error GET /programs/{id} 404",
		"request DELETE /events/{id}", "attempt 1 DELETE /events/{id} 0", "timeout DELETE /events/{id}",
	}
	if fmt.Sprint(m.events) != fmt.Sprint(want) {
		t.Errorf("reported\n  %s\nwant\n  %s", strings.Join(m.events, "\n  "), strings.Join(want, "\n  "))
	}

	out := log.String()
	for _, line := range []string{
		`level=WARN msg="openadr: retrying" op="GET /programs" attempt=1`,
		`level=DEBUG msg="openadr: call" op="GET /programs" duration=`,
		`level=DEBUG msg="openadr: call" op="GET /programs/{id}"`,
		`op="DELETE /events/{id}"`,
	} {
		if !strings.Contains(out, line) {
			t.Errorf("the log lacks %q:\n%s", line, out)
		}
	}
	// Operations, not paths: an ID is not for a label, and need not be in
	// a log line of every call. (An error names its path, as it always did.)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "secret-id") && !strings.Contains(line, "error=") {
			t.Errorf("an ID in a log line that reports no error: %s", line)
		}
	}

	// The names of the operations.
	for call, want := range map[[2]string]string{
		{"GET", "programs"}: "GET /programs", {"PUT", "events/abc"}: "PUT /events/{id}",
		{"GET", "notifiers/mqtt/topics/vens/v1/events"}: "GET /notifiers/mqtt/topics/vens/{id}/events",
		{"GET", "notifiers/mqtt/topics/programs/p1"}:    "GET /notifiers/mqtt/topics/programs/{id}",
		{"DELETE", "resource_groups/g1"}:                "DELETE /resource_groups/{id}",
		{"GET", "notifiers"}:                            "GET /notifiers",
		// An ID that reads like a word of the API is still an ID where
		// one stands... unless it is one of those words: the price of
		// naming operations without a table of them.
		{"GET", "vens/events"}: "GET /vens/events",
	} {
		if got := opName(call[0], call[1]); got != want {
			t.Errorf("opName(%s, %s) = %q, want %q", call[0], call[1], got, want)
		}
	}
}

// A token that cannot be had is a failure before anything is sent: it can
// be retried, whatever the call.
func TestRetryOfTheTokenRequest(t *testing.T) {
	var tokenRequests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		if tokenRequests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"access_token":"t","token_type":"Bearer","expires_in":3600}`)
	})
	created := 0
	mux.HandleFunc("POST /programs", func(w http.ResponseWriter, _ *http.Request) {
		created++
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"p1","objectType":"PROGRAM","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","programName":"n"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	credentials := &Credentials{ClientID: "c", ClientSecret: "s", TokenURL: ts.URL + "/token"}

	bl, err := NewBL(Config{BaseURL: ts.URL, Credentials: credentials})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bl.CreateProgram(context.Background(), openadr.Program{Name: "n"})
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) || tokenErr.StatusCode != 503 || !errors.Is(err, openadr.ErrServer) || created != 0 {
		t.Fatalf("without a policy: %v, %d programs", err, created)
	}
	bl, err = NewBL(Config{BaseURL: ts.URL, Credentials: credentials, RetryPolicy: fast})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bl.CreateProgram(context.Background(), openadr.Program{Name: "n"}); err != nil || created != 1 {
		t.Errorf("with a policy: %v, %d programs", err, created)
	}
}
