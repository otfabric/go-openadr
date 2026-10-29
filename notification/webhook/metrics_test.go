// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

type outcomes struct {
	mu     sync.Mutex
	events []string
	last   error
}

func (o *outcomes) add(s string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events, o.last = append(o.events, s), err
}

func (o *outcomes) OnDelivered(attempts int, _ time.Duration) {
	o.add(fmt.Sprintf("delivered after %d", attempts), nil)
}
func (o *outcomes) OnFailed(attempts int, _ time.Duration, err error) {
	o.add(fmt.Sprintf("failed after %d", attempts), err)
}
func (o *outcomes) OnDropped() { o.add("dropped", nil) }

func (o *outcomes) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		o.mu.Lock()
		got := slices.Clone(o.events)
		o.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d outcomes of %d: %v", len(got), n, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Every notification ends in exactly one of three ways, and the sender
// says which.
func TestSenderMetrics(t *testing.T) {
	var calls atomic.Int64
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flaky":
			if calls.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case "/down":
			w.WriteHeader(http.StatusInternalServerError)
		case "/gone":
			w.WriteHeader(http.StatusGone)
		}
	}))
	defer hook.Close()
	m := &outcomes{}
	s := NewSender(SenderConfig{AllowInsecure: true, Backoff: time.Millisecond, Attempts: 3, Workers: 1, Metrics: m})
	defer closeSender(t, s)

	s.Send(hook.URL+"/ok", "", []byte(`{}`))
	s.Send(hook.URL+"/flaky", "", []byte(`{}`))
	s.Send(hook.URL+"/down", "", []byte(`{}`))
	s.Send(hook.URL+"/gone", "", []byte(`{}`))
	want := []string{"delivered after 1", "delivered after 3", "failed after 3", "failed after 1"}
	if got := m.wait(t, 4); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSenderMetricsDroppedAndClosed(t *testing.T) {
	release := make(chan struct{})
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer hook.Close()
	m := &outcomes{}
	s := NewSender(SenderConfig{AllowInsecure: true, Backoff: time.Hour, Workers: 1, Queue: 1, Metrics: m})
	// One is with the webhook, one waits, and there is no room for a third.
	s.Send(hook.URL, "", []byte(`1`))
	time.Sleep(50 * time.Millisecond)
	s.Send(hook.URL, "", []byte(`2`))
	s.Send(hook.URL, "", []byte(`3`))
	if got := m.wait(t, 1); got[0] != "dropped" {
		t.Errorf("got %v", got)
	}
	// Closed while the first waits for its second attempt: it is given up
	// on, and says why.
	close(release)
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = s.Close(ctx)
	got := m.wait(t, 2)
	if got[1] != "failed after 1" || !errors.Is(m.last, openadr.ErrClosed) {
		t.Errorf("got %v, last error %v", got, m.last)
	}
}

// A webhook that says when to come back is taken at its word.
func TestSenderHonoursRetryAfter(t *testing.T) {
	var times []time.Time
	var mu sync.Mutex
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		first := len(times) == 1
		mu.Unlock()
		if first {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer hook.Close()
	m := &outcomes{}
	s := NewSender(SenderConfig{AllowInsecure: true, Backoff: time.Millisecond, Metrics: m})
	defer closeSender(t, s)
	s.Send(hook.URL, "", []byte(`{}`))
	m.wait(t, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(times) != 2 || times[1].Sub(times[0]) < 900*time.Millisecond {
		t.Errorf("attempts at %v", times)
	}
}

// SendConfirmed says what became of a notification, once.
func TestSendConfirmed(t *testing.T) {
	var status atomic.Int64
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
		}
	}))
	defer hook.Close()
	s := NewSender(SenderConfig{AllowInsecure: true, Backoff: time.Millisecond, Attempts: 2})
	outcome := func(url string) error {
		t.Helper()
		done := make(chan error, 2)
		s.SendConfirmed(url, "", []byte(`{}`), func(err error) { done <- err })
		select {
		case err := <-done:
			select {
			case again := <-done:
				t.Errorf("done was called twice, the second time with %v", again)
			case <-time.After(30 * time.Millisecond):
			}
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("done was not called")
			return nil
		}
	}
	permanent := func(err error) bool {
		var p interface{ Permanent() bool }
		return errors.As(err, &p) && p.Permanent()
	}
	if err := outcome(hook.URL); err != nil {
		t.Errorf("delivered: %v", err)
	}
	status.Store(http.StatusServiceUnavailable)
	if err := outcome(hook.URL); err == nil || permanent(err) {
		t.Errorf("a webhook that is down: %v, want an error that is not for good", err)
	}
	status.Store(http.StatusGone)
	var refused *RefusedError
	if err := outcome(hook.URL); !errors.As(err, &refused) || !permanent(err) {
		t.Errorf("a webhook that refuses: %v, want a RefusedError", err)
	}
	// A URL the sender does not send to is refused for good as well.
	strict := NewSender(SenderConfig{})
	done := make(chan error, 1)
	strict.SendConfirmed("http://example.com/hook", "", nil, func(err error) { done <- err })
	if err := <-done; !permanent(err) {
		t.Errorf("an http URL to a sender that wants https: %v", err)
	}
	closeSender(t, strict)
	closeSender(t, s)
	if err := outcome(hook.URL); !errors.Is(err, openadr.ErrClosed) || permanent(err) {
		t.Errorf("after Close: %v", err)
	}
}
