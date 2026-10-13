// SPDX-License-Identifier: MIT

package v310

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

func TestResourceGroupCodec(t *testing.T) {
	c := Codec().ResourceGroups
	// The example of OpenLEADR's own tests.
	theirs := `{
		"id": "test-resource-group",
		"createdDateTime": "2023-06-15T09:30:00Z",
		"modificationDateTime": "2023-06-15T09:30:00Z",
		"resourceGroupName": "RESOURCE_GROUP_0999",
		"objectType": "RESOURCE_GROUP",
		"targets": ["resource_group_0999"],
		"children": [{"type":"resource_group","id":"resource-group-1"}, {"type":"ven_resource","id":"ven-1"}]
	}`
	at := time.Date(2023, 6, 15, 9, 30, 0, 0, time.UTC)
	want := openadr.ResourceGroup{
		Metadata: openadr.Metadata{ID: "test-resource-group", Created: at, Modified: at},
		Name:     "RESOURCE_GROUP_0999", Targets: []string{"resource_group_0999"},
		Children: []openadr.ResourceGroupChild{{Type: openadr.ChildResourceGroup, ID: "resource-group-1"}, {Type: openadr.ChildResource, ID: "ven-1"}},
	}
	got, form, err := c.Unmarshal([]byte(theirs))
	if err != nil || form != wire.Object || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, form %d, %v", got, form, err)
	}
	// And back: the same document.
	out, err := c.Marshal(got, wire.Object)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(out, &a)
	_ = json.Unmarshal([]byte(theirs), &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("written as %s", out)
	}

	// A request carries no metadata, and its arrays even when empty.
	out, err = c.Marshal(openadr.ResourceGroup{Metadata: want.Metadata, Name: "g",
		Attributes: []openadr.ValuesMap{{Type: "A", Values: []openadr.Value{openadr.Bool(true)}}}}, wire.Request)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"resourceGroupName":"g","attributes":[{"type":"A","values":[true]}],"targets":[],"children":[]}` {
		t.Errorf("a request is written as %s", out)
	}
	got, form, err = c.Unmarshal(out)
	if err != nil || form != wire.Request || got.Name != "g" || got.Targets != nil || got.Children != nil || len(got.Attributes) != 1 {
		t.Errorf("read back %+v, form %d, %v", got, form, err)
	}
	// Null for an array, as OpenLEADR accepts it.
	if got, _, err := c.Unmarshal([]byte(`{"resourceGroupName":"g","targets":null,"children":null}`)); err != nil || got.Name != "g" {
		t.Errorf("nulls: %+v, %v", got, err)
	}
	for _, bad := range []string{`[]`, `{"resourceGroupName":7}`, `{"resourceGroupName":"g","attributes":[{"type":"A"}]}`, `{"children":[{"type":1}]}`} {
		if _, _, err := c.Unmarshal([]byte(bad)); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
	// In a notification, as OpenLEADR sends one.
	n, err := Codec().UnmarshalNotification([]byte(`{"id":"op-1","objectType":"RESOURCE_GROUP","operation":"DELETE","object":` + theirs + `}`))
	if g, ok := n.Object.(openadr.ResourceGroup); err != nil || !ok || g.ID != want.ID || n.ID != "op-1" {
		t.Errorf("notification %+v, %v", n, err)
	}
	if body, err := Codec().MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectResourceGroup, Operation: openadr.OperationCreate, Object: want}); err != nil || !strings.Contains(string(body), `"resourceGroupName"`) || strings.Contains(string(body), `"id":"op`) {
		t.Errorf("a notification of a group: %s, %v", body, err)
	}
}

func TestMechanismCodec(t *testing.T) {
	c := Codec().Subscriptions
	sub := openadr.Subscription{ClientName: "c", ObjectOperations: []openadr.ObjectOperation{
		{Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate}, Mechanism: openadr.MechanismWebSocket},
		{Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate}, Mechanism: openadr.MechanismWebhook, CallbackURL: "https://example.com/hook"},
		{Objects: []openadr.ObjectType{}, Operations: []openadr.Operation{}, CallbackURL: "https://example.com/hook"},
	}}
	out, err := c.Marshal(sub, wire.Request)
	if err != nil {
		t.Fatal(err)
	}
	// A webhook is written as 3.1.0 has it: without a mechanism. A
	// WebSocket has no callback URL to write.
	want := `{"clientName":"c","objectOperations":[` +
		`{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET"},` +
		`{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"https://example.com/hook"},` +
		`{"objects":[],"operations":[],"callbackUrl":"https://example.com/hook"}]}`
	if string(out) != want {
		t.Errorf("written as\n%s\nwant\n%s", out, want)
	}
	got, _, err := c.Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if m := got.ObjectOperations; m[0].Mechanism != openadr.MechanismWebSocket || m[1].Mechanism != "" || m[0].CallbackURL != "" {
		t.Errorf("read back %+v", m)
	}
	// As OpenLEADR writes a webhook: with the mechanism named.
	got, _, err = c.Unmarshal([]byte(`{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBHOOK","callbackUrl":"https://e.example/"}]}`))
	if err != nil || got.ObjectOperations[0].Mechanism != "" || got.Validate() != nil {
		t.Errorf("an explicit WEBHOOK: %+v, %v", got, err)
	}
}

func TestNotifiersCodec(t *testing.T) {
	c := Codec()
	// A 3.1.0 VTN does not name what 3.1.0 does not have.
	if out, _ := c.MarshalNotifiers(openadr.Notifiers{Webhook: true}); string(out) != `{"WEBHOOK":true}` {
		t.Errorf("written as %s", out)
	}
	out, _ := c.MarshalNotifiers(openadr.Notifiers{WebSocket: true})
	if string(out) != `{"WEBHOOK":false,"WEBSOCKET":true}` {
		t.Errorf("written as %s", out)
	}
	// OpenLEADR's answer has no WEBHOOK at all.
	n, err := c.UnmarshalNotifiers([]byte(`{"WEBSOCKET":true,"MQTT":null}`))
	if err != nil || !n.WebSocket || n.Webhook || n.MQTT != nil {
		t.Errorf("read %+v, %v", n, err)
	}
}

// The start of an interval period that the User Guide writes for "now".
func TestBeginningOfTime(t *testing.T) {
	c := Codec().Events
	for _, start := range []string{"0001-01-01", "0001-01-01T00:00:00", "0001-01-01T00:00:00Z"} {
		e, _, err := c.Unmarshal([]byte(`{"programID":"p","intervalPeriod":{"start":"` + start + `","duration":"PT1H"},
			"intervals":[{"id":0,"intervalPeriod":{"start":"` + start + `"},"payloads":[]}]}`))
		if err != nil {
			t.Errorf("%s: %v", start, err)
			continue
		}
		if s := e.IntervalPeriod.Start; s == nil || !s.IsZero() || !e.Intervals[0].IntervalPeriod.Start.IsZero() {
			t.Errorf("%s was read as %v", start, s)
		}
		// It is written as a date-time, which is what the schema asks for.
		out, err := c.Marshal(e, wire.Request)
		if err != nil || !strings.Contains(string(out), `"start":"0001-01-01T00:00:00Z"`) {
			t.Errorf("%s was written as %s, %v", start, out, err)
		}
	}
	// Nothing else is taken that is not a date-time.
	for _, start := range []string{"2030-01-01", "2030-01-01T00:00:00", "now", "0001-01-02"} {
		if _, _, err := c.Unmarshal([]byte(`{"programID":"p","intervalPeriod":{"start":"` + start + `"}}`)); err == nil {
			t.Errorf("the start %q was read", start)
		}
	}
	if _, _, err := c.Unmarshal([]byte(`{"programID":"p","intervalPeriod":{"start":7}}`)); err == nil {
		t.Error("a number was read as a start")
	}
}
