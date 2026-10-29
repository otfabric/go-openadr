// SPDX-License-Identifier: MIT

package v301

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
	t0    = time.Date(2024, 7, 25, 8, 31, 10, 0, time.UTC)
	meta  = openadr.Metadata{ID: "object-999", Created: t0, Modified: t0.Add(time.Minute)}
	hour  = &openadr.Duration{Hours: 1}
	group = []openadr.ValuesMap{{Type: "GROUP", Values: []openadr.Value{openadr.String("group-1"), openadr.String("group-2")}}}
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

var programAttributes = []openadr.ValuesMap{
	{Type: "PROGRAM_LONG_NAME", Values: []openadr.Value{openadr.String("program long name")}},
	{Type: "RETAILER_NAME", Values: []openadr.Value{openadr.String("ACME")}},
	{Type: "RETAILER_LONG_NAME", Values: []openadr.Value{openadr.String("ACME Electric Inc.")}},
	{Type: "PROGRAM_TYPE", Values: []openadr.Value{openadr.String("PRICING_TARIFF")}},
	{Type: "COUNTRY", Values: []openadr.Value{openadr.String("US")}},
	{Type: "PRINCIPAL_SUBDIVISION", Values: []openadr.Value{openadr.String("CO")}},
	{Type: "BINDING_EVENTS", Values: []openadr.Value{openadr.Bool(false)}},
	{Type: "LOCAL", Values: []openadr.Value{openadr.Bool(true)}},
	{Type: "TIME_ZONE_OFFSET", Values: []openadr.Value{openadr.String("PT1H")}},
}

func TestRoundTrip(t *testing.T) {
	c := Codec()
	if c.Version != openadr.Version301 {
		t.Fatalf("Version = %s", c.Version)
	}
	values := []openadr.ValuesMap{{Type: "MIXED", Values: []openadr.Value{
		openadr.Number(0.17), openadr.String("x"), openadr.Bool(true), openadr.PointValue(1, 2.5),
	}}}
	intervals := []openadr.Interval{
		{ID: 0, Payloads: values},
		{ID: 1, IntervalPeriod: &openadr.IntervalPeriod{Start: &t0, Duration: hour, RandomizeStart: &openadr.Duration{Minutes: 5}}, Payloads: []openadr.ValuesMap{}},
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
		Attributes:   programAttributes,
		TypedTargets: group,
	})
	roundTrip(t, c.Programs, openadr.Program{Metadata: meta, Name: "minimal"})

	descriptor := openadr.NewReportDescriptor("USAGE")
	descriptor.TypedTargets, descriptor.Historical, descriptor.Repeat = group, false, -1
	roundTrip(t, c.Events, openadr.Event{
		Metadata: meta, ProgramID: "program-1", Name: "e", Priority: ptr(0),
		TypedTargets:       group,
		ReportDescriptors:  []openadr.ReportDescriptor{descriptor, openadr.NewReportDescriptor("X")},
		PayloadDescriptors: []openadr.EventPayloadDescriptor{{PayloadType: "PRICE"}},
		IntervalPeriod:     &openadr.IntervalPeriod{Start: &t0},
		Intervals:          intervals,
	})
	roundTrip(t, c.Events, openadr.Event{Metadata: meta, ProgramID: "program-1", Intervals: []openadr.Interval{}})
	roundTrip(t, c.Reports, openadr.Report{
		Metadata: meta, ProgramID: "program-1", EventID: "event-1", ClientName: "ven", Name: "r",
		PayloadDescriptors: []openadr.ReportPayloadDescriptor{{PayloadType: "USAGE"}},
		Resources: []openadr.ReportResource{
			{ResourceName: openadr.AggregatedReport, IntervalPeriod: &openadr.IntervalPeriod{Duration: hour}, Intervals: intervals},
			{ResourceName: "empty", Intervals: []openadr.Interval{}},
		},
	})
	roundTrip(t, c.VENs, openadr.VEN{Metadata: meta, Name: "VEN-999", Attributes: values, TypedTargets: group})
	roundTrip(t, c.Resources, openadr.Resource{Metadata: meta, VENID: "ven-1", Name: "RESOURCE-999", Attributes: values, TypedTargets: group})
	roundTrip(t, c.Subscriptions, subscriptionFixture)
}

var subscriptionFixture = openadr.Subscription{
	Metadata: meta, ClientName: "VEN-999", ProgramID: "program-1", TypedTargets: group,
	ObjectOperations: []openadr.ObjectOperation{{
		Objects:     []openadr.ObjectType{openadr.ObjectEvent, openadr.ObjectProgram},
		Operations:  []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate},
		CallbackURL: "https://myserver.com/send/callback/here", BearerToken: "NCEJGI9E8ER9802UT9HUG",
	}},
}

// TestWireShape checks the JSON against the schemas of 3.0.1: targets
// as values maps, the program properties that 3.1.0 made attributes, and
// requests without metadata.
func TestWireShape(t *testing.T) {
	c := Codec()
	data, err := c.Programs.Marshal(openadr.Program{Name: "p", Attributes: programAttributes, TypedTargets: group}, wire.BLRequest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"programName":"p","programLongName":"program long name","retailerName":"ACME","retailerLongName":"ACME Electric Inc.",` +
		`"programType":"PRICING_TARIFF","country":"US","principalSubdivision":"CO","timeZoneOffset":"PT1H","bindingEvents":false,` +
		`"localPrice":true,"targets":[{"type":"GROUP","values":["group-1","group-2"]}]}`
	if string(data) != want {
		t.Errorf("program request\n got %s\nwant %s", data, want)
	}

	data, err = c.Events.Marshal(openadr.Event{ProgramID: "p1"}, wire.BLRequest)
	if err != nil || string(data) != `{"programID":"p1","intervals":[]}` {
		t.Errorf("event request = %s, %v; intervals are required", data, err)
	}

	data, err = c.Reports.Marshal(openadr.Report{ClientID: "c", ProgramID: "p1", EventID: "e1", ClientName: "ven"}, wire.Request)
	if err != nil || string(data) != `{"programID":"p1","eventID":"e1","clientName":"ven","resources":[]}` {
		t.Errorf("report request = %s, %v", data, err)
	}

	// A VEN's own request leaves out the targets, which are Business Logic's.
	data, err = c.VENs.Marshal(openadr.VEN{Name: "v", TypedTargets: group}, wire.Request)
	if err != nil || string(data) != `{"venName":"v"}` {
		t.Errorf("VEN request = %s, %v", data, err)
	}
	data, err = c.Resources.Marshal(openadr.Resource{Name: "r", VENID: "v1", TypedTargets: group}, wire.Request)
	if err != nil || string(data) != `{"resourceName":"r","venID":"v1"}` {
		t.Errorf("resource request = %s, %v", data, err)
	}
	data, err = c.Resources.Marshal(openadr.Resource{Name: "r", VENID: "v1", TypedTargets: group}, wire.BLRequest)
	if err != nil || !strings.Contains(string(data), `"targets":[{"type":"GROUP"`) {
		t.Errorf("Business Logic's resource request = %s, %v", data, err)
	}
}

// TestRefused checks that what 3.0.1 cannot carry is refused, not dropped.
func TestRefused(t *testing.T) {
	c := Codec()
	strTargets := []string{"group-1"}
	cases := map[string]struct {
		err   error
		field string
	}{}
	check := func(name string, err error, field string) {
		cases[name] = struct {
			err   error
			field string
		}{err, field}
	}
	_, err := c.Programs.Marshal(openadr.Program{Name: "p", Targets: strTargets}, wire.BLRequest)
	check("program with string targets", err, "targets")
	_, err = c.Programs.Marshal(openadr.Program{Name: "p", Attributes: []openadr.ValuesMap{{Type: "COLOUR", Values: []openadr.Value{openadr.String("red")}}}}, wire.BLRequest)
	check("program attribute 3.0.1 has no property for", err, "attributes[0]")
	_, err = c.Programs.Marshal(openadr.Program{Name: "p", Attributes: []openadr.ValuesMap{{Type: "COUNTRY", Values: []openadr.Value{openadr.Number(1)}}}}, wire.BLRequest)
	check("COUNTRY that is not a string", err, "attributes[0]")
	_, err = c.Programs.Marshal(openadr.Program{Name: "p", Attributes: []openadr.ValuesMap{{Type: "LOCAL", Values: []openadr.Value{openadr.String("yes")}}}}, wire.BLRequest)
	check("LOCAL that is not a boolean", err, "attributes[0]")
	_, err = c.Programs.Marshal(openadr.Program{Name: "p", Attributes: []openadr.ValuesMap{{Type: "TIME_ZONE_OFFSET", Values: []openadr.Value{openadr.String("an hour")}}}}, wire.BLRequest)
	check("TIME_ZONE_OFFSET that is not a duration", err, "attributes[0]")
	twice := []openadr.ValuesMap{{Type: "COUNTRY", Values: []openadr.Value{openadr.String("US")}}, {Type: "COUNTRY", Values: []openadr.Value{openadr.String("NL")}}}
	_, err = c.Programs.Marshal(openadr.Program{Name: "p", Attributes: twice}, wire.BLRequest)
	check("an attribute twice", err, "attributes[1]")
	_, err = c.Events.Marshal(openadr.Event{ProgramID: "p1", Targets: strTargets}, wire.BLRequest)
	check("event with string targets", err, "targets")
	_, err = c.Events.Marshal(openadr.Event{ProgramID: "p1", Duration: hour}, wire.BLRequest)
	check("event duration", err, "duration")
	d := openadr.NewReportDescriptor("USAGE")
	d.ReportIntervals = openadr.ReportIntervalsOpen
	_, err = c.Events.Marshal(openadr.Event{ProgramID: "p1", ReportDescriptors: []openadr.ReportDescriptor{d}}, wire.BLRequest)
	check("open report intervals", err, "reportDescriptors[0].reportIntervals")
	d = openadr.NewReportDescriptor("USAGE")
	d.Targets = strTargets
	_, err = c.Events.Marshal(openadr.Event{ProgramID: "p1", ReportDescriptors: []openadr.ReportDescriptor{d}}, wire.BLRequest)
	check("report descriptor with string targets", err, "reportDescriptors[0].targets")
	_, err = c.Reports.Marshal(openadr.Report{EventID: "e1", ClientName: "c"}, wire.Request)
	check("report without programID", err, "programID")
	_, err = c.VENs.Marshal(openadr.VEN{Name: "v", ClientID: "client"}, wire.BLRequest)
	check("VEN with clientID", err, "clientID")
	_, err = c.VENs.Marshal(openadr.VEN{Name: "v", Targets: strTargets}, wire.BLRequest)
	check("VEN with string targets", err, "targets")
	_, err = c.Resources.Marshal(openadr.Resource{Name: "r", VENID: "v", ClientID: "client"}, wire.BLRequest)
	check("resource with clientID", err, "clientID")
	_, err = c.Subscriptions.Marshal(openadr.Subscription{ClientName: "c"}, wire.Request)
	check("subscription without programID", err, "programID")
	ws := openadr.Subscription{ClientName: "c", ProgramID: "p", ObjectOperations: []openadr.ObjectOperation{{Mechanism: openadr.MechanismWebSocket}}}
	_, err = c.Subscriptions.Marshal(ws, wire.Request)
	check("WebSocket subscription", err, "objectOperations[0].mechanism")
	_, err = c.MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectEvent, Operation: openadr.OperationCreate, Object: openadr.Event{ProgramID: "p"}, Targets: strTargets})
	check("notification with string targets", err, "targets")

	for name, tc := range cases {
		var ve *openadr.ValidationError
		if !errors.As(tc.err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: %v, want a ValidationError of %s", name, tc.err, tc.field)
		}
	}

	for name, err := range map[string]error{
		"resource groups": func() error { _, err := c.ResourceGroups.Marshal(openadr.ResourceGroup{}, wire.Request); return err }(),
		"notifiers":       func() error { _, err := c.UnmarshalNotifiers([]byte(`{}`)); return err }(),
		"topics":          func() error { _, err := c.UnmarshalTopics([]byte(`{}`)); return err }(),
		"auth server":     func() error { _, err := c.UnmarshalAuthServer([]byte(`{}`)); return err }(),
	} {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: %v, want errors.ErrUnsupported", name, err)
		}
	}
}

func TestNotification(t *testing.T) {
	c := Codec()
	for op, method := range map[openadr.Operation]string{
		openadr.OperationCreate: "POST", openadr.OperationRead: "GET", openadr.OperationUpdate: "PUT", openadr.OperationDelete: "DELETE",
	} {
		for _, object := range []any{
			openadr.Program{Metadata: meta, Name: "p", TypedTargets: group},
			openadr.Event{Metadata: meta, ProgramID: "p1", Intervals: []openadr.Interval{}},
			openadr.Report{Metadata: meta, ProgramID: "p1", EventID: "e1", ClientName: "n", Resources: []openadr.ReportResource{}},
			subscriptionFixture,
			openadr.VEN{Metadata: meta, Name: "v"},
			openadr.Resource{Metadata: meta, VENID: "v1", Name: "r"},
		} {
			kind := map[string]openadr.ObjectType{
				"Program": openadr.ObjectProgram, "Event": openadr.ObjectEvent, "Report": openadr.ObjectReport,
				"Subscription": openadr.ObjectSubscription, "VEN": openadr.ObjectVEN, "Resource": openadr.ObjectResource,
			}[reflect.TypeOf(object).Name()]
			n := openadr.Notification{ObjectType: kind, Operation: op, Object: object, TypedTargets: group}
			data, err := c.MarshalNotification(n)
			if err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
			var shape struct {
				Operation string `json:"operation"`
			}
			if err := json.Unmarshal(data, &shape); err != nil || shape.Operation != method {
				t.Errorf("%s %s: operation %q, want %s", kind, op, shape.Operation, method)
			}
			back, err := c.UnmarshalNotification(data)
			if err != nil || !reflect.DeepEqual(back, n) {
				t.Errorf("%s round trip = %+v, %v", kind, back, err)
			}
		}
	}
	for _, bad := range []string{
		`{"objectType":"EVENT","operation":"CREATE","object":{"programID":"p","intervals":[]}}`,
		`{"objectType":"EVENT","operation":"POST"}`,
		`{"objectType":"EVENT","object":{"programID":"p","intervals":[]}}`,
		`{"operation":"POST","object":{"programID":"p","intervals":[]}}`,
		`{"objectType":"THING","operation":"POST","object":{}}`,
		`{"objectType":"EVENT","operation":"POST","object":{"programID":"p"}}`,
	} {
		if _, err := c.UnmarshalNotification([]byte(bad)); !errors.Is(err, openadr.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := c.MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectEvent, Operation: "PATCH", Object: openadr.Event{}}); err == nil {
		t.Error("an operation that is not one was encoded")
	}
	if _, err := c.MarshalNotification(openadr.Notification{ObjectType: openadr.ObjectEvent, Operation: openadr.OperationRead, Object: openadr.ResourceGroup{}}); err == nil {
		t.Error("a resource group notification was encoded")
	}
}

// TestUnmarshal reads objects as a 3.0.1 VTN writes them: the program of
// OpenLEADR v0.1.4's fixtures, with nulls where 3.0.1 has nullable
// properties, and a VEN with its resources inline.
func TestUnmarshal(t *testing.T) {
	c := Codec()
	p, _, err := c.Programs.Unmarshal([]byte(`{
		"id": "program-1", "createdDateTime": "2024-07-25T08:31:10.776Z", "modificationDateTime": "2024-07-25T08:31:10.776Z",
		"objectType": "PROGRAM", "programName": "program-1", "programLongName": "program long name",
		"retailerName": "retailer name", "retailerLongName": null, "programType": "program type",
		"country": "country", "principalSubdivision": "principal-subdivision",
		"intervalPeriod": {"start": "2024-07-25T08:31:10.776Z"},
		"programDescriptions": [{"URL": "https://program-description-1.com"}],
		"bindingEvents": false, "localPrice": true,
		"payloadDescriptors": [{"objectType": "EVENT_PAYLOAD_DESCRIPTOR", "payloadType": "EXPORT_PRICE"}],
		"targets": [{"type": "GROUP", "values": ["group-1"]}, {"type": "PRIVATE_LABEL", "values": ["private value"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "program-1" || p.Name != "program-1" || len(p.TypedTargets) != 2 || p.TypedTargets[1].Type != "PRIVATE_LABEL" || p.Targets != nil {
		t.Errorf("program = %+v", p)
	}
	var types []string
	for _, a := range p.Attributes {
		types = append(types, a.Type)
	}
	if got := strings.Join(types, ","); got != "PROGRAM_LONG_NAME,RETAILER_NAME,PROGRAM_TYPE,COUNTRY,PRINCIPAL_SUBDIVISION,BINDING_EVENTS,LOCAL" {
		t.Errorf("attributes = %s", got)
	}

	v, _, err := c.VENs.Unmarshal([]byte(`{"id":"ven-1","venName":"ven-1","targets":[{"type":"GROUP","values":["group-1"]}],
		"resources":[{"id":"r1","resourceName":"r1","venID":"ven-1"}]}`))
	if err != nil || v.Name != "ven-1" || len(v.TypedTargets) != 1 {
		t.Errorf("VEN = %+v, %v", v, err)
	}

	for name, body := range map[string]string{
		"event without intervals":    `{"programID":"p"}`,
		"report without resources":   `{"programID":"p","eventID":"e","clientName":"c"}`,
		"targets that are strings":   `{"programName":"p","targets":["group-1"]}`,
		"a bad timeZoneOffset":       `{"programName":"p","timeZoneOffset":"an hour"}`,
		"subscription without oo":    `{"programID":"p","clientName":"c"}`,
		"oo without objects":         `{"programID":"p","clientName":"c","objectOperations":[{"operations":[],"callbackUrl":"https://x"}]}`,
		"oo without operations":      `{"programID":"p","clientName":"c","objectOperations":[{"objects":[],"callbackUrl":"https://x"}]}`,
		"target without values":      `{"venName":"v","targets":[{"type":"GROUP"}]}`,
		"resource with a bad target": `{"resourceName":"r","targets":[{"type":"GROUP","values":[null]}]}`,
	} {
		var err error
		switch {
		case strings.HasPrefix(name, "event"):
			_, _, err = c.Events.Unmarshal([]byte(body))
		case strings.HasPrefix(name, "report"):
			_, _, err = c.Reports.Unmarshal([]byte(body))
		case strings.HasPrefix(name, "subscription"), strings.HasPrefix(name, "oo"):
			_, _, err = c.Subscriptions.Unmarshal([]byte(body))
		case strings.HasPrefix(name, "target"):
			_, _, err = c.VENs.Unmarshal([]byte(body))
		case strings.HasPrefix(name, "resource"):
			_, _, err = c.Resources.Unmarshal([]byte(body))
		default:
			_, _, err = c.Programs.Unmarshal([]byte(body))
		}
		if !errors.Is(err, openadr.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}

	problem, err := c.UnmarshalProblem([]byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"A foreign key constraint is violated"}`))
	if err != nil || problem.Status != 409 || problem.Detail == "" {
		t.Errorf("problem = %+v, %v", problem, err)
	}
	if b, err := c.MarshalProblem(problem); err != nil || !strings.Contains(string(b), `"status":409`) {
		t.Errorf("MarshalProblem = %s, %v", b, err)
	}
}
