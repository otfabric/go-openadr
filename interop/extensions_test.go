//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/websocket"
)

// What is not of OpenADR 3.1.0, and that this module shares with OpenLEADR:
// the point of sharing it is that the two work together.

// Resource groups, written and read by this module's clients on OpenLEADR.
func TestOpenLEADRResourceGroups(t *testing.T) {
	p := openleadr(t)
	c := ctx(t)

	// The battery of the fixture, of ven-1.
	batteries, err := p.ven.Resources(c, client.ResourceQuery{})
	if err != nil || len(batteries) != 1 {
		t.Fatalf("ven-1's resources: %+v, %v", batteries, err)
	}
	battery := batteries[0]

	street, err := p.bl.CreateResourceGroup(c, openadr.ResourceGroup{Name: p.name("street"), Targets: []string{p.name("feeder")},
		Attributes: []openadr.ValuesMap{{Type: "LOCATION", Values: []openadr.Value{openadr.PointValue(52.1, 4.3)}}},
		Children:   []openadr.ResourceGroupChild{{Type: openadr.ChildResource, ID: battery.ID}}})
	if err != nil {
		t.Fatalf("CreateResourceGroup: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteResourceGroup(context.Background(), street.ID) })
	if street.ID == "" || street.Created.IsZero() || street.Name != p.name("street") || len(street.Attributes) != 1 ||
		!slices.Equal(street.Targets, []string{p.name("feeder")}) ||
		!slices.Equal(street.Children, []openadr.ResourceGroupChild{{Type: openadr.ChildResource, ID: battery.ID}}) {
		t.Errorf("created %+v", street)
	}
	district, err := p.bl.CreateResourceGroup(c, openadr.ResourceGroup{Name: p.name("district"),
		Children: []openadr.ResourceGroupChild{{Type: openadr.ChildResourceGroup, ID: street.ID}}})
	if err != nil {
		t.Fatalf("CreateResourceGroup with a group in it: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteResourceGroup(context.Background(), district.ID) })

	if got, err := p.bl.ResourceGroup(c, street.ID); err != nil || got.Name != street.Name || len(got.Children) != 1 {
		t.Errorf("read back %+v, %v", got, err)
	}
	if list, err := p.bl.ResourceGroups(c, client.ResourceGroupQuery{Name: p.name("district")}); err != nil || len(list) != 1 || list[0].ID != district.ID {
		t.Errorf("by name: %+v, %v", list, err)
	}

	// The VEN whose resource it is reads both groups; another reads neither.
	mine, err := p.ven.ResourceGroups(c, client.ResourceGroupQuery{})
	has := func(list []openadr.ResourceGroup, id string) bool {
		return slices.ContainsFunc(list, func(g openadr.ResourceGroup) bool { return g.ID == id })
	}
	if err != nil || !has(mine, street.ID) || !has(mine, district.ID) {
		t.Errorf("ven-1's groups: %+v, %v", mine, err)
	}
	if got, err := p.ven.ResourceGroup(c, district.ID); err != nil || len(got.Children) != 1 || got.Children[0].ID != street.ID {
		t.Errorf("ven-1 reads the district: %+v, %v", got, err)
	}
	if list, err := p.ven2.ResourceGroups(c, client.ResourceGroupQuery{}); err != nil || has(list, street.ID) || has(list, district.ID) {
		t.Errorf("ven-2's groups: %+v, %v", list, err)
	}
	if _, err := p.ven2.ResourceGroup(c, street.ID); err == nil {
		t.Error("ven-2 read a group that holds nothing of its own")
	}

	// A VEN does not write them.
	var apiErr *openadr.Error
	if _, err := p.venAsBL.CreateResourceGroup(c, openadr.ResourceGroup{Name: p.name("mine")}); !errors.As(err, &apiErr) || apiErr.StatusCode != 403 {
		t.Errorf("a VEN creating a group: %v", err)
	}

	// An update replaces the children.
	street.Name, street.Children = p.name("street-renamed"), nil
	updated, err := p.bl.UpdateResourceGroup(c, street)
	if err != nil || updated.Name != street.Name || len(updated.Children) != 0 {
		t.Errorf("updated %+v, %v", updated, err)
	}
	if list, err := p.ven.ResourceGroups(c, client.ResourceGroupQuery{}); err != nil || has(list, street.ID) {
		t.Errorf("ven-1 still sees a group its resource has left: %v", err)
	}
	deleted, err := p.bl.DeleteResourceGroup(c, district.ID)
	if err != nil || deleted.ID != district.ID {
		t.Errorf("deleted %+v, %v", deleted, err)
	}
	if _, err := p.bl.ResourceGroup(c, district.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("a deleted group: %v", err)
	}
}

// This module's WebSocket subscriber, receiving what another
// implementation's VTN sends.
func TestOpenLEADRWebSocketNotifications(t *testing.T) {
	p := openleadr(t)
	c := ctx(t)

	notifiers, err := p.ven.Notifiers(c)
	if err != nil {
		t.Fatal(err)
	}
	if !notifiers.WebSocket {
		t.Skipf("the VTN does not announce WebSockets: %+v", notifiers)
	}
	// By the mechanism WEBSOCKET, without a callback URL.
	sub, err := p.ven.CreateSubscription(c, openadr.Subscription{ClientName: p.name("ws"), ObjectOperations: []openadr.ObjectOperation{{
		Objects:    []openadr.ObjectType{openadr.ObjectProgram, openadr.ObjectEvent},
		Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete},
		Mechanism:  openadr.MechanismWebSocket,
	}}})
	if err != nil {
		t.Fatalf("subscribing by WebSocket: %v", err)
	}
	t.Cleanup(func() { _, _ = p.ven.DeleteSubscription(context.Background(), sub.ID) })
	if got := sub.ObjectOperations[0]; got.Mechanism != openadr.MechanismWebSocket || got.CallbackURL != "" {
		t.Errorf("the subscription came back as %+v", got)
	}

	received := make(chan openadr.Notification, 32)
	listener, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial:   p.ven.NotificationStream,
		Notify: func(_ context.Context, n openadr.Notification) { received <- n },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.Ready(c); err != nil {
		t.Fatalf("opening the WebSocket: %v", err)
	}
	next := func(what string) openadr.Notification {
		t.Helper()
		select {
		case n := <-received:
			return n
		case <-time.After(15 * time.Second):
			t.Fatalf("no notification of %s", what)
			return openadr.Notification{}
		}
	}

	// OpenLEADR takes one WebSocket per client.
	if _, err := p.ven.NotificationStream(c); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("a second WebSocket of the same client: %v", err)
	}

	program := p.program(t, openadr.Program{Name: p.name("ws-program")})
	n := next("a created program")
	if got, ok := n.Object.(openadr.Program); !ok || got.ID != program.ID || n.Operation != openadr.OperationCreate || n.ObjectType != openadr.ObjectProgram {
		t.Errorf("got %+v", n)
	}
	if n.ID == "" {
		t.Error("the notification has no ID; OpenLEADR sends one")
	}
	event := p.event(t, openadr.Event{ProgramID: program.ID, Name: p.name("ws-event")})
	if n := next("a created event"); n.Operation != openadr.OperationCreate || n.ObjectType != openadr.ObjectEvent {
		t.Errorf("got %+v", n)
	} else if got, ok := n.Object.(openadr.Event); !ok || got.ID != event.ID {
		t.Errorf("got %+v", n)
	}
	event.Name = p.name("ws-event-renamed")
	if _, err := p.bl.UpdateEvent(c, event); err != nil {
		t.Fatal(err)
	}
	if n := next("an updated event"); n.Operation != openadr.OperationUpdate {
		t.Errorf("got %+v", n)
	}
	if _, err := p.bl.DeleteEvent(c, event.ID); err != nil {
		t.Fatal(err)
	}
	if n := next("a deleted event"); n.Operation != openadr.OperationDelete || n.ObjectType != openadr.ObjectEvent {
		t.Errorf("got %+v", n)
	}

	// A targeted program is not for a VEN without the target.
	p.program(t, openadr.Program{Name: p.name("ws-not-for-ven-1"), Targets: []string{p.name("elsewhere")}})
	select {
	case n := <-received:
		t.Errorf("ven-1 was told of a program it may not see: %+v", n)
	case <-time.After(time.Second):
	}

	// The place is given up when the WebSocket is closed. OpenLEADR
	// notices that when it fails to send to the client, not before: until
	// then it answers 409, and a client that reconnects waits for the
	// notifications that would have been its own.
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for i := 0; ; i++ {
		// Something to send, and so to fail at.
		p.program(t, openadr.Program{Name: p.name(fmt.Sprintf("ws-after-close-%d", i))})
		stream, err := p.ven.NotificationStream(c)
		if err == nil {
			_ = stream.Close()
			break
		}
		if !errors.Is(err, openadr.ErrConflict) || time.Now().After(deadline) {
			t.Fatalf("no WebSocket after the first was closed: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
