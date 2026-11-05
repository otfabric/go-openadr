// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// A webhook that never answers. Once Close has run out of time, the request
// on its way is cancelled and the rest of the queue is given up on without
// a request each: Close does not wait out a Timeout per notification.
func TestCloseDoesNotSendWhatIsQueued(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	var closed atomic.Int64
	s := NewSender(SenderConfig{AllowInsecure: true, Queue: 64, Workers: 1, Timeout: time.Minute})
	for range 20 {
		s.SendConfirmed(srv.URL, "", []byte("n"), func(err error) {
			if errors.Is(err, openadr.ErrClosed) {
				closed.Add(1)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close: %v, want the context's error", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Close took %v", took)
	}
	if n := requests.Load(); n > 1 {
		t.Errorf("%d requests after Close gave up, want at most the one on its way", n)
	}
	if n := closed.Load(); n != 20 {
		t.Errorf("%d of 20 notifications given up as closed", n)
	}
}

// A Backoff and a MaxBackoff as long as a Duration holds neither overflow
// into a negative wait, which would retry at once and for ever, nor keep
// Close waiting.
func TestBackoffNearMaxDuration(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "99999999999999999999")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s := NewSender(SenderConfig{AllowInsecure: true, Attempts: 1000, Backoff: math.MaxInt64, MaxBackoff: math.MaxInt64})
	failed := make(chan error, 1)
	s.SendConfirmed(srv.URL, "", []byte("n"), func(err error) { failed <- err })
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Long enough for a hot loop to show.
	time.Sleep(200 * time.Millisecond)
	if n := requests.Load(); n != 1 {
		t.Errorf("%d requests, want 1 and then a very long wait", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = s.Close(ctx)
	select {
	case err := <-failed:
		if !errors.Is(err, openadr.ErrClosed) {
			t.Errorf("given up with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notification was never given up")
	}
}

// A webhook that answers 200 and then dribbles its body is bounded by
// Timeout, and a redirect is not followed: the token would go with it.
func TestHostileWebhooks(t *testing.T) {
	var stolen atomic.Int64
	thief := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { stolen.Add(1) }))
	defer thief.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, thief.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	release := make(chan struct{})
	dribble := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-release:
				return
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	defer dribble.Close()
	defer close(release)

	s := NewSender(SenderConfig{AllowInsecure: true, Attempts: 2, Backoff: time.Millisecond, Timeout: 300 * time.Millisecond})
	defer closeSender(t, s)
	result := func(url string) error {
		t.Helper()
		done := make(chan error, 1)
		s.SendConfirmed(url, "secret-token", []byte("n"), func(err error) { done <- err })
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatalf("no outcome for %s", url)
			return nil
		}
	}
	if err := result(dribble.URL); err != nil {
		t.Errorf("a dribbled body after 200: %v", err)
	}
	var refused *RefusedError
	if err := result(redirect.URL); !errors.As(err, &refused) {
		t.Errorf("a redirect: %v, want a RefusedError", err)
	}
	if n := stolen.Load(); n != 0 {
		t.Errorf("the redirect was followed %d times", n)
	}
}
