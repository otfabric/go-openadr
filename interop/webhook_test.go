//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/server"
)

// Webhooks across implementations, both ways:
//
//   - testVTNWebhook: this module's VEN client subscribes on the VTN of an
//     image, with this module's webhook.Handler as the callback; Business
//     Logic creates an event; the VTN of the image notifies the handler.
//   - testListenWebhookOnGoVTN: the client of an image subscribes on this
//     module's VTN with listen-webhook (openadr-interop contract 1.5), its
//     receiver in its container; Business Logic creates an event; this
//     module's webhook.Sender notifies the receiver.

// callbackHost is the name under which a container reaches a server of the
// test on the host. docker run gets --add-host for it, which Docker Desktop
// does not need and Linux does.
const callbackHost = "host.docker.internal"

var callbackHostArgs = []string{"--add-host", callbackHost + ":host-gateway"}

// dayAhead is the ID of the untargeted program of the baseline fixture.
func dayAhead(t *testing.T, bl *client.BL) string {
	t.Helper()
	list, err := bl.Programs(ctx(t), client.ProgramQuery{})
	if err != nil {
		t.Fatalf("BL Programs: %v", err)
	}
	for _, p := range list {
		if p.Name == "day-ahead" {
			return p.ID
		}
	}
	t.Fatal("no program day-ahead")
	return ""
}

func testVTNWebhook(t *testing.T, adapter string) {
	if !has(t, needImage(t, adapter), "serverNotifiersWebhook") {
		t.Skipf("the VTN of %s has no webhooks", adapter)
	}
	p := vtnPeer(t, adapter, callbackHostArgs...)
	c := ctx(t)

	// The handler listens on every interface: on Linux the container
	// reaches the host through the bridge, not its loopback.
	received := make(chan openadr.Notification, 8)
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: &webhook.Handler{BearerToken: "interop-callback-token",
		Notify: func(_ context.Context, n openadr.Notification) error { received <- n; return nil }}}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	callback := fmt.Sprintf("http://%s:%d/notifications", callbackHost, ln.Addr().(*net.TCPAddr).Port)

	program := dayAhead(t, p.bl)
	sub, err := p.ven.CreateSubscription(c, openadr.Subscription{ClientName: p.name("webhook"), ProgramID: program,
		ObjectOperations: []openadr.ObjectOperation{{Objects: []openadr.ObjectType{openadr.ObjectEvent},
			Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: callback, BearerToken: "interop-callback-token"}}})
	if err != nil {
		t.Fatalf("CreateSubscription with the callback %s: %v", callback, err)
	}
	t.Cleanup(func() { _, _ = p.ven.DeleteSubscription(context.Background(), sub.ID) })
	if sub.ID == "" || len(sub.ObjectOperations) != 1 || sub.ObjectOperations[0].CallbackURL != callback {
		t.Errorf("the subscription as created: %+v", sub)
	}

	event := p.event(t, openadr.Event{ProgramID: program, Name: p.name("notified"),
		Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.42)}}}}}})
	select {
	case n := <-received:
		got, isEvent := n.Object.(openadr.Event)
		if n.ObjectType != openadr.ObjectEvent || n.Operation != openadr.OperationCreate || !isEvent || got.ID != event.ID || got.Name != event.Name {
			t.Errorf("the notification: %+v", n)
		}
		if v, _ := got.Intervals[0].Payloads[0].Values[0].Number(); v != 0.42 {
			t.Errorf("the event in the notification: %+v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no notification at the callback")
	}
}

// listened is what listen-webhook reports.
type listened struct {
	Subscription map[string]any `json:"subscription"`
	CallbackURL  string         `json:"callbackUrl"`
	Messages     []struct {
		ObjectType string         `json:"objectType"`
		Operation  string         `json:"operation"`
		Object     map[string]any `json:"object"`
	} `json:"messages"`
}

func testListenWebhookOnGoVTN(t *testing.T, adapter string) {
	needOperation(t, adapter, "ven", "listen-webhook")
	sender := webhook.NewSender(webhook.SenderConfig{AllowInsecure: true, Backoff: 50 * time.Millisecond})
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sender.Close(c)
	})
	vtn := startGoVTNWith(t, adapter, "baseline", vtnOptions{configure: func(cfg *server.Config) { cfg.Webhooks = sender }})

	// The receiver in the container is reached from the host on a port of
	// the host: with --network host on Linux, published on Docker Desktop.
	port := freePort(t)
	runner := vtn.client
	if runtime.GOOS != "linux" {
		runner.network = append(append([]string{}, runner.network...), "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, port))
	}
	callback := fmt.Sprintf("http://127.0.0.1:%d/notify", port)
	program := vtn.ids["program:day-ahead"]

	type outcome struct {
		r   result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := runner.try("listen-webhook", "--as", "ven-1", "--listen-port", fmt.Sprint(port), "--callback-url", callback,
			"--program-id", program, "--objects", "EVENT", "--operations", "CREATE", "--listen-ms", "60000")
		done <- outcome{r, err}
	}()

	// Subscribed once Business Logic sees the subscription.
	bl := vtn.goBL(t)
	deadline := time.Now().Add(90 * time.Second)
	for {
		list, err := bl.Subscriptions(ctx(t), client.SubscriptionQuery{})
		if err == nil && len(list) == 1 {
			if got := list[0].ObjectOperations[0]; got.CallbackURL != callback || got.BearerToken == "" {
				t.Errorf("the subscription of %s: %+v", adapter, list[0])
			}
			break
		}
		select {
		case o := <-done:
			t.Fatalf("listen-webhook ended before it subscribed: %+v, %v", o.r, o.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no subscription")
		}
		time.Sleep(200 * time.Millisecond)
	}

	event, err := bl.CreateEvent(ctx(t), openadr.Event{ProgramID: program, Name: "notified",
		Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.42)}}}}}})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	o := <-done
	if o.err != nil {
		t.Fatal(o.err)
	}
	var l listened
	if err := json.Unmarshal(ok(t, "listen-webhook", o.r, 201).Response, &l); err != nil {
		t.Fatalf("listen-webhook: %v: %s", err, o.r.Response)
	}
	if len(l.Messages) != 1 || l.Messages[0].ObjectType != "EVENT" || l.Messages[0].Operation != "CREATE" ||
		l.Messages[0].Object["id"] != event.ID || l.Messages[0].Object["eventName"] != "notified" {
		t.Errorf("the notification %s received: %+v", adapter, l.Messages)
	}
	// It deletes its subscription when it is done.
	if list, err := bl.Subscriptions(ctx(t), client.SubscriptionQuery{}); err != nil || len(list) != 0 {
		t.Errorf("subscriptions after listen-webhook: %+v, %v", list, err)
	}
}
