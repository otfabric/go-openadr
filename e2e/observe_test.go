// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/websocket"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// tallies is a client's Metrics: how every call ended, by operation.
type tallies struct {
	mu                                  sync.Mutex
	requests, responses, errs, timeouts map[string]int
	attempts                            atomic.Int64
}

func newTallies() *tallies {
	return &tallies{requests: map[string]int{}, responses: map[string]int{}, errs: map[string]int{}, timeouts: map[string]int{}}
}

func (m *tallies) inc(of map[string]int, op string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	of[op]++
}

func (m *tallies) OnRequest(op string)                         { m.inc(m.requests, op) }
func (m *tallies) OnResponse(op string, _ time.Duration)       { m.inc(m.responses, op) }
func (m *tallies) OnError(op string, _ time.Duration, _ error) { m.inc(m.errs, op) }
func (m *tallies) OnTimeout(op string, _ time.Duration)        { m.inc(m.timeouts, op) }
func (m *tallies) OnAttempt(string, int, time.Duration, error) { m.attempts.Add(1) }

func (m *tallies) total(of map[string]int) (n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range of {
		n += v
	}
	return n
}

// A client with a retry policy on a network that cuts a third of its
// connections: its calls succeed without the caller doing anything, where
// repeating them is safe, and a create ends in exactly one object.
func TestChaosRetryPolicy(t *testing.T) {
	seed := chaosSeed(t)
	vtn := start(t, options{})
	proxy := newFlakyProxy(t, strings.TrimPrefix(vtn.Server.URL, "https://"), seed)
	ctx := context.Background()
	metrics := newTallies()
	transport := vtn.HTTP.Transport.(*http.Transport).Clone()
	bl, err := client.NewBL(client.Config{
		BaseURL: proxy.url(vtn), HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second},
		Credentials: &client.Credentials{ClientID: "bl", ClientSecret: "bl-secret", TokenURL: proxy.url(vtn) + "/auth/token"},
		RetryPolicy: client.NewExponentialBackoff(client.ExponentialBackoffConfig{BaseDelay: time.Millisecond, MaxDelay: 20 * time.Millisecond, MaxAttempts: 40, RetryOnTimeout: true}),
		// The caller takes on what a repeated create can do, and looks at
		// the answer: a conflict on a name it chose is its own first try.
		RetryCreates: true,
		Metrics:      metrics,
	})
	if err != nil {
		t.Fatal(err)
	}

	proxy.on.Store(true)
	const writers = 6
	perWriter := rounds(30)
	fail := &failures{t: t}
	var (
		wg        sync.WaitGroup
		meant     atomic.Int64
		conflicts atomic.Int64
		stormOver atomic.Bool
	)
	storm := make(chan struct{})
	go func() {
		defer close(storm)
		for range 5 {
			time.Sleep(40 * time.Millisecond)
			proxy.cutAll()
		}
		stormOver.Store(true)
	}()
	for wkr := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter || !stormOver.Load(); i++ {
				name := fmt.Sprintf("program-%d-%d", wkr, i)
				meant.Add(1)
				p, err := bl.CreateProgram(ctx, openadr.Program{Name: name})
				switch {
				case errors.Is(err, openadr.ErrConflict):
					// The first attempt arrived and its answer did not.
					conflicts.Add(1)
					continue
				case err != nil:
					fail.add("creating %s: %v", name, err)
					continue
				}
				// Reads, updates and deletes need no such care.
				if _, err := bl.Program(ctx, p.ID); err != nil {
					fail.add("reading %s: %v", name, err)
				}
				p.Targets = []string{"t"}
				if _, err := bl.UpdateProgram(ctx, p); err != nil {
					fail.add("updating %s: %v", name, err)
				}
				if i%3 == 0 {
					// A delete that is repeated finds nothing the second time.
					if _, err := bl.DeleteProgram(ctx, p.ID); err != nil && !errors.Is(err, openadr.ErrNotFound) {
						fail.add("deleting %s: %v", name, err)
					}
					meant.Add(-1)
				}
			}
		}()
	}
	wg.Wait()
	<-storm
	proxy.on.Store(false)
	if proxy.cuts.Load() == 0 {
		t.Fatal("no connection was cut")
	}
	consistent(t, vtn.Store)
	all, _ := vtn.Store.Programs().List(ctx, storage.ProgramFilter{})
	if int64(len(all)) != meant.Load() {
		t.Errorf("%d programs exist, %d were meant to", len(all), meant.Load())
	}

	// The metrics add up: every call that began ended, one way.
	requests := metrics.total(metrics.requests)
	ended := metrics.total(metrics.responses) + metrics.total(metrics.errs) + metrics.total(metrics.timeouts)
	if requests == 0 || requests != ended {
		t.Errorf("%d calls began and %d ended", requests, ended)
	}
	if retried := metrics.attempts.Load() - int64(requests); retried <= 0 {
		t.Errorf("%d attempts for %d calls: nothing was retried", metrics.attempts.Load(), requests)
	}
	metrics.mu.Lock()
	for op := range metrics.requests {
		if strings.Count(op, "/") > 2 || strings.ContainsAny(op, "0123456789") {
			t.Errorf("an operation is named %q: not fit to be a label", op)
		}
	}
	metrics.mu.Unlock()
	t.Logf("%d connections cut; %d calls in %d attempts; %d creates answered with a conflict for their own first attempt",
		proxy.cuts.Load(), requests, metrics.attempts.Load(), conflicts.Load())
}

// socketTallies is the Metrics of a WebSocket subscriber and of the VTN's
// side of it.
type socketTallies struct {
	connected, disconnected, received atomic.Int64
	opened, closed                    atomic.Int64
	notifications                     atomic.Int64
}

func (m *socketTallies) OnConnected()         { m.connected.Add(1) }
func (m *socketTallies) OnDisconnected(error) { m.disconnected.Add(1) }
func (m *socketTallies) OnReceived()          { m.received.Add(1) }

// And as a server's metrics.
type vtnTallies struct{ *socketTallies }

func (vtnTallies) OnRequest(string)                      {}
func (vtnTallies) OnResponse(string, int, time.Duration) {}
func (m vtnTallies) OnSocketOpened()                     { m.opened.Add(1) }
func (m vtnTallies) OnSocketClosed(string)               { m.closed.Add(1) }
func (m vtnTallies) OnNotification(mechanism string, _ openadr.ObjectType, _ openadr.Operation) {
	if mechanism == server.MechanismWebSocket {
		m.notifications.Add(1)
	}
}

// Both ends of a WebSocket report what they see, and they see the same.
func TestWebSocketMetrics(t *testing.T) {
	ctx := context.Background()
	m := &socketTallies{}
	vtn := start(t, options{websocket: &server.WebSocketConfig{}, metrics: vtnTallies{m}})
	if _, err := vtn.VEN1.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-1", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
		t.Fatal(err)
	}
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.VEN1.NotificationStream, Metrics: m,
		Notify: func(context.Context, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("program-%d", i)})
	}
	eventually(t, "five notifications were sent and received", func() bool {
		return m.notifications.Load() == 5 && m.received.Load() == 5
	})
	if m.connected.Load() != 1 || m.opened.Load() != 1 || m.disconnected.Load() != 0 || m.closed.Load() != 0 {
		t.Errorf("connected %d, opened %d, disconnected %d, closed %d", m.connected.Load(), m.opened.Load(), m.disconnected.Load(), m.closed.Load())
	}
	_ = sub.Close()
	eventually(t, "both ends reported the end of the WebSocket", func() bool {
		return m.disconnected.Load() == 1 && m.closed.Load() == 1
	})
}

// What a constructor refuses, and what is used after it was closed, match
// the sentinels for it, in every package.
func TestSentinelsAcrossPackages(t *testing.T) {
	if _, err := client.NewVEN(client.Config{}); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("a client without a URL: %v", err)
	}
	if _, err := client.NewBL(client.Config{BaseURL: "ftp://x"}); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("a client with a URL that is not HTTP: %v", err)
	}
	if _, err := websocket.NewSubscriber(websocket.SubscriberConfig{}); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("a subscriber without Dial: %v", err)
	}
	vtn := start(t, options{websocket: &server.WebSocketConfig{}})
	stream, err := vtn.VEN1.NotificationStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if _, err := stream.Next(); !errors.Is(err, openadr.ErrClosed) {
		t.Errorf("Next on a closed stream: %v", err)
	}
	// An answer the client cannot read is the VTN's fault, and says so.
	broken := newPlainServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"not":"a list"}`)) }))
	lost, err := client.NewVEN(client.Config{BaseURL: broken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lost.Programs(context.Background(), client.ProgramQuery{}); !errors.Is(err, openadr.ErrProtocol) {
		t.Errorf("an answer that is not a list: %v", err)
	}
	// A VTN that has an operation not: 501, which is also a server error.
	plain := start(t, options{noWebhooks: true})
	_, err = plain.VEN1.Subscriptions(context.Background(), client.SubscriptionQuery{})
	if !errors.Is(err, openadr.ErrNotImplemented) || !errors.Is(err, openadr.ErrServer) || errors.Is(err, openadr.ErrUnavailable) {
		t.Errorf("subscriptions on a VTN that cannot notify: %v", err)
	}
	// Credentials the token service does not know.
	stranger, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP, Credentials: &client.Credentials{ClientID: "nobody", ClientSecret: "nothing"}})
	if err != nil {
		t.Fatal(err)
	}
	var tokenErr *client.TokenError
	if _, err := stranger.Programs(context.Background(), client.ProgramQuery{}); !errors.As(err, &tokenErr) || !errors.Is(err, openadr.ErrUnauthorized) {
		t.Errorf("unknown credentials: %v", err)
	}
}

// Metrics must not lag behind what a client has seen: when a client knows
// its WebSocket is open, the VTN has counted it; when it has read a
// notification, the VTN has counted that; when a subscriber says it is
// ready, it has reported being connected. Many clients at once, opening,
// reading and closing, and each checks after every step.
//
// This is not a matter of taste. A gauge that is read right after the
// event it is to show, by a test or by an alert, and does not show it yet,
// is a gauge that is wrong some of the time.
func TestMetricsAreNotBehind(t *testing.T) {
	ctx := context.Background()
	m := &socketTallies{}
	vtn := start(t, options{websocket: &server.WebSocketConfig{MaxPerClient: 64, Queue: 4096}, metrics: vtnTallies{m}})
	if _, err := vtn.VEN1.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-1", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
		t.Fatal(err)
	}
	fail := &failures{t: t}
	const clients = 16
	rounds := rounds(25)
	var (
		wg           sync.WaitGroup
		seenOpen     atomic.Int64 // WebSockets a client has seen open
		seenReceived atomic.Int64 // notifications a client has read
		created      atomic.Int64
	)
	for c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				stream, err := vtn.VEN1.NotificationStream(ctx)
				if err != nil {
					fail.add("opening a WebSocket: %v", err)
					return
				}
				// The client knows it is open: so does the VTN.
				if seen, counted := seenOpen.Add(1), m.opened.Load(); counted < seen {
					fail.add("%d WebSockets were seen open by clients, and the VTN had counted %d", seen, counted)
				}
				// Something to be told of, and the reading of it.
				if _, err := vtn.BL.CreateProgram(ctx, openadr.Program{Name: fmt.Sprintf("program-%d-%d", c, i)}); err != nil {
					fail.add("creating a program: %v", err)
				}
				created.Add(1)
				if _, err := stream.Next(); err != nil {
					fail.add("reading a notification: %v", err)
				} else if seen, counted := seenReceived.Add(1), m.notifications.Load(); counted < seen {
					fail.add("%d notifications were read by clients, and the VTN had counted %d", seen, counted)
				}
				_ = stream.Close()
			}
		}()
	}
	wg.Wait()
	if m.opened.Load() != int64(clients*rounds) {
		t.Errorf("%d WebSockets were opened, and %d counted", clients*rounds, m.opened.Load())
	}
	// Every one that was opened is reported closed, once the VTN has
	// noticed, and none twice.
	eventually(t, "every WebSocket was reported closed", func() bool { return m.closed.Load() == m.opened.Load() })
	time.Sleep(50 * time.Millisecond)
	if m.closed.Load() != m.opened.Load() {
		t.Errorf("%d opened, %d closed", m.opened.Load(), m.closed.Load())
	}

	// The subscriber's side: ready means reported.
	for range rounds {
		sm := &socketTallies{}
		sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.VEN1.NotificationStream, Metrics: sm,
			Notify: func(context.Context, openadr.Notification) {}})
		if err != nil {
			t.Fatal(err)
		}
		if err := sub.Ready(ctx); err != nil {
			t.Fatal(err)
		}
		if sm.connected.Load() != 1 {
			t.Fatalf("a subscriber was ready and had reported %d connections", sm.connected.Load())
		}
		_ = sub.Close()
		if sm.disconnected.Load() != 1 {
			t.Fatalf("a subscriber was closed and had reported %d disconnections", sm.disconnected.Load())
		}
	}
}
