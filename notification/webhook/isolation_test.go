// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// byHost is a transport that answers each request with the function for
// its host, without a network.
type byHost map[string]func(*http.Request) int

func (b byHost) RoundTrip(r *http.Request) (*http.Response, error) {
	status := b[r.URL.Hostname()](r)
	if status == 0 {
		return nil, r.Context().Err()
	}
	resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}
	if status == http.StatusServiceUnavailable {
		resp.Header.Set("Retry-After", "60")
	}
	return resp, nil
}

// Webhooks on four hosts, as many as there are workers, that answer 503
// and ask to come back in a minute hold no worker while they wait: a
// notification for another host is delivered at once.
func TestWebhooksThatAskToWaitDelayOnlyThemselves(t *testing.T) {
	var asked atomic.Int64
	wait := func(*http.Request) int { asked.Add(1); return http.StatusServiceUnavailable }
	transport := byHost{"ok.example": func(*http.Request) int { return http.StatusOK }}
	for i := range 4 {
		transport[fmt.Sprintf("wait%d.example", i)] = wait
	}
	s := NewSender(SenderConfig{AllowInsecure: true, HTTPClient: &http.Client{Transport: transport}, Workers: 4, Timeout: time.Hour})
	given := make(chan error, 64)
	for i := range 4 {
		for n := range 4 {
			s.SendConfirmed(fmt.Sprintf("http://wait%d.example/hook?n=%d", i, n), "", []byte(`{}`), func(err error) { given <- err })
		}
	}
	// Every URL is asked once, and then waits for its next attempt.
	for asked.Load() < 16 {
		time.Sleep(time.Millisecond)
	}
	delivered := make(chan struct{})
	s.SendConfirmed("http://ok.example/hook", "", []byte(`{}`), func(err error) {
		if err != nil {
			t.Errorf("the notification for another host: %v", err)
		}
		close(delivered)
	})
	within(t, "the delivery to another host", delivered)

	if n := asked.Load(); n != 16 {
		t.Errorf("%d requests to the hosts that asked to wait, want 16", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Close: %v", err)
	}
	for range 16 {
		if err := <-given; !errors.Is(err, openadr.ErrClosed) {
			t.Errorf("a notification that waited: %v", err)
		}
	}
}

// One host filling its share of the queue gets its own notifications
// dropped, and not those of another host.
func TestOneHostDoesNotFillTheQueueOfAnother(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	transport := byHost{
		"full.example": func(*http.Request) int {
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-release
			return http.StatusOK
		},
		"ok.example": func(*http.Request) int { return http.StatusOK },
	}
	m := &outcomes{}
	s := NewSender(SenderConfig{AllowInsecure: true, HTTPClient: &http.Client{Transport: transport}, Workers: 1, Queue: 4, Metrics: m})
	defer closeSender(t, s)
	// One on its way, four waiting, and the rest dropped: over URLs of
	// its own, too.
	s.Send("http://full.example/hook?n=first", "", []byte(`{}`))
	within(t, "the first request", arrived)
	for n := range 19 {
		s.Send(fmt.Sprintf("http://full.example/hook?n=%d", n), "", []byte(`{}`))
	}
	if got := len(m.wait(t, 15)); got != 15 {
		t.Errorf("%d outcomes, want 15 dropped", got)
	}
	var dropped []error
	var mu sync.Mutex
	for range 4 {
		s.SendConfirmed("http://ok.example/hook", "", []byte(`{}`), func(err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				dropped = append(dropped, err)
			}
		})
	}
	close(release)
	got := m.wait(t, 24)
	n := 0
	for _, e := range got {
		if e == "dropped" {
			n++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 15 || len(dropped) != 0 {
		t.Errorf("%d dropped in all, want 15; the other host's: %v", n, dropped)
	}
}

// The query of a callback URL is neither logged nor reported for a
// request that failed on its way, such as one to an address the default
// client does not connect to.
func TestFailedRequestWithoutQuery(t *testing.T) {
	var logged lockedBuffer
	m := &outcomes{}
	s := NewSender(SenderConfig{Attempts: 1, Logger: slog.New(slog.NewTextHandler(&logged, nil)), Metrics: m})
	done := make(chan error, 1)
	s.SendConfirmed("https://127.0.0.1:1/hook?secret=kept-out", "", []byte(`{}`), func(err error) { done <- err })
	err := <-done
	closeSender(t, s)
	m.wait(t, 1)
	if err == nil || strings.Contains(err.Error(), "kept-out") || !strings.Contains(err.Error(), "127.0.0.1:1/hook") {
		t.Errorf("done: %v", err)
	}
	if m.last == nil || strings.Contains(m.last.Error(), "kept-out") {
		t.Errorf("OnFailed: %v", m.last)
	}
	if out := logged.String(); !strings.Contains(out, "not delivered") || strings.Contains(out, "kept-out") {
		t.Errorf("log:\n%s", out)
	}
}
