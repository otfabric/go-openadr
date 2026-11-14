// SPDX-License-Identifier: MIT

package webhook

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
)

// atOnce calls f from n goroutines that start together, and returns when
// all of them have returned.
func atOnce(n int, f func()) {
	var start, all sync.WaitGroup
	start.Add(1)
	for range n {
		all.Add(1)
		go func() {
			defer all.Done()
			start.Wait()
			f()
		}()
	}
	start.Done()
	all.Wait()
}

// within fails the test unless done is closed within 10 seconds.
func within(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("%s did not happen within 10s", what)
	}
}

// waiting is a transport whose requests wait until they are cancelled.
type waiting struct{}

func (waiting) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// L1, L2: Close from several goroutines at once, and again after.
func TestSenderCloseFromSeveralGoroutines(t *testing.T) {
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		s := NewSender(SenderConfig{AllowInsecure: true, HTTPClient: &http.Client{Transport: waiting{}}, Workers: 1})
		// A notification on its way: each Close gives up on it.
		s.Send("http://127.0.0.1/hook", "", []byte(`{}`))
		atOnce(8, func() {
			if err := s.Close(expired); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Close: %v", err)
			}
		})
		if err := s.Close(expired); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Close again: %v", err)
		}
		closed := make(chan error, 1)
		s.SendConfirmed("http://127.0.0.1/hook", "", []byte(`{}`), func(err error) { closed <- err })
		if err := <-closed; !errors.Is(err, openadr.ErrClosed) {
			t.Fatalf("SendConfirmed after Close: %v", err)
		}
	}
}

// stopping is Metrics whose every callback does not return normally.
type stopping struct{}

func (stopping) OnDelivered(int, time.Duration)     { guardtest.Stop() }
func (stopping) OnFailed(int, time.Duration, error) { guardtest.Stop() }
func (stopping) OnDropped()                         { guardtest.Stop() }
func stopDone(error)                                { guardtest.Stop() }
func plainEntries(e guardtest.Entry) bool           { return !strings.HasSuffix(e.Message, guardtest.Message) }
func notDelivered(e guardtest.Entry) bool           { return strings.Contains(e.Message, "not delivered") }
func queueFull(e guardtest.Entry) bool              { return strings.Contains(e.Message, "queue full") }
func count(entries []guardtest.Entry, f func(guardtest.Entry) bool) (n int) {
	for _, e := range entries {
		if f(e) {
			n++
		}
	}
	return n
}

// The Metrics callbacks, the done function of SendConfirmed and the
// Logger's handler that do not return normally are logged, and the Sender
// goes on.
func TestSenderCallbacksThatDoNotReturnNormally(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gone":
			w.WriteHeader(http.StatusGone)
		case "/wait":
			arrived <- struct{}{}
			<-release
		}
	}))
	defer hook.Close()
	h := guardtest.NewHandler()
	h.FailWhen(plainEntries)
	s := NewSender(SenderConfig{AllowInsecure: true, Attempts: 1, Workers: 1, Queue: 1, Logger: h.Logger(), Metrics: stopping{}})
	defer closeSender(t, s)

	s.SendConfirmed(hook.URL+"/ok", "", []byte(`{}`), stopDone)
	h.WaitFailures(t, "Metrics.OnDelivered", 1)
	h.WaitFailures(t, "SendConfirmed done", 1)
	s.SendConfirmed(hook.URL+"/gone", "", []byte(`{}`), stopDone)
	h.WaitFailures(t, "Metrics.OnFailed", 1)
	h.WaitFailures(t, "SendConfirmed done", 2)
	h.Wait(t, "the entry of the failure, lost", func(entries []guardtest.Entry) bool { return count(entries, notDelivered) == 1 })
	h.WaitFailures(t, "Logger", 1)

	// One with the webhook, one waiting, and the third dropped, on this
	// goroutine: reported before SendConfirmed returns.
	s.Send(hook.URL+"/wait", "", []byte(`{}`))
	within(t, "the request", arrived)
	s.Send(hook.URL+"/ok", "", []byte(`{}`))
	s.SendConfirmed(hook.URL+"/ok", "", []byte(`{}`), stopDone)
	if n := h.Failures("Metrics.OnDropped"); n != 1 {
		t.Errorf("OnDropped reported %d times", n)
	}
	if n := h.Failures("SendConfirmed done"); n != 3 {
		t.Errorf("done reported %d times", n)
	}
	if n, lost := h.Failures("Logger"), count(h.Entries(), queueFull); n != 2 || lost != 1 {
		t.Errorf("the Logger reported %d times, for %d entries of a full queue", n, lost)
	}
	close(release)
	h.WaitFailures(t, "Metrics.OnDelivered", 3)

	// And it goes on.
	delivered := make(chan error, 1)
	s.SendConfirmed(hook.URL+"/ok", "", []byte(`{}`), func(err error) { delivered <- err })
	select {
	case err := <-delivered:
		if err != nil {
			t.Errorf("delivery after all that: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no delivery after all that")
	}
}

// lockedBuffer is a log destination that two goroutines may share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A Notify that does not return normally is answered 500, written to the
// ErrorLog of the server with its stack, and the Handler goes on; one that
// asks net/http to abort the response gets that.
func TestHandlerNotifyThatDoesNotReturnNormally(t *testing.T) {
	const event = `{"objectType":"EVENT","operation":"CREATE","object":{"id":"e1","objectType":"EVENT",
		"createdDateTime":"2026-01-01T00:00:00Z","modificationDateTime":"2026-01-01T00:00:00Z","programID":"p1","eventName":"prices"}}`
	var calls sync.Map
	h := &Handler{Notify: func(ctx context.Context, n openadr.Notification) error {
		switch n.Object.(openadr.Event).Name {
		case "stop":
			guardtest.Stop()
		case "abort":
			panic(http.ErrAbortHandler)
		}
		calls.Store(n.Object.(openadr.Event).Name, true)
		return nil
	}}
	var errorLog lockedBuffer
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(&errorLog, "", 0)
	srv.Start()
	defer srv.Close()
	post := func(name string) (*http.Response, error) {
		body := strings.Replace(event, `"prices"`, `"`+name+`"`, 1)
		resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(body))
		if err == nil {
			_ = resp.Body.Close()
		}
		return resp, err
	}

	resp, err := post("stop")
	if err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("Notify that does not return normally: %v, %v", resp, err)
	}
	entry := errorLog.String()
	if !strings.HasPrefix(entry, "webhook: callback did not return normally: callback=Notify value="+guardtest.Value+"\n") ||
		!strings.Contains(entry, "guardtest.Stop") {
		t.Errorf("ErrorLog:\n%s", entry)
	}
	if resp, err := post("abort"); err == nil {
		t.Errorf("an aborted response was answered %d", resp.StatusCode)
	}
	if resp, err := post("prices"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("the next notification: %v, %v", resp, err)
	}
	if _, ok := calls.Load("prices"); !ok {
		t.Error("the next notification did not reach Notify")
	}
	if got := errorLog.String(); got != entry {
		t.Errorf("more in the ErrorLog than the one entry:\n%s", got[len(entry):])
	}
}
