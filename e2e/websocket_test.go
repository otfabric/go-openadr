// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/internal/ws"
	"github.com/otfabric/go-openadr/notification/websocket"
	"github.com/otfabric/go-openadr/server"
)

// listener is a client that receives notifications over a WebSocket.
type listener struct {
	received chan openadr.Notification
	sub      *websocket.Subscriber
}

// listen opens the WebSocket of a client and waits until it is connected.
func listen(t *testing.T, dial func(context.Context) (*client.NotificationStream, error)) *listener {
	t.Helper()
	l := &listener{received: make(chan openadr.Notification, 256)}
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial:      dial,
		Notify:    func(_ context.Context, n openadr.Notification) { l.received <- n },
		KeepAlive: 200 * time.Millisecond,
		Backoff:   20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	l.sub = sub
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sub.Ready(ctx); err != nil {
		t.Fatalf("the WebSocket did not open: %v", err)
	}
	return l
}

func (l *listener) next(t *testing.T) openadr.Notification {
	t.Helper()
	select {
	case n := <-l.received:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no notification arrived")
		return openadr.Notification{}
	}
}

func (l *listener) quiet(t *testing.T) {
	t.Helper()
	select {
	case n := <-l.received:
		t.Fatalf("unexpected notification: %s %s %+v", n.Operation, n.ObjectType, n.Object)
	case <-time.After(150 * time.Millisecond):
	}
}

// bySocket asks for every operation on the given kinds of object, over the
// WebSocket.
func bySocket(objects ...openadr.ObjectType) []openadr.ObjectOperation {
	return []openadr.ObjectOperation{{
		Objects:    objects,
		Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete},
		Mechanism:  openadr.MechanismWebSocket,
	}}
}

// eventually waits for a condition that another goroutine brings about.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("did not happen: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// What a client subscribes to with the mechanism WEBSOCKET, it is sent on
// its WebSocket: its own view of it, as by any other way of notifying.
func TestWebSocketNotifications(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{websocket: &server.WebSocketConfig{}})
	if n, err := vtn.VEN1.Notifiers(ctx); err != nil || !n.WebSocket || !n.Webhook {
		t.Fatalf("notifiers: %+v, %v", n, err)
	}
	if _, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}

	sub1, err := vtn.VEN1.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-1",
		ObjectOperations: bySocket(openadr.ObjectProgram, openadr.ObjectEvent, openadr.ObjectReport), Targets: []string{"group-a", "group-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sub1.ObjectOperations[0]; got.Mechanism != openadr.MechanismWebSocket || got.CallbackURL != "" {
		t.Errorf("the subscription came back as %+v", got)
	}
	if _, err := vtn.VEN2.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-2",
		ObjectOperations: bySocket(openadr.ObjectProgram, openadr.ObjectEvent, openadr.ObjectReport), Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.BL.CreateSubscription(ctx, openadr.Subscription{ClientName: "bl", ObjectOperations: bySocket(openadr.ObjectReport, openadr.ObjectVEN)}); err != nil {
		t.Fatal(err)
	}
	ven1, ven2, bl := listen(t, vtn.VEN1.NotificationStream), listen(t, vtn.VEN2.NotificationStream), listen(t, vtn.BL.NotificationStream)

	// A targeted program: the VEN that was granted the target is told, and
	// shown that target only.
	program := mustProgram(t, vtn.BL, openadr.Program{Name: "flex", Targets: []string{"group-a", "group-c"}})
	n := ven1.next(t)
	got, isProgram := n.Object.(openadr.Program)
	if n.ObjectType != openadr.ObjectProgram || n.Operation != openadr.OperationCreate || !isProgram || got.ID != program.ID {
		t.Fatalf("ven-1 was told %+v", n)
	}
	if len(got.Targets) != 1 || got.Targets[0] != "group-a" || len(n.Targets) != 1 {
		t.Errorf("ven-1 was shown the targets %v and %v", got.Targets, n.Targets)
	}
	ven2.quiet(t)
	bl.quiet(t) // it did not subscribe to programs

	// Events: created, updated, deleted, in that order.
	event := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "now", Targets: []string{"group-a"}})
	event.Name = "later"
	if _, err := vtn.BL.UpdateEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	for _, want := range []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate} {
		if n := ven1.next(t); n.Operation != want || n.ObjectType != openadr.ObjectEvent {
			t.Errorf("ven-1 was told %s %s, want %s EVENT", n.Operation, n.ObjectType, want)
		}
	}

	// A report: its VEN and Business Logic are told, another VEN is not.
	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "meter", Resources: []openadr.ReportResource{}})
	if err != nil {
		t.Fatal(err)
	}
	for who, l := range map[string]*listener{"ven-1": ven1, "Business Logic": bl} {
		n := l.next(t)
		if got, ok := n.Object.(openadr.Report); !ok || got.ID != report.ID || n.Operation != openadr.OperationCreate {
			t.Errorf("%s was told %+v", who, n)
		}
	}
	if _, err := vtn.BL.DeleteEvent(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	if n := ven1.next(t); n.Operation != openadr.OperationDelete || n.ObjectType != openadr.ObjectEvent {
		t.Errorf("ven-1 was told %s %s", n.Operation, n.ObjectType)
	}
	ven2.quiet(t)

	// A subscription that is deleted notifies no more.
	if _, err := vtn.VEN1.DeleteSubscription(ctx, sub1.ID); err != nil {
		t.Fatal(err)
	}
	mustProgram(t, vtn.BL, openadr.Program{Name: "more", Targets: []string{"group-a"}})
	ven1.quiet(t)
}

// One subscription may notify both ways, and an entry that is named twice
// still notifies once.
func TestWebSocketAndWebhookTogether(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{websocket: &server.WebSocketConfig{}})
	hook := newHook(t, "tok")
	both := append(bySocket(openadr.ObjectProgram), bySocket(openadr.ObjectProgram, openadr.ObjectEvent)...)
	both = append(both, openadr.ObjectOperation{Objects: []openadr.ObjectType{openadr.ObjectProgram}, Operations: []openadr.Operation{openadr.OperationCreate},
		Mechanism: openadr.MechanismWebhook, CallbackURL: hook.URL, BearerToken: "tok"})
	sub, err := vtn.VEN1.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-1", ObjectOperations: both})
	if err != nil {
		t.Fatal(err)
	}
	// WEBHOOK is what the empty mechanism means, and reads back as that.
	if sub.ObjectOperations[2].Mechanism != "" || sub.ObjectOperations[0].Mechanism != openadr.MechanismWebSocket {
		t.Errorf("mechanisms came back as %q and %q", sub.ObjectOperations[0].Mechanism, sub.ObjectOperations[2].Mechanism)
	}
	socket := listen(t, vtn.VEN1.NotificationStream)

	program := mustProgram(t, vtn.BL, openadr.Program{Name: "tariff"})
	for what, n := range map[string]openadr.Notification{"the webhook": hook.next(t), "the WebSocket": socket.next(t)} {
		if got, ok := n.Object.(openadr.Program); !ok || got.ID != program.ID {
			t.Errorf("%s was told %+v", what, n)
		}
	}
	socket.quiet(t)
	hook.quiet(t)

	// A client may listen from more than one place: each is told.
	second := listen(t, vtn.VEN1.NotificationStream)
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID})
	socket.next(t)
	second.next(t)
	hook.quiet(t)
}

// A VTN says which mechanisms it has, and refuses a subscription to one it
// has not.
func TestWebSocketMechanisms(t *testing.T) {
	ctx := context.Background()
	socketSub := openadr.Subscription{ClientName: "c", ObjectOperations: bySocket(openadr.ObjectEvent)}
	hook := newHook(t, "")
	hookSub := openadr.Subscription{ClientName: "c", ObjectOperations: all(openadr.ObjectEvent)}
	hookSub.ObjectOperations[0].CallbackURL = hook.URL

	t.Run("WebSockets only", func(t *testing.T) {
		vtn := start(t, options{websocket: &server.WebSocketConfig{}, noWebhooks: true})
		if n, err := vtn.VEN1.Notifiers(ctx); err != nil || !n.WebSocket || n.Webhook || n.MQTT != nil {
			t.Errorf("notifiers: %+v, %v", n, err)
		}
		if _, err := vtn.VEN1.CreateSubscription(ctx, socketSub); err != nil {
			t.Errorf("a WebSocket subscription: %v", err)
		}
		var apiErr *openadr.Error
		if _, err := vtn.VEN1.CreateSubscription(ctx, hookSub); !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || !strings.Contains(apiErr.Problem.Detail, "does not notify by webhook") {
			t.Errorf("a webhook subscription: %v", err)
		}
	})

	t.Run("webhooks only", func(t *testing.T) {
		vtn := start(t, options{})
		if n, err := vtn.VEN1.Notifiers(ctx); err != nil || n.WebSocket || !n.Webhook {
			t.Errorf("notifiers: %+v, %v", n, err)
		}
		var apiErr *openadr.Error
		if _, err := vtn.VEN1.CreateSubscription(ctx, socketSub); !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || !strings.Contains(apiErr.Problem.Detail, "does not notify by WebSocket") {
			t.Errorf("a WebSocket subscription: %v", err)
		}
		// And there is no WebSocket to open.
		if _, err := vtn.VEN1.NotificationStream(ctx); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotImplemented {
			t.Errorf("opening a WebSocket: %v", err)
		}
		// The mechanism of a subscription can be changed, where there is one to change to.
		sub, err := vtn.VEN1.CreateSubscription(ctx, hookSub)
		if err != nil {
			t.Fatal(err)
		}
		sub.ObjectOperations = bySocket(openadr.ObjectEvent)
		if _, err := vtn.VEN1.UpdateSubscription(ctx, sub); !errors.Is(err, openadr.ErrBadRequest) {
			t.Errorf("changing to a mechanism the VTN has not: %v", err)
		}
	})

	t.Run("what a client may not send", func(t *testing.T) {
		vtn := start(t, options{websocket: &server.WebSocketConfig{}})
		token := vtn.token(t, "ven-1")
		for what, oo := range map[string]string{
			"an unknown mechanism":            `{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"PIGEON","callbackUrl":"https://example.com/"}`,
			"a WebSocket with a callback URL": `{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET","callbackUrl":"https://example.com/"}`,
			"a webhook without one":           `{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBHOOK"}`,
		} {
			if status, _, body := vtn.do(t, "POST", "/subscriptions", token, `{"clientName":"c","objectOperations":[`+oo+`]}`); status != 400 {
				t.Errorf("%s: %d %s", what, status, body)
			}
		}
		bad := openadr.Subscription{ClientName: "c", ObjectOperations: []openadr.ObjectOperation{{Mechanism: "PIGEON", CallbackURL: "https://example.com/"}}}
		var invalid *openadr.ValidationError
		if _, err := vtn.VEN1.CreateSubscription(ctx, bad); !errors.As(err, &invalid) || !strings.Contains(invalid.Field, "mechanism") {
			t.Errorf("the client sent an unknown mechanism: %v", err)
		}
	})
}

func TestWebSocketAccess(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{websocket: &server.WebSocketConfig{MaxPerClient: 2}, anonymous: true})
	open := func(token string) (*ws.Conn, error) {
		header := http.Header{}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
		return ws.Dial(ctx, vtn.HTTP, vtn.URL+"/notifiers/ws", header)
	}
	var refused *ws.HandshakeError
	// Not without a token, even where anyone may read: notifications are
	// those of a client.
	if _, err := open(""); !errors.As(err, &refused) || refused.StatusCode != 401 {
		t.Errorf("without a token: %v", err)
	}
	if _, err := open("forged"); !errors.As(err, &refused) || refused.StatusCode != 401 {
		t.Errorf("with a forged token: %v", err)
	}
	// A client with a token source is given a fresh one when the VTN no
	// longer takes the one it has.
	stale, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP, Tokens: &rotating{tokens: []string{"expired", vtn.token(t, "ven-2")}}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := stale.NotificationStream(ctx)
	if err != nil {
		t.Fatalf("with a token that had to be renewed: %v", err)
	}
	_ = stream.Close()

	// So many at a time per client, and no more.
	token := vtn.token(t, "ven-1")
	first, err := open(token)
	if err != nil {
		t.Fatal(err)
	}
	second, err := open(token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(ws.CloseNormal, "") }()
	if _, err := open(token); !errors.As(err, &refused) || refused.StatusCode != 409 || !strings.Contains(string(refused.Body), "already open") {
		t.Errorf("a third WebSocket: %v", err)
	}
	if _, err := vtn.VEN1.NotificationStream(ctx); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("a third WebSocket, by the client: %v", err)
	}
	// Another client is not in the way, and a place that is given up can
	// be taken again.
	other, err := open(vtn.token(t, "ven-2"))
	if err != nil {
		t.Errorf("another client: %v", err)
	} else {
		_ = other.Close(ws.CloseNormal, "")
	}
	_ = first.Close(ws.CloseNormal, "bye")
	eventually(t, "the closed WebSocket made room", func() bool {
		conn, err := open(token)
		if err != nil {
			return false
		}
		_ = conn.Close(ws.CloseNormal, "")
		return true
	})

	// Only GET opens it.
	if status, header, _ := vtn.do(t, "POST", "/notifiers/ws", token, ""); status != 405 || header.Get("Allow") != "GET" {
		t.Errorf("POST: %d, Allow %q", status, header.Get("Allow"))
	}
	// A cancelled context opens nothing.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := vtn.VEN1.NotificationStream(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("with a cancelled context: %v", err)
	}
}

// rotating hands out its tokens one after the other, moving on when one is
// refused.
type rotating struct {
	tokens []string
}

func (r *rotating) Token(context.Context) (string, error) { return r.tokens[0], nil }

func (r *rotating) Invalidate(string) {
	if len(r.tokens) > 1 {
		r.tokens = r.tokens[1:]
	}
}

// A client that does not read is disconnected rather than left behind, and
// a subscriber that is disconnected connects again.
func TestWebSocketSlowClientAndReconnect(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{websocket: &server.WebSocketConfig{Queue: 4}})
	if _, err := vtn.VEN1.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-1", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
		t.Fatal(err)
	}

	block := make(chan struct{})
	received := make(chan openadr.Notification, 1024)
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial: vtn.VEN1.NotificationStream,
		Notify: func(ctx context.Context, n openadr.Notification) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			received <- n
		},
		Backoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	// Far more than the queue holds and the connection buffers, while the
	// client is stuck in its handler.
	big := strings.Repeat("x", 100)
	for i := range 600 {
		descriptions := make([]openadr.ProgramDescription, 50)
		for j := range descriptions {
			descriptions[j].URL = "https://example.com/" + big
		}
		mustProgram(t, vtn.BL, openadr.Program{Name: "program-" + string(rune('a'+i%26)) + big[:i%90] + string(rune('a'+i/26)), Descriptions: descriptions})
	}
	close(block)
	eventually(t, "the subscriber connected again", func() bool { return sub.Sessions() >= 2 })
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sub.Ready(readyCtx); err != nil {
		t.Fatal(err)
	}
	// It missed notifications, which is what being disconnected means...
	missed := 600 - len(received)
	for len(received) > 0 {
		<-received
	}
	if missed <= 0 {
		t.Errorf("a client that did not read was told everything")
	}
	// ...and it is told again from here on.
	created := mustProgram(t, vtn.BL, openadr.Program{Name: "after the reconnect"})
	select {
	case n := <-received:
		if got, ok := n.Object.(openadr.Program); !ok || got.ID != created.ID {
			// Late arrivals from before are possible; look further.
			eventually(t, "the program after the reconnect arrived", func() bool {
				select {
				case n := <-received:
					got, ok := n.Object.(openadr.Program)
					return ok && got.ID == created.ID
				default:
					return false
				}
			})
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived after the reconnect")
	}
}

// A client that has gone without a word is noticed by its silence.
func TestWebSocketDeadClient(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{websocket: &server.WebSocketConfig{PingInterval: 40 * time.Millisecond, MaxPerClient: 1}})
	header := http.Header{"Authorization": {"Bearer " + vtn.token(t, "ven-1")}}
	// Opened, and never read from: the pings of the VTN go unanswered.
	dead, err := ws.Dial(ctx, vtn.HTTP, vtn.URL+"/notifiers/ws", header)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dead.Close(ws.CloseNormal, "") }()
	if _, err := ws.Dial(ctx, vtn.HTTP, vtn.URL+"/notifiers/ws", header); err == nil {
		t.Fatal("a second WebSocket was opened while the first was")
	}
	eventually(t, "the VTN gave the silent client up", func() bool {
		conn, err := ws.Dial(ctx, vtn.HTTP, vtn.URL+"/notifiers/ws", header)
		if err != nil {
			return false
		}
		_ = conn.Close(ws.CloseNormal, "")
		return true
	})

	// A client that answers stays, for many times the interval.
	alive, err := vtn.VEN2.NotificationStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = alive.Close() }()
	ended := make(chan error, 1)
	go func() {
		_, err := alive.Next()
		ended <- err
	}()
	select {
	case err := <-ended:
		t.Errorf("a client that answers pings was disconnected: %v", err)
	case <-time.After(400 * time.Millisecond):
	}

	// And the other way round: a VTN that has gone silent is noticed by a
	// client that keeps the stream alive.
	silent := http.NewServeMux()
	silent.HandleFunc("/notifiers/ws", func(w http.ResponseWriter, r *http.Request) {
		if conn, _, err := ws.Accept(w, r); err == nil {
			// Never reads: no pong, no close.
			<-r.Context().Done()
			_ = conn
		}
	})
	srv := newPlainServer(t, silent)
	lonely, err := client.NewVEN(client.Config{BaseURL: srv})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := lonely.NotificationStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	stream.KeepAlive(40 * time.Millisecond)
	start := time.Now()
	if _, err := stream.Next(); err == nil {
		t.Error("a stream to a silent VTN returned a notification")
	}
	if took := time.Since(start); took < 80*time.Millisecond || took > 3*time.Second {
		t.Errorf("the silence was noticed after %v", took)
	}
}

// When the VTN stops, it says so to who is connected, and lets nobody in.
func TestWebSocketShutdown(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{websocket: &server.WebSocketConfig{}})
	stream, err := vtn.VEN1.NotificationStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	ended := make(chan error, 1)
	go func() {
		_, err := stream.Next()
		ended <- err
	}()
	closed := make(chan struct{})
	go func() {
		_ = vtn.VTN.Close()
		close(closed)
	}()
	select {
	case err := <-ended:
		var bye *ws.CloseError
		if !errors.As(err, &bye) || bye.Code != ws.CloseGoingAway {
			t.Errorf("the stream ended with %v, want the VTN going away", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the VTN")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	var apiErr *openadr.Error
	if _, err := vtn.VEN1.NotificationStream(ctx); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a WebSocket after Close: %v", err)
	}
	// The rest of the API is the HTTP server's to stop, not the VTN's.
	if _, err := vtn.VEN1.Programs(ctx, client.ProgramQuery{}); err != nil {
		t.Errorf("a search after Close: %v", err)
	}
	if err := vtn.VTN.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

// What is on the WebSocket is not always a notification this client knows.
func TestWebSocketStreamPassesOverNoise(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/notifiers/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := ws.Accept(w, r)
		if err != nil {
			return
		}
		for _, m := range []string{
			`hello`, `{}`, `{"objectType":"GADGET","operation":"CREATE","object":{}}`,
			// As OpenLEADR writes one: with an ID, without targets.
			`{"id":"op-7","operation":"UPDATE","objectType":"PROGRAM","object":{"id":"p1","objectType":"PROGRAM","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","programName":"tariff"}}`,
			`{"id":"op-8","operation":"CREATE","objectType":"RESOURCE_GROUP","object":{"id":"g1","objectType":"RESOURCE_GROUP","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","resourceGroupName":"street","targets":[],"children":[{"type":"ven_resource","id":"r1"}]}}`,
		} {
			_ = conn.WriteText([]byte(m), time.Second)
		}
		_ = conn.Close(ws.CloseNormal, "that was all")
	})
	ven, err := client.NewVEN(client.Config{BaseURL: newPlainServer(t, mux)})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ven.NotificationStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	n, err := stream.Next()
	if p, ok := n.Object.(openadr.Program); err != nil || n.ID != "op-7" || n.Operation != openadr.OperationUpdate || !ok || p.Name != "tariff" {
		t.Errorf("got %+v, %v", n, err)
	}
	n, err = stream.Next()
	if g, ok := n.Object.(openadr.ResourceGroup); err != nil || n.ID != "op-8" || !ok || g.Name != "street" || len(g.Children) != 1 {
		t.Errorf("got %+v, %v", n, err)
	}
	var bye *ws.CloseError
	if _, err := stream.Next(); !errors.As(err, &bye) || bye.Reason != "that was all" {
		t.Errorf("the end of the stream: %v", err)
	}
}

func TestSubscriberValidatesAndCloses(t *testing.T) {
	if _, err := websocket.NewSubscriber(websocket.SubscriberConfig{Notify: func(context.Context, openadr.Notification) {}}); err == nil {
		t.Error("a Subscriber without Dial")
	}
	if _, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: func(context.Context) (*client.NotificationStream, error) { return nil, nil }}); err == nil {
		t.Error("a Subscriber without Notify")
	}
	// A VTN that cannot be reached: the Subscriber keeps trying, is not
	// ready, and can be closed at any time.
	attempts := make(chan struct{}, 100)
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial: func(context.Context) (*client.NotificationStream, error) {
			attempts <- struct{}{}
			return nil, errors.New("no route to host")
		},
		Notify:  func(context.Context, openadr.Notification) {},
		Backoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		select {
		case <-attempts:
		case <-time.After(5 * time.Second):
			t.Fatal("the Subscriber stopped trying")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := sub.Ready(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Ready without a VTN: %v", err)
	}
	if sub.Sessions() != 0 {
		t.Errorf("%d sessions without a VTN", sub.Sessions())
	}
	if err := sub.Close(); err != nil {
		t.Error(err)
	}
	if err := sub.Close(); err != nil {
		t.Error(err)
	}
	if err := sub.Ready(context.Background()); err == nil {
		t.Error("a closed Subscriber is ready")
	}
}
