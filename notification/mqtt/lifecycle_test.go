// SPDX-License-Identifier: MIT

package mqtt

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
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
	"github.com/otfabric/go-openadr/internal/mqtttest"
)

// atOnce calls f from n goroutines that start together, and returns when
// all of them have returned.
func atOnce(n int, f func(i int)) {
	var start, all sync.WaitGroup
	start.Add(1)
	for i := range n {
		all.Add(1)
		go func() {
			defer all.Done()
			start.Wait()
			f(i)
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
		t.Fatalf("%s did not return within 10s", what)
	}
}

// nowhere is a broker that cannot be reached, and a wait before the next
// attempt that only Close ends.
var nowhere = Broker{URL: "mqtt://127.0.0.1:1", Backoff: time.Hour}

// L1, L2: Close from several goroutines at once, and again after.
func TestPublisherCloseFromSeveralGoroutines(t *testing.T) {
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		p, err := NewPublisher(PublisherConfig{Broker: nowhere})
		if err != nil {
			t.Fatal(err)
		}
		// A notification that waits for the broker: each Close gives up
		// on it and stops the Publisher.
		p.Publish("events/create", []byte(eventNotification))
		atOnce(8, func(int) {
			if err := p.Close(expired); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Close: %v", err)
			}
		})
		if err := p.Close(expired); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Close again: %v", err)
		}
		select {
		case <-p.done:
		default:
			t.Fatal("the Publisher runs after Close")
		}
	}
}

// L1, L2.
func TestSubscriberCloseFromSeveralGoroutines(t *testing.T) {
	for range 200 {
		s, err := NewSubscriber(SubscriberConfig{Broker: nowhere, Topics: []string{"events/+"},
			Notify: func(context.Context, string, openadr.Notification) {}})
		if err != nil {
			t.Fatal(err)
		}
		atOnce(8, func(int) { _ = s.Close() })
		if err := s.Close(); err != nil {
			t.Errorf("Close again: %v", err)
		}
		if err := s.Ready(context.Background()); !errors.Is(err, openadr.ErrClosed) {
			t.Fatalf("Ready after Close: %v", err)
		}
	}
}

// startBroker returns a test broker and a Publisher of it.
func startBroker(t *testing.T) (*mqtttest.Broker, *Publisher) {
	t.Helper()
	broker, err := mqtttest.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	pub, err := NewPublisher(PublisherConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pub.Close(ctx)
	})
	return broker, pub
}

// L10: a Notify that calls Close returns from it, the Subscriber is
// stopped by then, and Notify is not called again.
func TestSubscriberNotifyCallsClose(t *testing.T) {
	broker, pub := startBroker(t)
	var calls atomic.Int32
	closed := make(chan struct{})
	created := make(chan *Subscriber, 1)
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"events/+"},
		Notify: func(context.Context, string, openadr.Notification) {
			if calls.Add(1) > 1 {
				return
			}
			s := <-created
			_ = s.Close()
			select {
			case <-s.done:
			default:
				t.Error("Close called from Notify returned before the Subscriber stopped")
			}
			close(closed)
		}})
	if err != nil {
		t.Fatal(err)
	}
	created <- s
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		pub.Publish("events/create", []byte(eventNotification))
	}
	within(t, "Close called from Notify", closed)
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Notify was called %d times", n)
	}
}

// L10: Close called from a Metrics callback or from the Logger's handler,
// both on the goroutine Close would otherwise wait for, returns.
func TestSubscriberCallbackCallsClose(t *testing.T) {
	t.Run("Metrics", func(t *testing.T) {
		broker, _ := startBroker(t)
		m := &closer{returned: make(chan struct{}), created: make(chan *Subscriber, 1)}
		s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"events/+"},
			Metrics: m, Notify: func(context.Context, string, openadr.Notification) {}})
		if err != nil {
			t.Fatal(err)
		}
		m.created <- s
		within(t, "Close called from OnConnected", m.returned)
		within(t, "the Subscriber", s.done)
		_ = s.Close()
	})
	t.Run("Logger", func(t *testing.T) {
		h := &closingHandler{returned: make(chan struct{}), created: make(chan *Subscriber, 1)}
		s, err := NewSubscriber(SubscriberConfig{Broker: nowhere, Topics: []string{"events/+"}, Logger: slog.New(h),
			Notify: func(context.Context, string, openadr.Notification) {}})
		if err != nil {
			t.Fatal(err)
		}
		h.created <- s
		within(t, "Close called from the Logger's handler", h.returned)
		within(t, "the Subscriber", s.done)
		_ = s.Close()
	})
}

// closer is Metrics whose OnConnected closes the Subscriber.
type closer struct {
	first    atomic.Bool
	created  chan *Subscriber
	returned chan struct{}
}

func (c *closer) OnConnected() {
	if c.first.CompareAndSwap(false, true) {
		_ = (<-c.created).Close()
		close(c.returned)
	}
}
func (*closer) OnDisconnected(error) {}
func (*closer) OnPublished()         {}
func (*closer) OnDropped()           {}
func (*closer) OnReceived()          {}

// closingHandler is a log handler that closes the Subscriber at its first
// entry.
type closingHandler struct {
	first    atomic.Bool
	created  chan *Subscriber
	returned chan struct{}
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

// hooks is Metrics that calls connected at the first OnConnected, received
// at the first OnReceived and dropped at the first OnDropped, when it has
// them.
type hooks struct {
	connected, received, dropped func()
	c, r, d                      atomic.Bool
}

// firstCall calls f the first time it is called with its flag, when there
// is an f.
func firstCall(flag *atomic.Bool, f func()) {
	if f != nil && flag.CompareAndSwap(false, true) {
		f()
	}
}

func (h *hooks) OnConnected()       { firstCall(&h.c, h.connected) }
func (h *hooks) OnReceived()        { firstCall(&h.r, h.received) }
func (h *hooks) OnDropped()         { firstCall(&h.d, h.dropped) }
func (*hooks) OnDisconnected(error) {}
func (*hooks) OnPublished()         {}

// L9: a Metrics callback that calls Publish. It returns, and what it
// publishes is queued or dropped as any other notification.
func TestOnDroppedCallsPublish(t *testing.T) {
	var p *Publisher
	created, again := make(chan struct{}), make(chan struct{})
	m := &hooks{dropped: func() {
		<-created
		p.Publish("events/create", []byte(eventNotification))
		close(again)
	}}
	pub, err := NewPublisher(PublisherConfig{Broker: nowhere, Queue: 1, Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	p = pub
	close(created)
	published := make(chan struct{})
	go func() {
		defer close(published)
		// The Publisher holds one and queues one: the third, or an earlier
		// one, is dropped.
		for range 3 {
			pub.Publish("events/create", []byte(eventNotification))
		}
	}()
	within(t, "Publish called from OnDropped", again)
	within(t, "the Publish that OnDropped was called for", published)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	_ = pub.Close(expired)
}

// L9: Notify may call the other methods of the Subscriber: it is ready, in
// its first session.
func TestNotifyCallsMethods(t *testing.T) {
	broker, pub := startBroker(t)
	type seen struct {
		ready    error
		sessions int
	}
	var sub *Subscriber
	created, got := make(chan struct{}), make(chan seen, 1)
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"events/+"},
		Notify: func(ctx context.Context, _ string, _ openadr.Notification) {
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
	pub.Publish("events/create", []byte(eventNotification))
	select {
	case g := <-got:
		if g.ready != nil || g.sessions != 1 {
			t.Errorf("in Notify: Ready = %v, Sessions = %d", g.ready, g.sessions)
		}
	case <-ctx.Done():
		t.Fatal("Notify did not return from Ready and Sessions")
	}
}

// L10: OnConnected and Notify run on two goroutines of the Subscriber. A
// Close from OnConnected returns while a Notify is in progress, and a Close
// from that Notify returns as well: neither waits for the other.
func TestSubscriberCloseFromTwoCallbacks(t *testing.T) {
	broker, pub := startBroker(t)
	var (
		sub        *Subscriber
		created    = make(chan struct{}) // sub is set
		connected  = make(chan struct{}) // OnConnected runs
		notifying  = make(chan struct{}) // Notify runs
		fromHook   = make(chan struct{}) // Close has returned in OnConnected
		fromNotify = make(chan struct{}) // Close has returned in Notify
		first      sync.Once
	)
	m := &hooks{connected: func() {
		<-created
		close(connected)
		<-notifying
		_ = sub.Close()
		close(fromHook)
	}}
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"events/+"}, Metrics: m,
		Notify: func(context.Context, string, openadr.Notification) {
			first.Do(func() {
				close(notifying)
				timer := time.NewTimer(10 * time.Second)
				defer timer.Stop()
				select {
				case <-fromHook:
				case <-timer.C:
					t.Error("Close called from OnConnected waited for the Notify in progress")
					return
				}
				_ = sub.Close()
				close(fromNotify)
			})
		}})
	if err != nil {
		t.Fatal(err)
	}
	sub = s
	close(created)
	// Subscribed, and OnConnected has not returned: what is published now
	// reaches Notify meanwhile.
	within(t, "OnConnected", connected)
	pub.Publish("events/create", []byte(eventNotification))
	within(t, "Close called from Notify", fromNotify)
	within(t, "the Subscriber", s.done)
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}

// L10: a Close from OnReceived returns, and the notification it was called
// for does not reach Notify: no Notify begins after Close has returned.
func TestSubscriberOnReceivedCallsClose(t *testing.T) {
	broker, pub := startBroker(t)
	var (
		sub      *Subscriber
		created  = make(chan struct{})
		returned = make(chan struct{})
		calls    atomic.Int32
	)
	m := &hooks{received: func() {
		<-created
		_ = sub.Close()
		close(returned)
	}}
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"events/+"}, Metrics: m,
		Notify: func(context.Context, string, openadr.Notification) { calls.Add(1) }})
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
	for range 2 {
		pub.Publish("events/create", []byte(eventNotification))
	}
	within(t, "Close called from OnReceived", returned)
	// This Close waits for what the receiving goroutine does with the
	// notification that OnReceived was called for.
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("Notify was called %d times after a Close from OnReceived", n)
	}
}

// A Close from another goroutine still waits for a Notify in progress,
// which ends when its context does.
func TestSubscriberCloseWaitsForNotify(t *testing.T) {
	broker, pub := startBroker(t)
	entered := make(chan struct{})
	var once sync.Once
	var returned atomic.Bool
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"events/+"},
		Notify: func(ctx context.Context, _ string, _ openadr.Notification) {
			first := false
			once.Do(func() { first = true })
			if !first {
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
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	pub.Publish("events/create", []byte(eventNotification))
	within(t, "Notify", entered)
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if !returned.Load() {
		t.Error("Close returned while Notify was running")
	}
}

// A Metrics callback or a Logger that blocks holds up the Publish it is
// called from, and no other.
func TestPublishDoesNotWaitForAnotherPublishesCallback(t *testing.T) {
	for _, hook := range []string{"OnDropped", "Logger"} {
		t.Run(hook, func(t *testing.T) {
			b := &blocker{entered: make(chan struct{}), release: make(chan struct{})}
			cfg := PublisherConfig{Broker: nowhere, Queue: 1}
			if hook == "Logger" {
				cfg.Logger = slog.New(b)
			} else {
				cfg.Metrics = b
			}
			p, err := NewPublisher(cfg)
			if err != nil {
				t.Fatal(err)
			}
			first := make(chan struct{})
			go func() {
				defer close(first)
				// The Publisher holds one and queues one: the third, or an
				// earlier one, is dropped, and the hook blocks.
				for range 3 {
					p.Publish("events/create", []byte(eventNotification))
				}
			}()
			within(t, "the hook", b.entered)
			other := make(chan struct{})
			go func() {
				p.Publish("events/create", []byte(eventNotification))
				close(other)
			}()
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-other:
			case <-timer.C:
				t.Error("a Publish waited for the hook of another")
			}
			close(b.release)
			<-first
			<-other
			expired, cancel := context.WithCancel(context.Background())
			cancel()
			_ = p.Close(expired)
		})
	}
}

// blocker is Metrics and a log handler that block at their first drop
// until released, and return at once for the drops after.
type blocker struct {
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *blocker) block() {
	if b.first.CompareAndSwap(false, true) {
		close(b.entered)
		<-b.release
	}
}
func (b *blocker) OnDropped()                               { b.block() }
func (*blocker) OnConnected()                               {}
func (*blocker) OnDisconnected(error)                       {}
func (*blocker) OnPublished()                               {}
func (*blocker) OnReceived()                                {}
func (b *blocker) Enabled(context.Context, slog.Level) bool { return true }
func (b *blocker) Handle(_ context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "queue full") {
		b.block()
	}
	return nil
}
func (b *blocker) WithAttrs([]slog.Attr) slog.Handler { return b }
func (b *blocker) WithGroup(string) slog.Handler      { return b }

// stopping is Metrics whose every callback does not return normally.
type stopping struct{ counts }

func (s *stopping) OnConnected()           { s.counts.OnConnected(); guardtest.Stop() }
func (s *stopping) OnDisconnected(e error) { s.counts.OnDisconnected(e); guardtest.Stop() }
func (s *stopping) OnPublished()           { s.counts.OnPublished(); guardtest.Stop() }
func (s *stopping) OnDropped()             { s.counts.OnDropped(); guardtest.Stop() }
func (s *stopping) OnReceived()            { s.counts.OnReceived(); guardtest.Stop() }

// plainEntries makes a handler not return normally for every entry but the
// reports of what did not.
func plainEntries(e guardtest.Entry) bool { return !strings.HasSuffix(e.Message, guardtest.Message) }

// Notify, every Metrics callback and the Logger's handler that do not
// return normally are logged, and the Publisher and Subscriber go on.
func TestCallbacksThatDoNotReturnNormally(t *testing.T) {
	broker, err := mqtttest.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	login := Broker{URL: broker.URL(), Backoff: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	subLog, pubLog := guardtest.NewHandler(), guardtest.NewHandler()
	subLog.FailWhen(plainEntries)
	pubLog.FailWhen(plainEntries)
	subMetrics, pubMetrics := &stopping{}, &stopping{}
	var notified atomic.Int32
	sub, err := NewSubscriber(SubscriberConfig{Broker: login, Topics: []string{"events/+"}, Logger: subLog.Logger(), Metrics: subMetrics,
		Notify: func(context.Context, string, openadr.Notification) {
			notified.Add(1)
			guardtest.Stop()
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	subLog.WaitFailures(t, "Metrics.OnConnected", 1)
	pub, err := NewPublisher(PublisherConfig{Broker: login, Logger: pubLog.Logger(), Metrics: pubMetrics})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pub.Close(ctx) }()

	// Both notifications arrive: the first Notify did not stop the second.
	pub.Publish("events/create", []byte(eventNotification))
	pub.Publish("events/create", []byte(eventNotification))
	subLog.WaitFailures(t, "Notify", 2)
	subLog.WaitFailures(t, "Metrics.OnReceived", 2)
	pubLog.WaitFailures(t, "Metrics.OnConnected", 1)
	pubLog.WaitFailures(t, "Metrics.OnPublished", 2)
	if n := notified.Load(); n != 2 {
		t.Errorf("Notify was called %d times", n)
	}
	// Not a notification: logged, through a handler that does not return.
	pub.Publish("events/create", []byte("noise"))
	subLog.WaitFailures(t, "Logger", 1)

	// Both lose the connection, and come back.
	broker.DropClients()
	subLog.WaitFailures(t, "Metrics.OnDisconnected", 1)
	subLog.WaitFailures(t, "Metrics.OnConnected", 2)
	pub.Publish("events/create", []byte(eventNotification))
	pubLog.WaitFailures(t, "Metrics.OnDisconnected", 1)
	pubLog.WaitFailures(t, "Metrics.OnPublished", 3)
	subLog.WaitFailures(t, "Notify", 3)

	// Without a broker, the queue fills up.
	dropLog := guardtest.NewHandler()
	dropLog.FailWhen(plainEntries)
	dropMetrics := &stopping{}
	lost, err := NewPublisher(PublisherConfig{Broker: nowhere, Queue: 1, Logger: dropLog.Logger(), Metrics: dropMetrics})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		lost.Publish("events/create", []byte(eventNotification))
	}
	dropLog.WaitFailures(t, "Metrics.OnDropped", 1)
	dropLog.WaitFailures(t, "Logger", 2) // cannot connect, queue full
	expired, stop := context.WithCancel(ctx)
	stop()
	_ = lost.Close(expired)
}
