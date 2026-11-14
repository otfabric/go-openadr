// SPDX-License-Identifier: MIT

package server_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
	"github.com/otfabric/go-openadr/internal/ws"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
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

// L1, L2: Close of a VTN with an Outbox and WebSockets from several
// goroutines at once, and again after.
func TestServerCloseFromSeveralGoroutines(t *testing.T) {
	for range 200 {
		vtn, err := server.New(server.Config{Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token",
			Webhooks: &confirming{}, Outbox: storage.NewMemory(), WebSocket: &server.WebSocketConfig{}})
		if err != nil {
			t.Fatal(err)
		}
		atOnce(8, func() { _ = vtn.Close() })
		if err := vtn.Close(); err != nil {
			t.Errorf("Close again: %v", err)
		}
	}
}

// armed makes one call of the application's code not return normally:
// the next of the given name for which when, if any, reports true.
type armed struct {
	mu   sync.Mutex
	name string
	when func() bool
}

func (a *armed) arm(name string, when func() bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.name, a.when = name, when
}

func (a *armed) check(name string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	hit := a.name == name && (a.when == nil || a.when())
	if hit {
		a.name = ""
	}
	a.mu.Unlock()
	if hit {
		guardtest.Stop()
	}
}

// calledFrom reports whether the calling goroutine runs fn, a function
// named by the end of its full name.
func calledFrom(fn string) bool {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	for {
		f, more := frames.Next()
		if strings.HasSuffix(f.Function, fn) {
			return true
		}
		if !more {
			return false
		}
	}
}

// confirming is a ConfirmingSender that delivers at once, and sends the
// body of each delivery on sent once the VTN has been told and has done
// what it does then: who reads a body there finds the VTN through with it.
type confirming struct {
	a    *armed
	sent chan string
}

func (*confirming) Verify(context.Context, string) error { return nil }
func (c *confirming) Send(_, _ string, body []byte)      { c.SendConfirmed("", "", body, func(error) {}) }
func (c *confirming) SendConfirmed(_, _ string, body []byte, done func(error)) {
	c.a.check("SendConfirmed")
	done(nil)
	if c.sent != nil {
		c.sent <- string(body)
	}
}

// outbox and store are storage.Memory with the calls that a can stop.
type outbox struct {
	storage.Outbox
	a *armed
}

func (o outbox) RemoveDelivery(ctx context.Context, id string) error {
	o.a.check("RemoveDelivery")
	return o.Outbox.RemoveDelivery(ctx, id)
}

func (o outbox) Deliveries(ctx context.Context) ([]storage.Delivery, error) {
	o.a.check("Deliveries")
	return o.Outbox.Deliveries(ctx)
}

type store struct {
	storage.Store
	a *armed
}

func (s store) Subscriptions() storage.Collection[storage.SubscriptionRecord, storage.SubscriptionFilter] {
	return subscriptions{s.Store.Subscriptions(), s.a}
}

type subscriptions struct {
	storage.Collection[storage.SubscriptionRecord, storage.SubscriptionFilter]
	a *armed
}

func (c subscriptions) Get(ctx context.Context, id string) (storage.SubscriptionRecord, error) {
	c.a.check("Get")
	return c.Collection.Get(ctx, id)
}

// What the outbox calls on its way, and does not return normally, is
// logged, and the notification is delivered after all.
func TestOutboxCallsThatDoNotReturnNormally(t *testing.T) {
	a := &armed{}
	mem := storage.NewMemory()
	h := guardtest.NewHandler()
	sender := &confirming{a: a, sent: make(chan string, 64)}
	vtn, err := server.New(server.Config{Store: store{mem, a}, Validator: tokens, TokenURL: "https://idp.example/token",
		Webhooks: sender, Outbox: outbox{mem, a}, RedeliverEvery: 10 * time.Millisecond, Logger: h.Logger(),
		Now: func() time.Time { a.check("Now"); return time.Now() }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vtn.Close() }()
	program := call(vtn, "POST", "/programs", "bl", `{"programName":"p"}`)
	programID, _ := program.json["id"].(string)
	sub := `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"https://example.com/hook"}]}`
	if r := call(vtn, "POST", "/subscriptions", "bl", sub); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	delivered := func(name string, times int) {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for n := 0; n < times; {
			select {
			case body := <-sender.sent:
				if strings.Contains(body, `"`+name+`"`) {
					n++
				}
			case <-timer.C:
				t.Fatalf("%s was not delivered %d times", name, times)
			}
		}
	}

	inNext := func() bool { return calledFrom("server.(*outbox).next") && !calledFrom("server.(*outbox).confirmed") }
	inConfirmed := func() bool { return calledFrom("server.(*outbox).confirmed") }
	reported := map[string]int{}
	for i, site := range []struct {
		arm, callback string
		when          func() bool
		// times is how often the event is delivered.
		times int
	}{
		{"SendConfirmed", "ConfirmingSender.SendConfirmed", nil, 1},
		{"Deliveries", "Outbox.Deliveries", inNext, 1},
		{"Get", "Store.Subscriptions", inNext, 1},
		{"Now", "Config.Now", inNext, 1},
		// Delivered, and not forgotten: delivered again.
		{"RemoveDelivery", "Outbox.RemoveDelivery", inConfirmed, 2},
	} {
		a.arm(site.arm, site.when)
		name := fmt.Sprintf("event-%d", i)
		if r := call(vtn, "POST", "/events", "bl", `{"programID":"`+programID+`","eventName":"`+name+`"}`); r.status != 201 {
			t.Fatalf("%s: %d %s", site.callback, r.status, r.body)
		}
		reported[site.callback]++
		h.WaitFailures(t, site.callback, reported[site.callback])
		delivered(name, site.times)
	}

	// The loop's own reading of the outbox.
	a.arm("Deliveries", func() bool { return calledFrom("server.(*outbox).run") })
	h.WaitFailures(t, "Outbox.Deliveries", 2)

	// A notification whose subscription is gone, given up and not
	// forgotten: given up again.
	a.arm("RemoveDelivery", inNext)
	if err := mem.AddDelivery(context.Background(), storage.Delivery{ID: "d-gone", SubscriptionID: "gone",
		CallbackURL: "https://example.com/gone", Body: []byte(`{}`), Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h.WaitFailures(t, "Outbox.RemoveDelivery", 2)
	h.Wait(t, "the notification given up twice", func(entries []guardtest.Entry) bool {
		n := 0
		for _, e := range entries {
			if strings.Contains(e.Message, "subscription of a notification is gone") {
				n++
			}
		}
		return n >= 2
	})
}

// stopping is Metrics, NotificationMetrics and SocketMetrics whose every
// callback does not return normally.
type stopping struct{}

func (stopping) OnRequest(string)                                             { guardtest.Stop() }
func (stopping) OnResponse(string, int, time.Duration)                        { guardtest.Stop() }
func (stopping) OnNotification(string, openadr.ObjectType, openadr.Operation) { guardtest.Stop() }
func (stopping) OnSocketOpened()                                              { guardtest.Stop() }
func (stopping) OnSocketClosed(string)                                        { guardtest.Stop() }
func plainEntries(e guardtest.Entry) bool                                     { return !strings.HasSuffix(e.Message, guardtest.Message) }

// Metrics callbacks and a Logger's handler that do not return normally are
// logged, and the requests and the WebSockets go on.
func TestServerCallbacksThatDoNotReturnNormally(t *testing.T) {
	h := guardtest.NewHandler()
	h.FailWhen(plainEntries)
	vtn, err := server.New(server.Config{Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token",
		Webhooks: fakeWebhooks{}, WebSocket: &server.WebSocketConfig{}, Metrics: stopping{}, Logger: h.Logger()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vtn.Close() }()

	program := call(vtn, "POST", "/programs", "bl", `{"programName":"p"}`)
	if program.status != 201 {
		t.Fatalf("%d %s", program.status, program.body)
	}
	programID, _ := program.json["id"].(string)
	for callback, n := range map[string]int{"Metrics.OnRequest": 1, "Metrics.OnResponse": 1, "Logger": 1} {
		if got := h.Failures(callback); got != n {
			t.Errorf("%s reported %d times, want %d", callback, got, n)
		}
	}
	sub := `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"https://example.com/hook"},{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET"}]}`
	if r := call(vtn, "POST", "/subscriptions", "bl", sub); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}

	ts := httptest.NewServer(vtn)
	defer ts.Close()
	conn, err := ws.Dial(context.Background(), nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer bl"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := h.Failures("SocketMetrics.OnSocketOpened"); n != 1 {
		t.Errorf("OnSocketOpened reported %d times", n)
	}
	if r := call(vtn, "POST", "/events", "bl", `{"programID":"`+programID+`","eventName":"e"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// One for the webhook, one for the WebSocket.
	if n := h.Failures("NotificationMetrics.OnNotification"); n != 2 {
		t.Errorf("OnNotification reported %d times", n)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), `"eventName":"e"`) {
		t.Errorf("the WebSocket was sent %s, %v", msg, err)
	}
	_ = conn.Close(ws.CloseNormal, "")
	h.WaitFailures(t, "SocketMetrics.OnSocketClosed", 1)
}

// stopVerify is a WebhookSender whose Verify does not return normally.
type stopVerify struct{}

func (stopVerify) Verify(context.Context, string) error { guardtest.Stop(); return nil }
func (stopVerify) Send(string, string, []byte)          {}

// A Verify that does not return normally, on a goroutine of the VTN, is
// logged, and the URL is refused as one that could not be verified.
func TestVerifyThatDoesNotReturnNormally(t *testing.T) {
	h := guardtest.NewHandler()
	vtn := newServer(t, server.Config{Webhooks: stopVerify{}, Logger: h.Logger()})
	sub := `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"https://example.com/hook"}]}`
	r := call(vtn, "POST", "/subscriptions", "bl", sub)
	if r.status != 400 || !strings.Contains(r.body, "could not be verified") {
		t.Errorf("%d %s", r.status, r.body)
	}
	if n := h.Failures("WebhookSender.Verify"); n != 1 {
		t.Errorf("Verify reported %d times", n)
	}
}

// slowNotifications is Metrics whose OnNotification for a WebSocket blocks,
// the first time, until released.
type slowNotifications struct {
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (*slowNotifications) OnRequest(string)                      {}
func (*slowNotifications) OnResponse(string, int, time.Duration) {}
func (m *slowNotifications) OnNotification(mechanism string, _ openadr.ObjectType, _ openadr.Operation) {
	if mechanism == server.MechanismWebSocket && m.first.CompareAndSwap(false, true) {
		close(m.entered)
		<-m.release
	}
}

// An OnNotification that blocks holds up the request it is called for, and
// neither the WebSockets that open meanwhile nor the notifications of other
// requests.
func TestNotificationHookDoesNotHoldUpOthers(t *testing.T) {
	m := &slowNotifications{entered: make(chan struct{}), release: make(chan struct{})}
	vtn := newServer(t, server.Config{WebSocket: &server.WebSocketConfig{}, Metrics: m})
	defer func() { _ = vtn.(*server.Server).Close() }()
	program := call(vtn, "POST", "/programs", "bl", `{"programName":"p"}`)
	programID, _ := program.json["id"].(string)
	sub := `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET"}]}`
	if r := call(vtn, "POST", "/subscriptions", "bl", sub); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	ts := httptest.NewServer(vtn)
	defer ts.Close()
	// Released at the end however the test ends, before the server closes.
	release := sync.OnceFunc(func() { close(m.release) })
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := ws.Dial(ctx, nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer bl"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ws.CloseNormal, "") }()

	first := make(chan struct{})
	go func() {
		defer close(first)
		call(vtn, "POST", "/events", "bl", `{"programID":"`+programID+`","eventName":"first"}`)
	}()
	within(t, "OnNotification", m.entered)

	opened := make(chan struct{})
	go func() {
		defer close(opened)
		other, err := ws.Dial(ctx, nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer ven"}})
		if err != nil {
			t.Errorf("a WebSocket opened meanwhile: %v", err)
			return
		}
		_ = other.Close(ws.CloseNormal, "")
	}()
	within(t, "a WebSocket opened meanwhile", opened)

	second := make(chan struct{})
	go func() {
		defer close(second)
		call(vtn, "POST", "/events", "bl", `{"programID":"`+programID+`","eventName":"second"}`)
	}()
	within(t, "a second event", second)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), `"eventName":"second"`) {
		t.Errorf("while the first waits, the WebSocket was sent %s, %v", msg, err)
	}

	release()
	<-first
	if msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), `"eventName":"first"`) {
		t.Errorf("after the first, the WebSocket was sent %s, %v", msg, err)
	}
}

// opening is SocketMetrics whose first OnSocketOpened waits until released:
// the connection is the VTN's by then, and the client has no answer yet.
type opening struct {
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (*opening) OnRequest(string)                      {}
func (*opening) OnResponse(string, int, time.Duration) {}
func (*opening) OnSocketClosed(string)                 {}
func (m *opening) OnSocketOpened() {
	if m.first.CompareAndSwap(false, true) {
		close(m.entered)
		<-m.release
	}
}

// L5: a VTN closed while a WebSocket is being opened. Close waits for the
// request that opens it, the client is told that the VTN goes away, and no
// WebSocket opens after.
func TestServerCloseWhileAWebSocketOpens(t *testing.T) {
	m := &opening{entered: make(chan struct{}), release: make(chan struct{})}
	vtn := newServer(t, server.Config{WebSocket: &server.WebSocketConfig{}, Metrics: m}).(*server.Server)
	ts := httptest.NewServer(vtn)
	defer ts.Close()
	// Released however the test ends, before the HTTP server closes.
	release := sync.OnceFunc(func() { close(m.release) })
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type dialed struct {
		conn *ws.Conn
		err  error
	}
	opened := make(chan dialed, 1)
	go func() {
		conn, err := ws.Dial(ctx, nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer bl"}})
		opened <- dialed{conn, err}
	}()
	within(t, "OnSocketOpened", m.entered)

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = vtn.Close()
	}()
	// Close has begun once another WebSocket is refused with 503. Until
	// then the request is refused for what it lacks, and keeps no place.
	another := func() int {
		req := httptest.NewRequest("GET", "/notifiers/ws", nil)
		req.Header.Set("Authorization", "Bearer ven")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		rec := httptest.NewRecorder()
		vtn.ServeHTTP(rec, req)
		return rec.Code
	}
	for another() != http.StatusServiceUnavailable {
		if ctx.Err() != nil {
			t.Fatal("Close did not begin")
		}
		runtime.Gosched()
	}
	select {
	case <-closed:
		t.Fatal("Close returned while a WebSocket was being opened")
	default:
	}

	release()
	var d dialed
	select {
	case d = <-opened:
	case <-ctx.Done():
		t.Fatal("the WebSocket was not answered")
	}
	if d.err != nil {
		t.Fatalf("the WebSocket that was being opened: %v", d.err)
	}
	defer func() { _ = d.conn.Close(ws.CloseNormal, "") }()
	d.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var bye *ws.CloseError
	if _, err := d.conn.ReadMessage(); !errors.As(err, &bye) || bye.Code != ws.CloseGoingAway {
		t.Errorf("the WebSocket that was being opened ended with %v, want a close with %d", err, ws.CloseGoingAway)
	}
	within(t, "Close", closed)
	if code := another(); code != http.StatusServiceUnavailable {
		t.Errorf("a WebSocket after Close was answered %d", code)
	}
}

// reentering is Metrics whose first OnNotification for a WebSocket creates
// another event on the VTN it is called by, and says how that was answered.
type reentering struct {
	first    atomic.Bool
	vtn      http.Handler
	event    string
	answered chan int
}

func (*reentering) OnRequest(string)                      {}
func (*reentering) OnResponse(string, int, time.Duration) {}
func (m *reentering) OnNotification(mechanism string, _ openadr.ObjectType, _ openadr.Operation) {
	if mechanism == server.MechanismWebSocket && m.first.CompareAndSwap(false, true) {
		m.answered <- call(m.vtn, "POST", "/events", "bl", m.event).status
	}
}

// L9: a metrics hook that calls the VTN it is called by. The request it
// makes is served, and its notification is on the WebSocket before the one
// the hook was called for, which is queued when the hook has returned.
func TestNotificationHookCallsTheVTN(t *testing.T) {
	m := &reentering{answered: make(chan int, 1)}
	vtn := newServer(t, server.Config{WebSocket: &server.WebSocketConfig{}, Metrics: m})
	// Not after a failure: a hook that waits for the VTN keeps it from closing.
	defer func() {
		if !t.Failed() {
			_ = vtn.(*server.Server).Close()
		}
	}()
	program := call(vtn, "POST", "/programs", "bl", `{"programName":"p"}`)
	programID, _ := program.json["id"].(string)
	sub := `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET"}]}`
	if r := call(vtn, "POST", "/subscriptions", "bl", sub); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	ts := httptest.NewServer(vtn)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := ws.Dial(ctx, nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer bl"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ws.CloseNormal, "") }()
	// Set before the first notification: the hook reads them on the
	// goroutine of the request below.
	m.vtn, m.event = vtn, `{"programID":"`+programID+`","eventName":"second"}`

	first := make(chan int, 1)
	go func() {
		first <- call(vtn, "POST", "/events", "bl", `{"programID":"`+programID+`","eventName":"first"}`).status
	}()
	for what, answered := range map[string]chan int{"the event the hook creates": m.answered, "the event the hook was called for": first} {
		select {
		case status := <-answered:
			if status != 201 {
				t.Errorf("%s was answered %d", what, status)
			}
		case <-ctx.Done():
			t.Fatalf("%s was not answered", what)
		}
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for _, name := range []string{"second", "first"} {
		if msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), `"eventName":"`+name+`"`) {
			t.Errorf("the WebSocket was sent %s, %v; want the event %q", msg, err, name)
		}
	}
}
