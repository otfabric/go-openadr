// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/mqtt"
	"github.com/otfabric/go-openadr/server/storage"
)

// A VTN on the journal store is stopped and started again: its clients find
// everything as they left it, with the same access rules, and a
// subscription made before the restart is still notified.
func TestRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vtn.journal")
	store, err := storage.OpenFile(path, storage.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := start(t, options{store: store})

	if _, err := before.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}
	start2030 := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	program := mustProgram(t, before.BL, openadr.Program{Name: "tariff", Targets: []string{"group-a"},
		PayloadDescriptors: []openadr.PayloadDescriptor{openadr.EventPayloadDescriptor{PayloadType: "PRICE", Currency: "EUR"}}})
	event := mustEvent(t, before.BL, openadr.Event{ProgramID: program.ID, Name: "prices", Targets: []string{"group-a"},
		IntervalPeriod: &openadr.IntervalPeriod{Start: &start2030, Duration: &openadr.Duration{Hours: 1}},
		Intervals:      []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.3)}}}}}})
	report, err := before.VEN1.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "one", Resources: []openadr.ReportResource{}})
	if err != nil {
		t.Fatal(err)
	}
	hook := newHook(t, "tok")
	sub := subscribe(t, before.VEN1.CreateSubscription, hook, "tok",
		openadr.Subscription{ObjectOperations: all(openadr.ObjectEvent), Targets: []string{"group-a"}})

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenFile(path, storage.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after := start(t, options{store: reopened})

	if got, err := after.BL.Program(ctx, program.ID); err != nil || !reflect.DeepEqual(got, program) {
		t.Errorf("program after the restart:\n got %+v, %v\nwant %+v", got, err, program)
	}
	if got, err := after.BL.Event(ctx, event.ID); err != nil || !reflect.DeepEqual(got, event) {
		t.Errorf("event after the restart:\n got %+v, %v\nwant %+v", got, err, event)
	}
	if got, err := after.VEN1.Report(ctx, report.ID); err != nil || !reflect.DeepEqual(got, report) {
		t.Errorf("report after the restart: %+v, %v", got, err)
	}
	if got, err := after.VEN1.Subscription(ctx, sub.ID); err != nil || !reflect.DeepEqual(got, sub) {
		t.Errorf("subscription after the restart: %+v, %v", got, err)
	}
	// The rules hold on what was restored: the grant, the ownership.
	if list, err := after.VEN1.Events(ctx, client.EventQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 1 {
		t.Errorf("the granted VEN after the restart: %d events, %v", len(list), err)
	}
	if list, err := after.VEN2.Events(ctx, client.EventQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 0 {
		t.Errorf("another VEN after the restart: %d events, %v", len(list), err)
	}
	if _, err := after.VEN2.Report(ctx, report.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("another VEN reading the report after the restart: %v", err)
	}
	if _, err := after.BL.CreateProgram(ctx, openadr.Program{Name: "tariff"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("a duplicate name after the restart: %v", err)
	}
	// And the subscription of before is served by the VTN of after.
	added := mustEvent(t, after.BL, openadr.Event{ProgramID: program.ID, Name: "more", Targets: []string{"group-a"}})
	if n := hook.next(t); n.Object.(openadr.Event).ID != added.ID {
		t.Errorf("notification after the restart: %+v", n)
	}
}

// The VTN behind an identity provider that issues JSON Web Tokens.
func TestJWTAccessTokens(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{jwt: true})

	program := mustProgram(t, vtn.BL, openadr.Program{Name: "tariff"})
	event := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID})
	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "one", Resources: []openadr.ReportResource{}})
	if err != nil || report.ClientID != "ven-1" {
		t.Fatalf("report under a JWT: %+v, %v; the client ID is the token's client_id", report, err)
	}
	if _, err := vtn.VEN2.Report(ctx, report.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("another client's report under JWTs: %v", err)
	}

	with := func(token string) *client.VEN {
		ven, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP, Tokens: client.StaticToken(token)})
		if err != nil {
			t.Fatal(err)
		}
		return ven
	}
	scopes := auth.VENScopes()
	for name, tc := range map[string]struct {
		token string
		want  error
	}{
		"expired":             {vtn.idp.mint("ven-1", scopes, -time.Minute, nil), openadr.ErrUnauthorized},
		"for another service": {vtn.idp.mint("ven-1", scopes, time.Hour, func(c map[string]any) { c["aud"] = "https://billing.example" }), openadr.ErrUnauthorized},
		"another issuer":      {vtn.idp.mint("ven-1", scopes, time.Hour, func(c map[string]any) { c["iss"] = "https://evil.example" }), openadr.ErrUnauthorized},
		"signed by someone":   {newIdentityProvider(t).mint("ven-1", scopes, time.Hour, nil), openadr.ErrUnauthorized},
		"without scopes":      {vtn.idp.mint("ven-1", nil, time.Hour, nil), openadr.ErrForbidden},
		"a scope too few":     {vtn.idp.mint("ven-1", []string{auth.ScopeWriteReports}, time.Hour, nil), openadr.ErrForbidden},
	} {
		if _, err := with(tc.token).Programs(ctx, client.ProgramQuery{}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	// What a token may do is what its scopes say, whoever holds it.
	if _, err := with(vtn.idp.mint("ven-1", []string{auth.ScopeReadTargets}, time.Hour, nil)).Programs(ctx, client.ProgramQuery{}); err != nil {
		t.Errorf("a token with exactly the scope needed: %v", err)
	}

	// A token the VTN stops accepting: the client asks its source for
	// another, once, and the call succeeds.
	src := vtn.idp.source("ven-1", scopes)
	src.current = vtn.idp.mint("ven-1", scopes, -time.Minute, nil)
	ven, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP, Tokens: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ven.Programs(ctx, client.ProgramQuery{}); err != nil || src.minted != 1 {
		t.Errorf("call with an expired token and a source that can renew: %v, %d tokens minted", err, src.minted)
	}
}

// mqttClient subscribes to topics on the stack's broker and collects what
// arrives.
type mqttClient struct {
	*mqtt.Subscriber
	received chan openadr.Notification
	topics   chan string
}

func subscribeMQTT(t *testing.T, vtn *stack, id string, topics ...string) *mqttClient {
	t.Helper()
	c := &mqttClient{received: make(chan openadr.Notification, 64), topics: make(chan string, 64)}
	var err error
	c.Subscriber, err = mqtt.NewSubscriber(mqtt.SubscriberConfig{
		Broker: mqtt.Broker{URL: vtn.Broker.URL(), ClientID: id, Username: "client", Password: "client-broker-password", Backoff: 20 * time.Millisecond},
		Topics: topics,
		Notify: func(_ context.Context, topic string, n openadr.Notification) {
			c.topics <- topic
			c.received <- n
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ready(ctx); err != nil {
		t.Fatalf("MQTT subscriber %s: %v", id, err)
	}
	return c
}

func (c *mqttClient) next(t *testing.T) (string, openadr.Notification) {
	t.Helper()
	select {
	case n := <-c.received:
		return <-c.topics, n
	case <-time.After(5 * time.Second):
		t.Fatal("no MQTT notification arrived")
		return "", openadr.Notification{}
	}
}

func (c *mqttClient) quiet(t *testing.T) {
	t.Helper()
	select {
	case n := <-c.received:
		t.Fatalf("unexpected MQTT notification on %s: %s %s", <-c.topics, n.Operation, n.ObjectType)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestMQTTNotifications(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{mqtt: true})

	// A client learns from the VTN where the broker is, and which topics
	// are its own.
	notifiers, err := vtn.VEN1.Notifiers(ctx)
	if err != nil || !notifiers.Webhook || notifiers.MQTT == nil || !slices.Equal(notifiers.MQTT.URIs, []string{vtn.Broker.URL()}) ||
		notifiers.MQTT.Serialization != "JSON" || notifiers.MQTT.Authentication.Method != openadr.MQTTAnonymous {
		t.Fatalf("Notifiers = %+v, %v", notifiers, err)
	}
	site1, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}})
	if err != nil {
		t.Fatal(err)
	}
	site2, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-2", ClientID: "ven-2"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := vtn.VEN1.MQTTTopics(ctx, client.EventsForVEN(site1.ID))
	if err != nil || events.Create != "oadr/vens/"+site1.ID+"/events/create" || events.All != "oadr/vens/"+site1.ID+"/events/+" {
		t.Fatalf("topics of ven-1's events = %+v, %v", events, err)
	}
	own, err := vtn.VEN1.MQTTTopics(ctx, client.OneVEN(site1.ID))
	if err != nil || own.Create != "" || own.Update == "" || own.Delete == "" {
		t.Errorf("topics of one ven = %+v, %v; an object has no topic for its own creation", own, err)
	}
	// Topics are given to those entitled to what is published on them.
	if _, err := vtn.VEN2.MQTTTopics(ctx, client.EventsForVEN(site1.ID)); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("topics of another client's ven: %v, want ErrNotFound", err)
	}
	if _, err := vtn.VEN1.MQTTTopics(ctx, client.AllEvents()); !errors.Is(err, openadr.ErrForbidden) {
		t.Errorf("topics of all events, as a VEN: %v, want ErrForbidden", err)
	}
	if _, err := vtn.BL.MQTTTopics(ctx, client.OneProgram("no-such-program")); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("topics of a missing program: %v, want ErrNotFound", err)
	}
	for _, set := range []client.TopicSet{client.AllPrograms(), client.AllEvents(), client.AllReports(), client.AllSubscriptions(),
		client.AllVENs(), client.AllResources(), client.ProgramsForVEN(site1.ID), client.ResourcesOfVEN(site1.ID)} {
		if topics, err := vtn.BL.MQTTTopics(ctx, set); err != nil || topics.Create == "" || topics.Update == "" || topics.Delete == "" || topics.All == "" {
			t.Errorf("topics of %+v = %+v, %v", set, topics, err)
		}
	}

	programs2, _ := vtn.VEN2.MQTTTopics(ctx, client.ProgramsForVEN(site2.ID))
	events2, _ := vtn.VEN2.MQTTTopics(ctx, client.EventsForVEN(site2.ID))
	allEvents, _ := vtn.BL.MQTTTopics(ctx, client.AllEvents())
	allReports, _ := vtn.BL.MQTTTopics(ctx, client.AllReports())
	ven1 := subscribeMQTT(t, vtn, "ven-1", events.All)
	ven2 := subscribeMQTT(t, vtn, "ven-2", events2.All, programs2.All)
	bl := subscribeMQTT(t, vtn, "bl", allEvents.All, allReports.Create)

	// An event for everyone: each VEN on its own topic, Business Logic on
	// the topic of all events.
	program := mustProgram(t, vtn.BL, openadr.Program{Name: "tariff"})
	if topic, n := ven2.next(t); n.ObjectType != openadr.ObjectProgram || topic != "oadr/vens/"+site2.ID+"/programs/create" {
		t.Errorf("program on ven-2's topics: %s %+v", topic, n)
	}
	open := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "open",
		Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.3)}}}}}})
	topic, n := ven1.next(t)
	got, ok := n.Object.(openadr.Event)
	if !ok || topic != events.Create || n.Operation != openadr.OperationCreate || got.ID != open.ID || len(got.Intervals) != 1 {
		t.Errorf("event on ven-1's topic: %s %+v", topic, n)
	}
	if _, n := ven2.next(t); n.Object.(openadr.Event).ID != open.ID {
		t.Errorf("event on ven-2's topic: %+v", n)
	}
	if topic, n := bl.next(t); topic != allEvents.Create || n.Object.(openadr.Event).ID != open.ID {
		t.Errorf("event on the topic of all events: %s %+v", topic, n)
	}

	// A targeted event: the VEN that was granted the target, with that
	// target only; Business Logic with all of them; nobody else.
	targeted := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "for-a", Targets: []string{"group-a", "group-b"}})
	if _, n := ven1.next(t); n.Object.(openadr.Event).ID != targeted.ID || !slices.Equal(n.Object.(openadr.Event).Targets, []string{"group-a"}) {
		t.Errorf("targeted event on ven-1's topic: %+v", n.Object)
	}
	if _, n := bl.next(t); len(n.Object.(openadr.Event).Targets) != 2 {
		t.Errorf("targeted event on the topic of all events: %+v", n.Object)
	}
	ven2.quiet(t)

	// Updates and deletes have their own topics; a report goes to Business Logic.
	targeted.Name = "for-a-revised"
	if _, err := vtn.BL.UpdateEvent(ctx, targeted); err != nil {
		t.Fatal(err)
	}
	if topic, n := ven1.next(t); topic != events.Update || n.Operation != openadr.OperationUpdate || n.Object.(openadr.Event).Name != "for-a-revised" {
		t.Errorf("update on ven-1's topic: %s %+v", topic, n)
	}
	bl.next(t)
	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: open.ID, ClientName: "one", Resources: []openadr.ReportResource{}})
	if err != nil {
		t.Fatal(err)
	}
	if topic, n := bl.next(t); topic != allReports.Create || n.Object.(openadr.Report).ID != report.ID {
		t.Errorf("report on the topic of reports: %s %+v", topic, n)
	}
	ven1.quiet(t)
	ven2.quiet(t)

	// The broker drops everyone. The VTN and the client both come back, and
	// what is published after is delivered; the client can tell that it was
	// away.
	vtn.Broker.DropClients()
	deadline := time.Now().Add(5 * time.Second)
	for ven1.Sessions() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ven1.Ready(readyCtx); err != nil || ven1.Sessions() < 2 {
		t.Fatalf("the subscriber did not come back: %v, %d sessions", err, ven1.Sessions())
	}
	later := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "after-the-outage"})
	if _, n := ven1.next(t); n.Object.(openadr.Event).ID != later.ID {
		t.Errorf("event after the outage: %+v", n)
	}

	// Every message the VTN published was on a topic under its prefix.
	for _, m := range vtn.Broker.Published() {
		if !strings.HasPrefix(m.Topic, "oadr/") {
			t.Errorf("published outside the prefix: %s", m.Topic)
		}
	}
}

// Without a broker configured the VTN says so.
func TestNoMQTT(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})
	if n, err := vtn.VEN1.Notifiers(ctx); err != nil || !n.Webhook || n.MQTT != nil {
		t.Errorf("Notifiers = %+v, %v", n, err)
	}
	var apiErr *openadr.Error
	if _, err := vtn.BL.MQTTTopics(ctx, client.AllEvents()); !errors.As(err, &apiErr) || apiErr.StatusCode != 501 {
		t.Errorf("MQTT topics without a broker: %v, want 501", err)
	}
	var invalid *openadr.ValidationError
	if _, err := vtn.BL.MQTTTopics(ctx, client.OneProgram("a/b")); !errors.As(err, &invalid) {
		t.Errorf("topics for an ID that is not one: %v", err)
	}
	if _, err := vtn.BL.MQTTTopics(ctx, client.TopicSet{}); !errors.As(err, &invalid) {
		t.Errorf("topics of the zero TopicSet: %v", err)
	}
}
