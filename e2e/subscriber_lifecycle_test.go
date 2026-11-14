// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
	"github.com/otfabric/go-openadr/notification/websocket"
	"github.com/otfabric/go-openadr/server"
)

// within fails the test unless done is closed within 10 seconds.
func within(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("%s did not return within 10s", what)
	}
}

// socketVTN is a VTN that notifies Business Logic of programs over its
// WebSocket.
func socketVTN(t *testing.T) *stack {
	t.Helper()
	vtn := start(t, options{websocket: &server.WebSocketConfig{}})
	if _, err := vtn.BL.CreateSubscription(context.Background(), openadr.Subscription{ClientName: "bl", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
		t.Fatal(err)
	}
	return vtn
}

// L1, L2: Close from several goroutines at once, and again after.
func TestWebSocketSubscriberCloseFromSeveralGoroutines(t *testing.T) {
	for range 50 {
		sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Backoff: time.Hour,
			Dial:   func(context.Context) (*client.NotificationStream, error) { return nil, errors.New("no route to host") },
			Notify: func(context.Context, openadr.Notification) {}})
		if err != nil {
			t.Fatal(err)
		}
		var start, all sync.WaitGroup
		start.Add(1)
		for range 8 {
			all.Go(func() {
				start.Wait()
				_ = sub.Close()
			})
		}
		start.Done()
		all.Wait()
		if err := sub.Close(); err != nil {
			t.Errorf("Close again: %v", err)
		}
	}
}

// L10: a Notify that calls Close returns from it, and Notify is not called
// again.
func TestWebSocketSubscriberNotifyCallsClose(t *testing.T) {
	vtn := socketVTN(t)
	var calls atomic.Int32
	closed := make(chan struct{})
	created := make(chan *websocket.Subscriber, 1)
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream, Backoff: 10 * time.Millisecond,
		Notify: func(context.Context, openadr.Notification) {
			if calls.Add(1) > 1 {
				return
			}
			_ = (<-created).Close()
			close(closed)
		}})
	if err != nil {
		t.Fatal(err)
	}
	created <- sub
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		mustProgram(t, vtn.BL, openadr.Program{Name: name})
	}
	within(t, "Close called from Notify", closed)
	if err := sub.Close(); err != nil {
		t.Error(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Notify was called %d times", n)
	}
	if err := sub.Ready(ctx); !errors.Is(err, openadr.ErrClosed) {
		t.Errorf("Ready after Close: %v", err)
	}
}

// A Close from another goroutine still waits for a Notify in progress,
// which ends when its context does.
func TestWebSocketSubscriberCloseWaitsForNotify(t *testing.T) {
	vtn := socketVTN(t)
	entered := make(chan struct{})
	var first, returned atomic.Bool
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream, Backoff: 10 * time.Millisecond,
		Notify: func(ctx context.Context, _ openadr.Notification) {
			if !first.CompareAndSwap(false, true) {
				return
			}
			close(entered)
			<-ctx.Done()
			returned.Store(true)
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	mustProgram(t, vtn.BL, openadr.Program{Name: "one"})
	within(t, "Notify", entered)
	if err := sub.Close(); err != nil {
		t.Error(err)
	}
	if !returned.Load() {
		t.Error("Close returned while Notify was running")
	}
}

// closingMetrics closes the Subscriber when it is connected.
type closingMetrics struct {
	created  chan *websocket.Subscriber
	returned chan struct{}
	first    atomic.Bool
}

func (m *closingMetrics) OnConnected() {
	if m.first.CompareAndSwap(false, true) {
		_ = (<-m.created).Close()
		close(m.returned)
	}
}
func (*closingMetrics) OnDisconnected(error) {}
func (*closingMetrics) OnReceived()          {}

// closingHandler is a log handler that closes the Subscriber at its first
// entry.
type closingHandler struct {
	created  chan *websocket.Subscriber
	returned chan struct{}
	first    atomic.Bool
}

func (h *closingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *closingHandler) Handle(context.Context, slog.Record) error {
	if h.first.CompareAndSwap(false, true) {
		_ = (<-h.created).Close()
		close(h.returned)
	}
	return nil
}
func (h *closingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *closingHandler) WithGroup(string) slog.Handler      { return h }

// L10: Close called from Dial, from a Metrics callback or from the Logger's
// handler, all on the goroutine Close would otherwise wait for, returns.
func TestWebSocketSubscriberCallbackCallsClose(t *testing.T) {
	refused := func(context.Context) (*client.NotificationStream, error) { return nil, errors.New("no route to host") }
	notify := func(context.Context, openadr.Notification) {}
	newSub := func(t *testing.T, cfg websocket.SubscriberConfig) *websocket.Subscriber {
		t.Helper()
		cfg.Notify, cfg.Backoff = notify, time.Hour
		sub, err := websocket.NewSubscriber(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return sub
	}
	t.Run("Dial", func(t *testing.T) {
		created, returned := make(chan *websocket.Subscriber, 1), make(chan struct{})
		sub := newSub(t, websocket.SubscriberConfig{Dial: func(ctx context.Context) (*client.NotificationStream, error) {
			_ = (<-created).Close()
			close(returned)
			return refused(ctx)
		}})
		created <- sub
		within(t, "Close called from Dial", returned)
		_ = sub.Close()
	})
	t.Run("Metrics", func(t *testing.T) {
		vtn := socketVTN(t)
		m := &closingMetrics{created: make(chan *websocket.Subscriber, 1), returned: make(chan struct{})}
		sub := newSub(t, websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream, Metrics: m})
		m.created <- sub
		within(t, "Close called from OnConnected", m.returned)
		_ = sub.Close()
	})
	t.Run("Logger", func(t *testing.T) {
		h := &closingHandler{created: make(chan *websocket.Subscriber, 1), returned: make(chan struct{})}
		sub := newSub(t, websocket.SubscriberConfig{Dial: refused, Logger: slog.New(h)})
		h.created <- sub
		within(t, "Close called from the Logger's handler", h.returned)
		_ = sub.Close()
	})
}

// receivedMetrics calls received at the first OnReceived.
type receivedMetrics struct {
	received func()
	once     sync.Once
}

func (*receivedMetrics) OnConnected()         {}
func (*receivedMetrics) OnDisconnected(error) {}
func (m *receivedMetrics) OnReceived()        { m.once.Do(m.received) }

// L10: a Close from OnReceived returns, and the notification it was called
// for does not reach Notify: Notify is not called after Close has returned.
func TestWebSocketSubscriberOnReceivedCallsClose(t *testing.T) {
	vtn := socketVTN(t)
	var (
		sub      *websocket.Subscriber
		created  = make(chan struct{})
		returned = make(chan struct{})
		calls    atomic.Int32
	)
	m := &receivedMetrics{received: func() {
		<-created
		_ = sub.Close()
		close(returned)
	}}
	s, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream, Backoff: 10 * time.Millisecond, Metrics: m,
		Notify: func(context.Context, openadr.Notification) { calls.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	sub = s
	close(created)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		mustProgram(t, vtn.BL, openadr.Program{Name: name})
	}
	within(t, "Close called from OnReceived", returned)
	// This Close waits for the Subscriber's goroutine, and so for what it
	// does with the notification that OnReceived was called for.
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("Notify was called %d times after a Close from OnReceived", n)
	}
}

// stoppingMetrics is Metrics whose every callback does not return normally.
type stoppingMetrics struct{}

func (stoppingMetrics) OnConnected()         { guardtest.Stop() }
func (stoppingMetrics) OnDisconnected(error) { guardtest.Stop() }
func (stoppingMetrics) OnReceived()          { guardtest.Stop() }

// Dial, Notify, the Metrics callbacks and the Logger's handler that do not
// return normally are logged, and the Subscriber goes on.
func TestWebSocketSubscriberCallbacksThatDoNotReturnNormally(t *testing.T) {
	vtn := socketVTN(t)
	h := guardtest.NewHandler()
	h.FailWhen(func(e guardtest.Entry) bool { return !strings.HasSuffix(e.Message, guardtest.Message) })
	var dials, notified atomic.Int32
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial: func(ctx context.Context) (*client.NotificationStream, error) {
			if dials.Add(1) == 1 {
				guardtest.Stop()
			}
			return vtn.BL.NotificationStream(ctx)
		},
		Notify: func(context.Context, openadr.Notification) {
			notified.Add(1)
			guardtest.Stop()
		},
		Backoff: 10 * time.Millisecond, Logger: h.Logger(), Metrics: stoppingMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	// The Dial that did not return normally counts as one that failed: it
	// is logged, and so is the entry that says the VTN could not be
	// reached, which the handler loses.
	for callback, n := range map[string]int{"Dial": 1, "Logger": 1, "Metrics.OnConnected": 1} {
		if got := h.Failures(callback); got != n {
			t.Errorf("%s reported %d times, want %d", callback, got, n)
		}
	}
	// Both notifications arrive: the first Notify did not stop the second.
	mustProgram(t, vtn.BL, openadr.Program{Name: "one"})
	mustProgram(t, vtn.BL, openadr.Program{Name: "two"})
	h.WaitFailures(t, "Notify", 2)
	h.WaitFailures(t, "Metrics.OnReceived", 2)
	if n := notified.Load(); n != 2 {
		t.Errorf("Notify was called %d times", n)
	}
	if err := sub.Close(); err != nil {
		t.Error(err)
	}
	// Reported on the way out, before Close returned.
	if n := h.Failures("Metrics.OnDisconnected"); n != 1 {
		t.Errorf("OnDisconnected reported %d times", n)
	}
}

// L9: Notify may call the other methods of the Subscriber: it is ready, in
// its first session.
func TestWebSocketSubscriberNotifyCallsMethods(t *testing.T) {
	vtn := socketVTN(t)
	type seen struct {
		ready    error
		sessions int
	}
	var sub *websocket.Subscriber
	created, got := make(chan struct{}), make(chan seen, 1)
	s, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream, Backoff: 10 * time.Millisecond,
		Notify: func(ctx context.Context, _ openadr.Notification) {
			<-created
			select {
			case got <- seen{sub.Ready(ctx), sub.Sessions()}:
			default:
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	sub = s
	close(created)
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	mustProgram(t, vtn.BL, openadr.Program{Name: "one"})
	select {
	case g := <-got:
		if g.ready != nil || g.sessions != 1 {
			t.Errorf("in Notify: Ready = %v, Sessions = %d", g.ready, g.sessions)
		}
	case <-ctx.Done():
		t.Fatal("Notify did not return from Ready and Sessions")
	}
}
