// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
)

const (
	// The secrets of TestHandlerLogger, which no entry may hold.
	logToken  = "subscription-token-7f3a"
	logWrong  = "wrong-token-91c2"
	logMarker = "body-marker-5d1e"
	logQuery  = "query-secret-44b0"

	logEvent = `{"objectType":"EVENT","operation":"CREATE","object":{"id":"e1","objectType":"EVENT",
		"createdDateTime":"2026-01-01T00:00:00Z","modificationDateTime":"2026-01-01T00:00:00Z","programID":"p1","eventName":"NAME"}}`
)

// logRequest is a request of TestHandlerLogger and what it is answered
// and logged with: no entry when message is empty.
type logRequest struct {
	name, method, token, body string
	handler                   *Handler // when not the test's own
	status                    int
	level                     slog.Level
	message, reason           string
	attrs                     map[string]string
}

// The Handler logs what it refuses at warn level and the notifications it
// answers 500 at error level, with a fixed message and reason; what the
// request chose is cut and quoted, and no entry holds a token, the body,
// the path or the query.
func TestHandlerLogger(t *testing.T) {
	rec := guardtest.NewHandler()
	h := &Handler{BearerToken: logToken, MaxBodyBytes: 4096, Logger: rec.Logger(), Notify: func(_ context.Context, n openadr.Notification) error {
		switch n.Object.(openadr.Event).Name {
		case "fail":
			return errors.New("cannot store\nthe event")
		case "stop":
			guardtest.Stop()
		}
		return nil
	}}
	var errorLog lockedBuffer
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(&errorLog, "", 0)
	srv.Start()
	defer srv.Close()
	url := srv.URL + "/hooks/site-42?key=" + logQuery

	long := strings.Repeat("X", 300)
	refused, failed := msgRefused, msgFailed
	event := func(name string) string { return strings.Replace(logEvent, "NAME", name, 1) }
	for _, tc := range []logRequest{
		{name: "verification", method: http.MethodGet, status: 200},
		{name: "verification without echo", method: http.MethodGet, status: 400, level: slog.LevelWarn, message: refused,
			reason: "echo parameter missing or longer than 256 bytes", attrs: map[string]string{"method": `"GET"`}},
		{name: "another method", method: http.MethodPut, status: 405, level: slog.LevelWarn, message: refused,
			reason: "method not allowed", attrs: map[string]string{"method": `"PUT"`}},
		{name: "a long method", method: long, status: 405, level: slog.LevelWarn, message: refused,
			reason: "method not allowed", attrs: map[string]string{"method": strconv.Quote(long[:256]) + "... (300 bytes)"}},
		{name: "no token", method: http.MethodPost, body: event("prices"), status: 401, level: slog.LevelWarn, message: refused,
			reason: "bearer token missing or wrong"},
		{name: "a wrong token", method: http.MethodPost, token: logWrong, body: event("prices"), status: 401, level: slog.LevelWarn, message: refused,
			reason: "bearer token missing or wrong"},
		{name: "too long", method: http.MethodPost, token: logToken, body: strings.Repeat(logMarker, 400), status: 413, level: slog.LevelWarn, message: refused,
			reason: "body longer than MaxBodyBytes"},
		{name: "not a notification", method: http.MethodPost, token: logToken, body: `{"objectType":"EVENT","operation":"CREATE","object":["` + logMarker + `"]}`,
			status: 400, level: slog.LevelWarn, message: refused, reason: "not a notification"},
		{name: "Notify fails", method: http.MethodPost, token: logToken, body: event("fail"), status: 500, level: slog.LevelError, message: failed,
			reason: "Notify returned an error", attrs: map[string]string{"error": `"cannot store\nthe event"`}},
		{name: "no Notify", handler: &Handler{Logger: rec.Logger()}, method: http.MethodPost, body: event("prices"), status: 500, level: slog.LevelError,
			message: failed, reason: "no Notify"},
		{name: "a Version not supported", handler: &Handler{Logger: rec.Logger(), Version: "9.9.9", Notify: h.Notify}, method: http.MethodPost,
			body: event("prices"), status: 500, level: slog.LevelError, message: failed, reason: "Version not supported"},
		{name: "a notification", method: http.MethodPost, token: logToken, body: event("prices"), status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rec.Entries())
			target := url
			if tc.method == http.MethodGet && tc.status == 200 {
				target += "&echo=abc"
			}
			got := serve(t, srv, tc.handler, tc.method, target, tc.token, tc.body)
			if got != tc.status {
				t.Fatalf("answered %d, want %d", got, tc.status)
			}
			entries := rec.Entries()[before:]
			if tc.message == "" {
				if len(entries) != 0 {
					t.Errorf("logged %v", entries)
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("logged %d entries, want 1: %v", len(entries), entries)
			}
			e := entries[0]
			if e.Level != tc.level || e.Message != tc.message || e.Attrs["reason"] != tc.reason || e.Attrs["status"] != strconv.Itoa(tc.status) {
				t.Errorf("logged %v, want %v %q with reason %q and status %d", e, tc.level, tc.message, tc.reason, tc.status)
			}
			for k, v := range tc.attrs {
				if e.Attrs[k] != v {
					t.Errorf("%s = %s, want %s", k, e.Attrs[k], v)
				}
			}
			if v, ok := e.Attrs["error"]; ok && !strings.HasPrefix(v, `"`) {
				t.Errorf("error %s is not quoted", v)
			}
		})
	}

	// A Notify that does not return normally is reported through the
	// Logger, as the other components report one, and not in the ErrorLog.
	before := len(rec.Entries())
	if got := serve(t, srv, nil, http.MethodPost, url, logToken, event("stop")); got != 500 {
		t.Errorf("a Notify that does not return normally: answered %d", got)
	}
	if n := rec.Failures("Notify"); n != 1 {
		t.Errorf("reported %d times: %v", n, rec.Entries()[before:])
	}
	if s := errorLog.String(); s != "" {
		t.Errorf("written to the ErrorLog as well:\n%s", s)
	}

	for _, e := range rec.Entries() {
		text := e.Message
		for k, v := range e.Attrs {
			text += " " + k + "=" + v
		}
		for _, secret := range []string{logToken, logWrong, logMarker, logQuery, "/hooks/site-42", `"eventName"`} {
			if strings.Contains(text, secret) {
				t.Errorf("an entry holds %q: %s", secret, text)
			}
		}
	}
}

// serve sends a request to srv, or to handler when not nil, and returns
// the status of the answer.
func serve(t *testing.T, srv *httptest.Server, handler *Handler, method, url, token, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if handler != nil {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A Handler without a Logger writes nothing: not to slog.Default, not to
// the standard logger, not to the ErrorLog of its server, for what it
// refuses and for what it fails on.
func TestHandlerWithoutLoggerIsSilent(t *testing.T) {
	rec := guardtest.NewHandler()
	var std lockedBuffer
	oldDefault, oldOut, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(rec.Logger())
	log.SetOutput(&std)
	t.Cleanup(func() {
		// slog.SetDefault of the original logger leaves the standard
		// logger as it is: restored after.
		slog.SetDefault(oldDefault)
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})
	h := &Handler{BearerToken: logToken, Notify: func(context.Context, openadr.Notification) error { return errors.New("no") }}
	var errorLog lockedBuffer
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(&errorLog, "", 0)
	srv.Start()
	defer srv.Close()
	event := strings.Replace(logEvent, "NAME", "prices", 1)
	for _, r := range []struct {
		method, token, body string
		status              int
	}{
		{http.MethodGet, "", "", 400},
		{http.MethodPut, "", "", 405},
		{http.MethodPost, logWrong, event, 401},
		{http.MethodPost, logToken, "{", 400},
		{http.MethodPost, logToken, event, 500},
	} {
		if got := serve(t, srv, nil, r.method, srv.URL, r.token, r.body); got != r.status {
			t.Errorf("%s: answered %d, want %d", r.method, got, r.status)
		}
	}
	if got := serve(t, nil, &Handler{}, http.MethodPost, "http://h.example/", "", event); got != 500 {
		t.Errorf("without Notify: answered %d", got)
	}
	if entries := rec.Entries(); len(entries) != 0 {
		t.Errorf("slog.Default has %v", entries)
	}
	if s := std.String() + errorLog.String(); s != "" {
		t.Errorf("written:\n%s", s)
	}
}

// A handler of the Logger that does not return normally costs its entry,
// is reported through the Logger, and the request is answered as before.
func TestHandlerLoggerThatDoesNotReturnNormally(t *testing.T) {
	rec := guardtest.NewHandler()
	rec.FailWhen(plainEntries)
	h := &Handler{BearerToken: logToken, Logger: rec.Logger(), Notify: func(context.Context, openadr.Notification) error { return nil }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	if got := serve(t, srv, nil, http.MethodPut, srv.URL, "", ""); got != 405 {
		t.Errorf("answered %d", got)
	}
	if n := rec.Failures("Logger"); n != 1 {
		t.Errorf("the Logger was reported %d times: %v", n, rec.Entries())
	}
	if got := serve(t, srv, nil, http.MethodPost, srv.URL, logToken, strings.Replace(logEvent, "NAME", "prices", 1)); got != 200 {
		t.Errorf("the next notification: answered %d", got)
	}
}

func TestQuoted(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", `""`},
		{"POST", `"POST"`},
		{"a\nb\x00c\x1b[31m", `"a\nb\x00c\x1b[31m"`},
		{"\xff\xfe", `"\xff\xfe"`},
		{strings.Repeat("a", 255), strconv.Quote(strings.Repeat("a", 255))},
		{strings.Repeat("a", 256), strconv.Quote(strings.Repeat("a", 256))},
		{strings.Repeat("a", 257), strconv.Quote(strings.Repeat("a", 256)) + "... (257 bytes)"},
		// Cut at a character boundary: "é" is two bytes, at 255 and 256.
		{strings.Repeat("a", 255) + "é" + "b", strconv.Quote(strings.Repeat("a", 255)) + "... (258 bytes)"},
		{strings.Repeat("a", 1<<20), strconv.Quote(strings.Repeat("a", 256)) + "... (1048576 bytes)"},
	} {
		if got := quoted(tc.in); got != tc.want {
			t.Errorf("quoted(%.40q) = %.80s, want %.80s", tc.in, got, tc.want)
		}
	}
}
