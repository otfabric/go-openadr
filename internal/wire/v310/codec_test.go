// SPDX-License-Identifier: MIT

package v310

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

func ptr[T any](v T) *T { return &v }

var (
	t0   = time.Date(2023, 6, 15, 9, 30, 0, 0, time.UTC)
	meta = openadr.Metadata{ID: "object-999", Created: t0, Modified: t0.Add(time.Minute)}
	hour = &openadr.Duration{Hours: 1}
)

func roundTrip[T any](t *testing.T, c wire.ObjectCodec[T], v T) {
	t.Helper()
	data, err := c.Marshal(v, wire.Object)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, form, err := c.Unmarshal(data)
	if err != nil || form != wire.Object {
		t.Fatalf("Unmarshal(%s) = form %v, %v", data, form, err)
	}
	if !reflect.DeepEqual(got, v) {
		t.Errorf("round trip through %s:\n got %+v\nwant %+v", data, got, v)
	}
	list, err := c.MarshalList([]T{v, v})
	if err != nil {
		t.Fatal(err)
	}
	if back, err := c.UnmarshalList(list); err != nil || len(back) != 2 || !reflect.DeepEqual(back[1], v) {
		t.Errorf("list round trip = %+v, %v", back, err)
	}
}

func TestRoundTrip(t *testing.T) {
	c := Codec()
	values := []openadr.ValuesMap{{Type: "MIXED", Values: []openadr.Value{
		openadr.Number(0.17), openadr.Number(-3), openadr.String("x"), openadr.Bool(true), openadr.PointValue(1, 2.5),
	}}}
	intervals := []openadr.Interval{
		{ID: 0, Payloads: values},
		{ID: -1, IntervalPeriod: &openadr.IntervalPeriod{Start: &t0, Duration: hour, RandomizeStart: &openadr.Duration{Minutes: 5}}, Payloads: []openadr.ValuesMap{}},
	}
	roundTrip(t, c.Programs, openadr.Program{
		Metadata:       meta,
		Name:           "ResTOU",
		IntervalPeriod: &openadr.IntervalPeriod{Start: &t0, Duration: &openadr.Infinite},
		Descriptions:   []openadr.ProgramDescription{{URL: "https://example.com/p"}},
		PayloadDescriptors: []openadr.PayloadDescriptor{
			openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "USD"},
			openadr.ReportPayloadDescriptor{PayloadType: "USAGE", ReadingType: "DIRECT_READ", Units: "KWH", Accuracy: ptr(0.5), Confidence: ptr(90)},
		},
		Attributes: values,
		Targets:    []string{"group-1"},
	})
	roundTrip(t, c.Programs, openadr.Program{Metadata: meta, Name: "minimal"})

	descriptor := openadr.NewReportDescriptor("USAGE")
	descriptor.Targets, descriptor.Historical, descriptor.Repeat = []string{"r1"}, false, -1
	roundTrip(t, c.Events, openadr.Event{
		Metadata: meta, ProgramID: "program-1", Name: "e", Duration: hour, Priority: ptr(0),
		Targets:            []string{"group-1"},
		ReportDescriptors:  []openadr.ReportDescriptor{descriptor, openadr.NewReportDescriptor("X")},
		PayloadDescriptors: []openadr.EventPayloadDescriptor{{PayloadType: "PRICE"}},
		IntervalPeriod:     &openadr.IntervalPeriod{Start: &t0},
		Intervals:          intervals,
	})
	roundTrip(t, c.Reports, openadr.Report{
		Metadata: meta, ClientID: "client", EventID: "event-1", ClientName: "ven", Name: "r",
		PayloadDescriptors: []openadr.ReportPayloadDescriptor{{PayloadType: "USAGE"}},
		Resources: []openadr.ReportResource{
			{ResourceName: openadr.AggregatedReport, IntervalPeriod: &openadr.IntervalPeriod{Duration: hour}, Intervals: intervals},
			{ResourceName: "empty", Intervals: []openadr.Interval{}},
		},
	})
	roundTrip(t, c.Reports, openadr.Report{Metadata: meta, EventID: "e", ClientName: "c", Resources: []openadr.ReportResource{}})
	roundTrip(t, c.VENs, openadr.VEN{Metadata: meta, ClientID: "client", Name: "VEN-999", Attributes: values, Targets: []string{"a", "b"}})
	roundTrip(t, c.Resources, openadr.Resource{Metadata: meta, ClientID: "client", VENID: "ven-1", Name: "RESOURCE-999", Targets: []string{"a"}})
	roundTrip(t, c.Subscriptions, subscriptionFixture)
	roundTrip(t, c.Subscriptions, openadr.Subscription{Metadata: meta, ClientName: "c", ObjectOperations: []openadr.ObjectOperation{}})
}

var subscriptionFixture = openadr.Subscription{
	Metadata: meta, ClientID: "client", ClientName: "VEN-999", ProgramID: "program-1", Targets: []string{"group-1"},
	ObjectOperations: []openadr.ObjectOperation{{
		Objects:     []openadr.ObjectType{openadr.ObjectEvent, openadr.ObjectProgram},
		Operations:  []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate},
		CallbackURL: "https://myserver.com/send/callback/here", BearerToken: "NCEJGI9E8ER9802UT9HUG",
	}},
}

func TestNotification(t *testing.T) {
	c := Codec()
	for _, object := range []any{
		openadr.Program{Metadata: meta, Name: "p", Targets: []string{"a"}},
		openadr.Event{Metadata: meta, ProgramID: "p1", Name: "e"},
		openadr.Report{Metadata: meta, ClientID: "c", EventID: "e1", ClientName: "n", Resources: []openadr.ReportResource{}},
		subscriptionFixture,
		openadr.VEN{Metadata: meta, ClientID: "c", Name: "v"},
		openadr.Resource{Metadata: meta, ClientID: "c", VENID: "v1", Name: "r"},
	} {
		kind := map[string]openadr.ObjectType{
			"Program": openadr.ObjectProgram, "Event": openadr.ObjectEvent, "Report": openadr.ObjectReport,
			"Subscription": openadr.ObjectSubscription, "VEN": openadr.ObjectVEN, "Resource": openadr.ObjectResource,
		}[reflect.TypeOf(object).Name()]
		n := openadr.Notification{ObjectType: kind, Operation: openadr.OperationDelete, Object: object, Targets: []string{"a"}}
		data, err := c.MarshalNotification(n)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		// The object inside says what it is, as the schema's discriminator needs.
		var shape struct {
			ObjectType string `json:"objectType"`
			Object     struct {
				ObjectType string `json:"objectType"`
			} `json:"object"`
		}
		if err := json.Unmarshal(data, &shape); err != nil || shape.ObjectType != string(kind) || shape.Object.ObjectType != string(kind) {
			t.Errorf("%s: wire form %s", kind, data)
		}
		back, err := c.UnmarshalNotification(data)
		if err != nil || !reflect.DeepEqual(back, n) {
			t.Errorf("%s round trip = %+v, %v", kind, back, err)
		}
	}
	if _, err := c.MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectEvent, Object: "an event"}); err == nil {
		t.Error("a notification that carries a string was encoded")
	}
	if _, err := c.MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectEvent, Object: &openadr.Event{}}); err == nil {
		t.Error("a notification that carries a pointer was encoded; the documented type is the value")
	}
	for _, body := range []string{
		`{"objectType":"EVENT","object":{"programID":"p"}}`,
		`{"operation":"CREATE","object":{"programID":"p"}}`,
		`{"objectType":"EVENT","operation":"CREATE"}`,
		`{"objectType":"EVENT","operation":"CREATE","object":null}`,
		`{"objectType":"SUBSCRIPTION","operation":"CREATE","object":{"clientName":"c"}}`,
		`{"objectType":"SUBSCRIPTION","operation":"CREATE","object":{"clientName":"c","objectOperations":[{"operations":[],"callbackUrl":"https://x"}]}}`,
	} {
		if n, err := c.UnmarshalNotification([]byte(body)); err == nil {
			t.Errorf("%s decoded to %+v", body, n)
		}
	}
}

// What goes on the wire, checked against the property names and required
// members of the 3.1.0 schemas.
func TestWireShape(t *testing.T) {
	c := Codec()
	asMap := func(data []byte, err error) map[string]any {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	obj := asMap(c.Programs.Marshal(openadr.Program{Metadata: meta, Name: "p"}, wire.Object))
	want := map[string]any{
		"id": "object-999", "objectType": "PROGRAM", "programName": "p",
		"createdDateTime": "2023-06-15T09:30:00Z", "modificationDateTime": "2023-06-15T09:31:00Z",
	}
	if !reflect.DeepEqual(obj, want) {
		t.Errorf("program object = %v, want %v", obj, want)
	}
	req := asMap(c.Programs.Marshal(openadr.Program{Metadata: meta, Name: "p"}, wire.Request))
	if !reflect.DeepEqual(req, map[string]any{"programName": "p"}) {
		t.Errorf("program request = %v; metadata must be left out", req)
	}

	ven := openadr.VEN{Metadata: meta, Name: "v", ClientID: "c", Targets: []string{"t"}}
	req = asMap(c.VENs.Marshal(ven, wire.Request))
	if !reflect.DeepEqual(req, map[string]any{"objectType": "VEN_VEN_REQUEST", "venName": "v"}) {
		t.Errorf("VEN request = %v", req)
	}
	req = asMap(c.VENs.Marshal(ven, wire.BLRequest))
	if req["objectType"] != "BL_VEN_REQUEST" || req["clientID"] != "c" || req["targets"] == nil || req["id"] != nil {
		t.Errorf("BL ven request = %v", req)
	}
	res := openadr.Resource{Name: "r", VENID: "v1", ClientID: "c"}
	if req = asMap(c.Resources.Marshal(res, wire.Request)); req["objectType"] != "VEN_RESOURCE_REQUEST" || req["clientID"] != nil {
		t.Errorf("VEN resource request = %v", req)
	}
	if req = asMap(c.Resources.Marshal(res, wire.BLRequest)); req["objectType"] != "BL_RESOURCE_REQUEST" || req["clientID"] != "c" {
		t.Errorf("BL resource request = %v", req)
	}

	// Required arrays are written even when empty.
	rep := asMap(c.Reports.Marshal(openadr.Report{EventID: "e", ClientName: "c", Resources: []openadr.ReportResource{{ResourceName: "r"}}}, wire.Request))
	resources, _ := rep["resources"].([]any)
	if len(resources) != 1 || resources[0].(map[string]any)["intervals"] == nil {
		t.Errorf("report request = %v; resources[0].intervals must be present", rep)
	}
	rep = asMap(c.Reports.Marshal(openadr.Report{EventID: "e", ClientName: "c"}, wire.Request))
	if _, ok := rep["resources"].([]any); !ok {
		t.Errorf("report request = %v; resources must be an array", rep)
	}
	ev := asMap(c.Events.Marshal(openadr.Event{ProgramID: "p", Intervals: []openadr.Interval{{ID: 0}}}, wire.Request))
	iv := ev["intervals"].([]any)[0].(map[string]any)
	if iv["id"] != float64(0) || iv["payloads"] == nil {
		t.Errorf("interval = %v; id 0 and payloads must be present", iv)
	}
}

func TestUnmarshal(t *testing.T) {
	c := Codec()

	// From the specification's examples; nulls and unknown properties are tolerated.
	e, _, err := c.Events.Unmarshal([]byte(`{
		"id": "object-999", "createdDateTime": "2023-06-15T09:30:00Z", "modificationDateTime": "2023-06-15T09:30:00Z",
		"objectType": "EVENT", "programID": "object-999", "eventName": "price event 11-18-2022", "priority": 0,
		"targets": null, "duration": null, "x-vendor": {"anything": 1},
		"reportDescriptors": [{"payloadType": "USAGE", "readingType": null}],
		"payloadDescriptors": [{"objectType": "EVENT_PAYLOAD_DESCRIPTOR", "payloadType": "PRICE", "units": "KWH", "currency": "USD"}],
		"intervalPeriod": {"start": "2023-06-15T09:30:00+02:00", "duration": "PT1H", "randomizeStart": "PT1H"},
		"intervals": [{"id": 0, "payloads": [{"type": "PRICE", "values": [0.17, 1, "a", false, {"x": 1.0, "y": 2.0}]}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "object-999" || *e.Priority != 0 || e.Targets != nil || e.Duration != nil || len(e.Intervals[0].Payloads[0].Values) != 5 {
		t.Errorf("event = %+v", e)
	}
	if !reflect.DeepEqual(e.ReportDescriptors[0], openadr.NewReportDescriptor("USAGE")) {
		t.Errorf("report descriptor = %+v; absent properties must take the schema defaults", e.ReportDescriptors[0])
	}
	if got := e.IntervalPeriod.Start.UTC(); !got.Equal(time.Date(2023, 6, 15, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("start = %v", got)
	}

	if _, form, err := c.VENs.Unmarshal([]byte(`{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"c"}`)); err != nil || form != wire.BLRequest {
		t.Errorf("BL_VEN_REQUEST: form %v, %v", form, err)
	}
	if _, form, err := c.Resources.Unmarshal([]byte(`{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"r","venID":"v"}`)); err != nil || form != wire.Request {
		t.Errorf("VEN_RESOURCE_REQUEST: form %v, %v", form, err)
	}

	// OpenLEADR v0.2.7 sends ven objects without objectType.
	v, form, err := c.VENs.Unmarshal([]byte(`{"id":"v1","createdDateTime":"2026-10-12T19:26:17.029184+00:00","modificationDateTime":"2026-10-12T19:26:17.029184+00:00","clientID":"c","targets":[],"venName":"v"}`))
	if err != nil || form != wire.Object || v.ID != "v1" || v.ClientID != "c" || v.Created.IsZero() {
		t.Errorf("ven without objectType = %+v, form %v, %v", v, form, err)
	}

	bad := []struct{ name, field, body string }{
		{"report", "resources", `{"eventID":"e","clientName":"c"}`},
		{"report", "resources[0].intervals", `{"eventID":"e","clientName":"c","resources":[{"resourceName":"r"}]}`},
		{"event", "intervals[0].id", `{"programID":"p","intervals":[{"payloads":[]}]}`},
		{"event", "intervals[0].payloads", `{"programID":"p","intervals":[{"id":1}]}`},
		{"event", "intervals[0].payloads[0].values", `{"programID":"p","intervals":[{"id":1,"payloads":[{"type":"X"}]}]}`},
		{"event", "intervals[0].payloads[0].values[0]", `{"programID":"p","intervals":[{"id":1,"payloads":[{"type":"X","values":[null]}]}]}`},
		{"event", "intervals[0].payloads[0].values[0]", `{"programID":"p","intervals":[{"id":1,"payloads":[{"type":"X","values":[{"x":1}]}]}]}`},
		{"event", "duration", `{"programID":"p","duration":"1 hour"}`},
		{"event", "intervalPeriod.duration", `{"programID":"p","intervalPeriod":{"duration":"PT"}}`},
		{"event", "body", `{"programID":"p","intervalPeriod":{"start":"yesterday"}}`},
		{"event", "body", `{"programID":7}`},
		{"event", "body", `[]`},
		{"program", "payloadDescriptors[0].objectType", `{"programName":"p","payloadDescriptors":[{"payloadType":"PRICE"}]}`},
		{"program", "payloadDescriptors[0].objectType", `{"programName":"p","payloadDescriptors":[{"objectType":"OTHER","payloadType":"PRICE"}]}`},
	}
	for _, tc := range bad {
		var err error
		switch tc.name {
		case "report":
			_, _, err = c.Reports.Unmarshal([]byte(tc.body))
		case "event":
			_, _, err = c.Events.Unmarshal([]byte(tc.body))
		case "program":
			_, _, err = c.Programs.Unmarshal([]byte(tc.body))
		case "ven":
			_, _, err = c.VENs.Unmarshal([]byte(tc.body))
		case "resource":
			_, _, err = c.Resources.Unmarshal([]byte(tc.body))
		}
		var invalid *openadr.ValidationError
		if !errors.As(err, &invalid) || invalid.Field != tc.field {
			t.Errorf("%s %s: error %v, want a ValidationError for %s", tc.name, tc.body, err, tc.field)
		}
	}
	if _, err := c.Events.UnmarshalList([]byte(`[{"programID":"p"},{"programID":"p","duration":"x"}]`)); err == nil || !strings.Contains(err.Error(), "item 1") {
		t.Errorf("list with a bad item: %v", err)
	}
}

func FuzzUnmarshalEvent(f *testing.F) {
	f.Add([]byte(`{"programID":"p","intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.17,"a",true,{"x":1,"y":2}]}]}]}`))
	f.Add([]byte(`{"programID":"p","duration":"PT1H","reportDescriptors":[{"payloadType":"USAGE"}]}`))
	c := Codec()
	f.Fuzz(func(t *testing.T, data []byte) {
		e, _, err := c.Events.Unmarshal(data)
		if err != nil || e.Validate() != nil {
			return
		}
		// Whatever decodes and validates must encode, and decode to the same.
		out, err := c.Events.Marshal(e, wire.Object)
		if err != nil {
			t.Fatalf("Marshal of a decoded event: %v", err)
		}
		again, _, err := c.Events.Unmarshal(out)
		if err != nil {
			t.Fatalf("re-decoding %s: %v", out, err)
		}
		if out2, _ := c.Events.Marshal(again, wire.Object); string(out2) != string(out) {
			t.Fatalf("encoding is not stable:\n%s\n%s", out, out2)
		}
	})
}

// TestRefuses301 checks that what only OpenADR 3.0.1 has is refused, not
// dropped: typed targets, and the program of a report.
func TestRefuses301(t *testing.T) {
	c := Codec()
	typed := []openadr.ValuesMap{{Type: "GROUP", Values: []openadr.Value{openadr.String("g")}}}
	descriptor := openadr.NewReportDescriptor("USAGE")
	descriptor.TypedTargets = typed
	for name, tc := range map[string]struct {
		err   error
		field string
	}{
		"program": {func() error {
			_, err := c.Programs.Marshal(openadr.Program{Name: "p", TypedTargets: typed}, wire.BLRequest)
			return err
		}(), "targets"},
		"event": {func() error {
			_, err := c.Events.Marshal(openadr.Event{ProgramID: "p", TypedTargets: typed}, wire.BLRequest)
			return err
		}(), "targets"},
		"report descriptor": {func() error {
			_, err := c.Events.Marshal(openadr.Event{ProgramID: "p", ReportDescriptors: []openadr.ReportDescriptor{descriptor}}, wire.BLRequest)
			return err
		}(), "reportDescriptors[0].targets"},
		"report": {func() error {
			_, err := c.Reports.Marshal(openadr.Report{ProgramID: "p", EventID: "e", ClientName: "c"}, wire.Request)
			return err
		}(), "programID"},
		"VEN": {func() error {
			_, err := c.VENs.Marshal(openadr.VEN{Name: "v", TypedTargets: typed}, wire.BLRequest)
			return err
		}(), "targets"},
		"resource": {func() error {
			_, err := c.Resources.Marshal(openadr.Resource{Name: "r", TypedTargets: typed}, wire.BLRequest)
			return err
		}(), "targets"},
		"subscription": {func() error {
			_, err := c.Subscriptions.Marshal(openadr.Subscription{ClientName: "c", TypedTargets: typed}, wire.Request)
			return err
		}(), "targets"},
		"notification": {func() error {
			_, err := c.MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectVEN, Operation: openadr.OperationCreate, Object: openadr.VEN{Name: "v"}, TypedTargets: typed})
			return err
		}(), "targets"},
	} {
		var ve *openadr.ValidationError
		if !errors.As(tc.err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: %v, want a ValidationError of %s", name, tc.err, tc.field)
		}
	}
}
