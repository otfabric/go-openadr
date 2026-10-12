// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/webhook"
)

// hook is a webhook of a test: it collects what the VTN sends it.
type hook struct {
	URL      string
	received chan openadr.Notification
}

func newHook(t *testing.T, token string) *hook {
	t.Helper()
	h := &hook{received: make(chan openadr.Notification, 64)}
	// A webhook is an https endpoint: the VTN's sender accepts nothing else.
	srv := httptest.NewTLSServer(&webhook.Handler{
		BearerToken: token,
		Notify: func(_ context.Context, n openadr.Notification) error {
			h.received <- n
			return nil
		},
	})
	t.Cleanup(srv.Close)
	h.URL = srv.URL + "/hook"
	return h
}

// next returns the next notification, or fails the test.
func (h *hook) next(t *testing.T) openadr.Notification {
	t.Helper()
	select {
	case n := <-h.received:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no notification arrived")
		return openadr.Notification{}
	}
}

// quiet fails the test if a notification arrives.
func (h *hook) quiet(t *testing.T) {
	t.Helper()
	select {
	case n := <-h.received:
		t.Fatalf("unexpected notification: %s %s %+v", n.Operation, n.ObjectType, n.Object)
	case <-time.After(150 * time.Millisecond):
	}
}

func all(objects ...openadr.ObjectType) []openadr.ObjectOperation {
	return []openadr.ObjectOperation{{
		Objects:    objects,
		Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete},
	}}
}

func subscribe(t *testing.T, create func(context.Context, openadr.Subscription) (openadr.Subscription, error), h *hook, token string, sub openadr.Subscription) openadr.Subscription {
	t.Helper()
	for i := range sub.ObjectOperations {
		sub.ObjectOperations[i].CallbackURL, sub.ObjectOperations[i].BearerToken = h.URL, token
	}
	if sub.ClientName == "" {
		sub.ClientName = "test"
	}
	out, err := create(context.Background(), sub)
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	return out
}

func TestNotifications(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})

	venHook := newHook(t, "ven-1-hook-token")
	sub := subscribe(t, vtn.VEN1.CreateSubscription, venHook, "ven-1-hook-token",
		openadr.Subscription{ClientName: "ven-one", ObjectOperations: all(openadr.ObjectProgram, openadr.ObjectEvent)})
	if sub.ID == "" || sub.ClientID != "ven-1" || sub.ObjectOperations[0].CallbackURL != venHook.URL {
		t.Fatalf("subscription = %+v", sub)
	}

	// Create, update, delete: each arrives, with the object as it then is.
	program := mustProgram(t, vtn.BL, openadr.Program{Name: "tariff"})
	n := venHook.next(t)
	if got, ok := n.Object.(openadr.Program); n.Operation != openadr.OperationCreate || n.ObjectType != openadr.ObjectProgram || !ok || got.ID != program.ID || got.Name != "tariff" {
		t.Fatalf("notification of the new program = %+v", n)
	}
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	event := mustEvent(t, vtn.BL, openadr.Event{
		ProgramID: program.ID, Name: "prices",
		IntervalPeriod: &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Hours: 1}},
		Intervals:      []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.3)}}}}},
	})
	n = venHook.next(t)
	got, ok := n.Object.(openadr.Event)
	if n.Operation != openadr.OperationCreate || !ok || got.ID != event.ID || len(got.Intervals) != 1 || !got.IntervalPeriod.Start.Equal(start) {
		t.Fatalf("notification of the new event = %+v", n)
	}
	if price, _ := got.Intervals[0].Payloads[0].Values[0].Number(); price != 0.3 {
		t.Errorf("price in the notification = %v", price)
	}
	event.Name = "prices-revised"
	if _, err := vtn.BL.UpdateEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if n = venHook.next(t); n.Operation != openadr.OperationUpdate || n.Object.(openadr.Event).Name != "prices-revised" {
		t.Errorf("notification of the update = %+v", n)
	}
	if _, err := vtn.BL.DeleteEvent(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	if n = venHook.next(t); n.Operation != openadr.OperationDelete || n.Object.(openadr.Event).ID != event.ID {
		t.Errorf("notification of the delete = %+v", n)
	}

	// Kinds and operations that were not subscribed to stay silent.
	if _, err := vtn.VEN2.CreateVEN(ctx, openadr.VEN{Name: "site-2"}); err != nil {
		t.Fatal(err)
	}
	venHook.quiet(t)

	// Ending the subscription ends the notifications.
	if _, err := vtn.VEN1.DeleteSubscription(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	mustProgram(t, vtn.BL, openadr.Program{Name: "after"})
	venHook.quiet(t)
}

// A subscriber is told of what a search of its own would show it.
func TestNotificationPrivacy(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})
	if _, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}

	plain := newHook(t, "")    // ven-1, naming no targets
	named := newHook(t, "")    // ven-1, naming group-a and group-b
	stranger := newHook(t, "") // ven-2, naming group-a, which it was not granted
	bl := newHook(t, "")       // Business Logic
	subscribe(t, vtn.VEN1.CreateSubscription, plain, "", openadr.Subscription{ObjectOperations: all(openadr.ObjectProgram, openadr.ObjectReport)})
	subscribe(t, vtn.VEN1.CreateSubscription, named, "", openadr.Subscription{ObjectOperations: all(openadr.ObjectProgram), Targets: []string{"group-a", "group-b"}})
	subscribe(t, vtn.VEN2.CreateSubscription, stranger, "", openadr.Subscription{ObjectOperations: all(openadr.ObjectProgram, openadr.ObjectReport), Targets: []string{"group-a"}})
	subscribe(t, vtn.BL.CreateSubscription, bl, "", openadr.Subscription{ObjectOperations: all(openadr.ObjectProgram, openadr.ObjectReport)})

	// A targeted program: only the granted VEN that names the target, and
	// Business Logic. The VEN is shown the target it was granted, no more.
	targeted := mustProgram(t, vtn.BL, openadr.Program{Name: "for-a", Targets: []string{"group-a", "group-b"}})
	n := named.next(t)
	if p := n.Object.(openadr.Program); p.ID != targeted.ID || !slices.Equal(p.Targets, []string{"group-a"}) || !slices.Equal(n.Targets, []string{"group-a"}) {
		t.Errorf("targeted program as the granted VEN is told: %+v, targets %v", p, n.Targets)
	}
	if p := bl.next(t).Object.(openadr.Program); len(p.Targets) != 2 {
		t.Errorf("targeted program as Business Logic is told: %+v", p)
	}
	plain.quiet(t)
	stranger.quiet(t)

	// An untargeted program: the subscriptions that name no targets.
	open := mustProgram(t, vtn.BL, openadr.Program{Name: "open"})
	if p := plain.next(t).Object.(openadr.Program); p.ID != open.ID {
		t.Errorf("open program as ven-1 is told: %+v", p)
	}
	bl.next(t)
	named.quiet(t)
	stranger.quiet(t)

	// A report: its author and Business Logic, not another VEN.
	event := mustEvent(t, vtn.BL, openadr.Event{ProgramID: open.ID})
	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "one", Resources: []openadr.ReportResource{}})
	if err != nil {
		t.Fatal(err)
	}
	if r := plain.next(t).Object.(openadr.Report); r.ID != report.ID {
		t.Errorf("report as its author is told: %+v", r)
	}
	if r := bl.next(t).Object.(openadr.Report); r.ID != report.ID || r.ClientID != "ven-1" {
		t.Errorf("report as Business Logic is told: %+v", r)
	}
	stranger.quiet(t)
}

func TestSubscriptionProgramFilter(t *testing.T) {
	vtn := start(t, options{})
	one := mustProgram(t, vtn.BL, openadr.Program{Name: "one"})
	two := mustProgram(t, vtn.BL, openadr.Program{Name: "two"})
	h := newHook(t, "")
	subscribe(t, vtn.VEN1.CreateSubscription, h, "", openadr.Subscription{ProgramID: one.ID, ObjectOperations: all(openadr.ObjectEvent)})

	mustEvent(t, vtn.BL, openadr.Event{ProgramID: two.ID, Name: "other"})
	mine := mustEvent(t, vtn.BL, openadr.Event{ProgramID: one.ID, Name: "mine"})
	if e := h.next(t).Object.(openadr.Event); e.ID != mine.ID {
		t.Errorf("event notified for a subscription to program one: %+v", e)
	}
	h.quiet(t)

	_, err := vtn.VEN1.CreateSubscription(context.Background(), openadr.Subscription{
		ClientName: "x", ProgramID: "no-such-program",
		ObjectOperations: []openadr.ObjectOperation{{Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: h.URL}},
	})
	if !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("subscription to a missing program: %v, want ErrConflict", err)
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})
	h := newHook(t, "s3cret")
	sub := subscribe(t, vtn.VEN1.CreateSubscription, h, "s3cret", openadr.Subscription{ClientName: "ven-one", ObjectOperations: all(openadr.ObjectEvent)})
	subscribe(t, vtn.VEN2.CreateSubscription, h, "s3cret", openadr.Subscription{ClientName: "ven-two", ObjectOperations: all(openadr.ObjectProgram)})

	// Whose subscription it is decides who sees it.
	if got, err := vtn.VEN1.Subscription(ctx, sub.ID); err != nil || got.ObjectOperations[0].BearerToken != "s3cret" {
		t.Errorf("own subscription = %+v, %v", got, err)
	}
	if _, err := vtn.VEN2.Subscription(ctx, sub.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("another client's subscription: %v, want ErrNotFound", err)
	}
	if _, err := vtn.VEN2.DeleteSubscription(ctx, sub.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("deleting another client's subscription: %v, want ErrNotFound", err)
	}
	for name, tc := range map[string]struct {
		list func(context.Context, client.SubscriptionQuery) ([]openadr.Subscription, error)
		q    client.SubscriptionQuery
		want int
	}{
		"ven-1 sees its own":         {vtn.VEN1.Subscriptions, client.SubscriptionQuery{}, 1},
		"bl sees all":                {vtn.BL.Subscriptions, client.SubscriptionQuery{}, 2},
		"by client name":             {vtn.BL.Subscriptions, client.SubscriptionQuery{ClientName: "ven-two"}, 1},
		"by object":                  {vtn.BL.Subscriptions, client.SubscriptionQuery{Objects: []openadr.ObjectType{openadr.ObjectEvent}}, 1},
		"by either object":           {vtn.BL.Subscriptions, client.SubscriptionQuery{Objects: []openadr.ObjectType{openadr.ObjectEvent, openadr.ObjectProgram}}, 2},
		"by an object nobody wants":  {vtn.BL.Subscriptions, client.SubscriptionQuery{Objects: []openadr.ObjectType{openadr.ObjectVEN}}, 0},
		"ven-1 cannot widen by name": {vtn.VEN1.Subscriptions, client.SubscriptionQuery{ClientName: "ven-two"}, 0},
	} {
		if got, err := tc.list(ctx, tc.q); err != nil || len(got) != tc.want {
			t.Errorf("%s: %d subscriptions, %v; want %d", name, len(got), err, tc.want)
		}
	}

	// An update keeps the owner; a subscription changing is itself notified
	// of, to those who subscribed to subscriptions.
	watcher := newHook(t, "")
	subscribe(t, vtn.BL.CreateSubscription, watcher, "", openadr.Subscription{ObjectOperations: all(openadr.ObjectSubscription)})
	watcher.next(t) // its own creation
	sub.ClientName = "renamed"
	updated, err := vtn.VEN1.UpdateSubscription(ctx, sub)
	if err != nil || updated.ClientName != "renamed" || updated.ClientID != "ven-1" || !updated.Created.Equal(sub.Created) {
		t.Errorf("UpdateSubscription = %+v, %v", updated, err)
	}
	if n := watcher.next(t); n.Operation != openadr.OperationUpdate || n.Object.(openadr.Subscription).ID != sub.ID {
		t.Errorf("notification of the changed subscription = %+v", n)
	}
}

// The VTN accepts a callback URL only after whoever answers there has
// echoed its challenge.
func TestCallbackVerification(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})
	silent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "hello") }))
	defer silent.Close()
	refusing := httptest.NewTLSServer(http.NotFoundHandler())
	defer refusing.Close()
	plain := httptest.NewServer(&webhook.Handler{})
	defer plain.Close()
	good := newHook(t, "")

	at := func(url string) openadr.Subscription {
		return openadr.Subscription{ClientName: "x", ObjectOperations: []openadr.ObjectOperation{{
			Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: url,
		}}}
	}
	for name, url := range map[string]string{
		"does not echo":       silent.URL,
		"answers 404":         refusing.URL,
		"nobody there":        "https://127.0.0.1:1/hook",
		"not https":           plain.URL,
		"not http at all":     "ftp://example.com/hook",
		"carries credentials": strings.Replace(good.URL, "https://", "https://user:pw@", 1),
	} {
		_, err := vtn.VEN1.CreateSubscription(ctx, at(url))
		var apiErr *openadr.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || !strings.Contains(apiErr.Problem.Detail, "objectOperations[0].callbackUrl") {
			t.Errorf("%s: %v, want a 400 about the callback URL", name, err)
		}
	}
	if list, _ := vtn.VEN1.Subscriptions(ctx, client.SubscriptionQuery{}); len(list) != 0 {
		t.Errorf("%d subscriptions exist after only refused ones", len(list))
	}

	// An update is verified where the URL is new.
	sub, err := vtn.VEN1.CreateSubscription(ctx, at(good.URL))
	if err != nil {
		t.Fatal(err)
	}
	sub.ObjectOperations[0].CallbackURL = silent.URL
	if _, err := vtn.VEN1.UpdateSubscription(ctx, sub); !errors.Is(err, openadr.ErrBadRequest) {
		t.Errorf("update to an unverifiable URL: %v, want ErrBadRequest", err)
	}
	if kept, _ := vtn.VEN1.Subscription(ctx, sub.ID); kept.ObjectOperations[0].CallbackURL != good.URL {
		t.Errorf("the refused update changed the subscription: %+v", kept)
	}

	// Without what a subscription needs, nothing is asked of the VTN.
	var invalid *openadr.ValidationError
	if _, err := vtn.VEN1.CreateSubscription(ctx, openadr.Subscription{ObjectOperations: all(openadr.ObjectEvent)}); !errors.As(err, &invalid) {
		t.Errorf("subscription without clientName and callback: %v", err)
	}
}
