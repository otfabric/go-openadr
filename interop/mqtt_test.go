//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/mqtt"
)

// listen subscribes to topics on a broker and collects what arrives.
func listen(t *testing.T, broker mqtt.Broker, topics ...string) chan openadr.Notification {
	t.Helper()
	received := make(chan openadr.Notification, 32)
	sub, err := mqtt.NewSubscriber(mqtt.SubscriberConfig{Broker: broker, Topics: topics,
		Notify: func(_ context.Context, _ string, n openadr.Notification) { received <- n }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sub.Ready(c); err != nil {
		t.Fatalf("subscribing to %v on %s as %q: %v", topics, broker.URL, broker.Username, err)
	}
	return received
}

// This module's MQTT subscriber, on the production broker of the reference image,
// receiving the notifications another implementation's VTN publishes.
func TestOpenLEADRMQTTNotifications(t *testing.T) {
	img := needImage(t, "openleadr")
	if !has(t, img, "serverMqtt") {
		// The pinned image has one. An older image given by environment
		// variable may not: say so instead of passing by doing nothing.
		if os.Getenv("OPENADR_INTEROP_REQUIRE") != "" && os.Getenv("OPENADR_INTEROP_OPENLEADR_IMAGE") == "" {
			t.Fatalf("%s has no MQTT broker", img)
		}
		t.Skipf("%s has no MQTT broker (openadr-interop v0.2.0 or later has)", img)
	}
	p := openleadr(t)
	c := ctx(t)
	fx := readFixture(t, img, "baseline")

	// The VTN says where its broker is.
	notifiers, err := p.ven.Notifiers(c)
	if err != nil || notifiers.MQTT == nil || len(notifiers.MQTT.URIs) != 1 || notifiers.MQTT.Serialization != "JSON" {
		t.Fatalf("Notifiers = %+v, %v", notifiers, err)
	}
	// The broker of the reference image takes the client secret as the
	// password, with the client ID as the user name; see the container
	// contract of openadr-interop.
	login := func(name string) mqtt.Broker {
		id, secret := fx.credentials(t, name)
		return mqtt.Broker{URL: notifiers.MQTT.URIs[0], ClientID: "go-openadr-" + name + "-" + p.nonce, Username: id, Password: secret}
	}

	// ... and which topics are the VEN's own.
	vens, err := p.ven.VENs(c, client.VENQuery{})
	if err != nil || len(vens) != 1 {
		t.Fatalf("ven-1's ven: %+v, %v", vens, err)
	}
	events, err := p.ven.MQTTTopics(c, client.EventsForVEN(vens[0].ID))
	if err != nil || events.Create == "" || events.Update == "" || events.Delete == "" || events.All == "" || !strings.Contains(events.Create, vens[0].ID) {
		t.Fatalf("topics of ven-1's events = %+v, %v", events, err)
	}
	all, err := p.bl.MQTTTopics(c, client.AllEvents())
	if err != nil || all.Create == "" || all.All == "" {
		t.Fatalf("topics of all events = %+v, %v", all, err)
	}
	if one, err := p.bl.MQTTTopics(c, client.OneProgram(mustFind(t, p, "day-ahead"))); err != nil || one.Update == "" || one.Create != "" {
		t.Errorf("topics of one program = %+v, %v; an object has no topic for its own creation", one, err)
	}

	// OpenLEADR v0.2.7 names a ven's event topics events/vens/{id}/<op> in
	// its topic operation and publishes on vens/{id}/events/<op>: a client
	// that subscribes to what it was told hears nothing. The VEN here
	// listens on both, so that the test shows what this module's subscriber
	// does with OpenLEADR's notifications, and keeps passing when OpenLEADR
	// makes the two agree. The finding is in openadr-interop's
	// docs/COMPATIBILITY.md.
	forVEN := listen(t, login("ven-1"), events.All, "vens/"+vens[0].ID+"/events/+")
	forBL := listen(t, login("bl"), all.All)

	// Business Logic publishes an event, changes it and withdraws it.
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	event := p.event(t, openadr.Event{ProgramID: mustFind(t, p, "day-ahead"), Name: "mqtt-" + p.nonce,
		IntervalPeriod: &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Hours: 1}},
		Intervals:      []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.42)}}}}}})
	expect := func(who string, ch chan openadr.Notification, op openadr.Operation, name string) {
		t.Helper()
		select {
		case n := <-ch:
			got, ok := n.Object.(openadr.Event)
			if !ok || n.ObjectType != openadr.ObjectEvent || n.Operation != op || got.ID != event.ID || got.Name != name {
				t.Errorf("%s: notification %s %s %+v, want %s of event %q", who, n.Operation, n.ObjectType, n.Object, op, name)
			}
			if op == openadr.OperationCreate {
				if price, _ := got.Intervals[0].Payloads[0].Values[0].Number(); len(got.Intervals) != 1 || price != 0.42 || !got.IntervalPeriod.Start.Equal(start) {
					t.Errorf("%s: the event in the notification: %+v", who, got)
				}
			}
		case <-time.After(15 * time.Second):
			t.Errorf("%s: no %s notification arrived", who, op)
		}
	}
	expect("Business Logic", forBL, openadr.OperationCreate, event.Name)
	expect("the VEN", forVEN, openadr.OperationCreate, event.Name)

	event.Name += "-revised"
	if _, err := p.bl.UpdateEvent(c, event); err != nil {
		t.Fatal(err)
	}
	expect("Business Logic", forBL, openadr.OperationUpdate, event.Name)
	expect("the VEN", forVEN, openadr.OperationUpdate, event.Name)

	if _, err := p.bl.DeleteEvent(c, event.ID); err != nil {
		t.Fatal(err)
	}
	expect("Business Logic", forBL, openadr.OperationDelete, event.Name)
	expect("the VEN", forVEN, openadr.OperationDelete, event.Name)

	// The broker refuses a client it does not know.
	stranger, err := mqtt.NewSubscriber(mqtt.SubscriberConfig{
		Broker: mqtt.Broker{URL: notifiers.MQTT.URIs[0], Username: "nobody", Password: "nothing", Backoff: 100 * time.Millisecond},
		Topics: []string{"#"}, Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stranger.Close() }()
	short, cancel := context.WithTimeout(c, 2*time.Second)
	defer cancel()
	if err := stranger.Ready(short); err == nil {
		t.Error("a client with made-up credentials subscribed to everything")
	}
}

// mustFind returns the ID of the fixture's program with the given name.
func mustFind(t *testing.T, p peer, name string) string {
	t.Helper()
	programs, err := p.bl.Programs(ctx(t), client.ProgramQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range programs {
		if program.Name == name {
			return program.ID
		}
	}
	t.Fatalf("the VTN has no program %q", name)
	return ""
}
