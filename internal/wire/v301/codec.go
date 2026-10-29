// SPDX-License-Identifier: MIT

package v301

import (
	"encoding/json"
	"errors"
	"fmt"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
	"github.com/otfabric/go-openadr/internal/wire/common"
)

var (
	invalid  = common.Invalid
	decode   = common.Decode
	metaTo   = common.MetaTo
	metaFrom = common.MetaFrom
)

func mapSlice[A, B any](in []A, f func(A) B) []B { return common.MapSlice(in, f) }

// errNotOf301 is what the parts of a codec that 3.0.1 does not have return.
var errNotOf301 = fmt.Errorf("not in OpenADR 3.0.1: %w", errors.ErrUnsupported)

// Codec returns the OpenADR 3.0.1 representation. It has no resource
// groups, notifiers, MQTT topics or authorization server information:
// those are of 3.1.0 or its extensions, and return errors.ErrUnsupported.
func Codec() *wire.Codec {
	return &wire.Codec{
		Version:   openadr.Version301,
		Programs:  wire.ObjectCodec[openadr.Program]{Marshal: marshalProgram, Unmarshal: unmarshalProgram},
		Events:    wire.ObjectCodec[openadr.Event]{Marshal: marshalEvent, Unmarshal: unmarshalEvent},
		Reports:   wire.ObjectCodec[openadr.Report]{Marshal: marshalReport, Unmarshal: unmarshalReport},
		VENs:      wire.ObjectCodec[openadr.VEN]{Marshal: marshalVEN, Unmarshal: unmarshalVEN},
		Resources: wire.ObjectCodec[openadr.Resource]{Marshal: marshalResource, Unmarshal: unmarshalResource},

		ResourceGroups: wire.ObjectCodec[openadr.ResourceGroup]{
			Marshal:   func(openadr.ResourceGroup, wire.Form) ([]byte, error) { return nil, errNotOf301 },
			Unmarshal: func([]byte) (openadr.ResourceGroup, wire.Form, error) { return openadr.ResourceGroup{}, 0, errNotOf301 },
		},

		Subscriptions:         wire.ObjectCodec[openadr.Subscription]{Marshal: marshalSubscription, Unmarshal: unmarshalSubscription},
		MarshalNotification:   marshalNotification,
		UnmarshalNotification: unmarshalNotification,

		MarshalNotifiers:    func(openadr.Notifiers) ([]byte, error) { return nil, errNotOf301 },
		UnmarshalNotifiers:  func([]byte) (openadr.Notifiers, error) { return openadr.Notifiers{}, errNotOf301 },
		MarshalTopics:       func(openadr.Topics) ([]byte, error) { return nil, errNotOf301 },
		UnmarshalTopics:     func([]byte) (openadr.Topics, error) { return openadr.Topics{}, errNotOf301 },
		MarshalAuthServer:   func(string) ([]byte, error) { return nil, errNotOf301 },
		UnmarshalAuthServer: func([]byte) (string, error) { return "", errNotOf301 },

		MarshalProblem:   common.MarshalProblem,
		UnmarshalProblem: common.UnmarshalProblem,
	}
}

// --- targets

// targetsTo writes the typed targets, and refuses the string targets of
// 3.1.0, which 3.0.1 cannot carry.
func targetsTo(field string, targets []string, typed []openadr.ValuesMap) ([]valuesMap, error) {
	if len(targets) > 0 {
		return nil, invalid(field, "are strings, the targets of OpenADR 3.1.0; 3.0.1 targets are values maps (TypedTargets)")
	}
	return common.ValuesMapsTo(typed)
}

// --- program

// The program properties of 3.0.1 that 3.1.0 made program attributes, by
// the attribute type 3.1.0 gives each. timeZoneOffset has no 3.1.0
// attribute; TIME_ZONE_OFFSET is this module's.
const (
	attrProgramLongName      = "PROGRAM_LONG_NAME"
	attrRetailerName         = "RETAILER_NAME"
	attrRetailerLongName     = "RETAILER_LONG_NAME"
	attrProgramType          = "PROGRAM_TYPE"
	attrCountry              = "COUNTRY"
	attrPrincipalSubdivision = "PRINCIPAL_SUBDIVISION"
	attrBindingEvents        = "BINDING_EVENTS"
	attrLocal                = "LOCAL"
	attrTimeZoneOffset       = "TIME_ZONE_OFFSET"
)

func marshalProgram(p openadr.Program, form wire.Form) ([]byte, error) {
	w := program{
		ProgramName:    p.Name,
		IntervalPeriod: common.PeriodTo(p.IntervalPeriod),
		ProgramDescriptions: mapSlice(p.Descriptions, func(d openadr.ProgramDescription) programDescription {
			return programDescription(d)
		}),
	}
	if form == wire.Object {
		w.metadata = metaTo(p.Metadata, openadr.ObjectProgram)
	}
	var err error
	if w.Targets, err = targetsTo("targets", p.Targets, p.TypedTargets); err != nil {
		return nil, err
	}
	if err := attributesTo(&w, p.Attributes); err != nil {
		return nil, err
	}
	if w.PayloadDescriptors, err = common.PayloadDescriptorsTo(p.PayloadDescriptors); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

// attributesTo writes the program attributes as the properties 3.0.1 has
// for them. 3.0.1 has no others.
func attributesTo(w *program, attrs []openadr.ValuesMap) error {
	seen := map[string]bool{}
	for i, a := range attrs {
		f := fmt.Sprintf("attributes[%d]", i)
		if seen[a.Type] {
			return invalid(f, "is a second %s", a.Type)
		}
		seen[a.Type] = true
		var target *string
		switch a.Type {
		case attrProgramLongName:
			target = &w.ProgramLongName
		case attrRetailerName:
			target = &w.RetailerName
		case attrRetailerLongName:
			target = &w.RetailerLongName
		case attrProgramType:
			target = &w.ProgramType
		case attrCountry:
			target = &w.Country
		case attrPrincipalSubdivision:
			target = &w.PrincipalSubdivision
		case attrBindingEvents, attrLocal:
			b, ok := one(a).Bool()
			if !ok {
				return invalid(f, "%s must have one boolean value", a.Type)
			}
			if a.Type == attrBindingEvents {
				w.BindingEvents = &b
			} else {
				w.LocalPrice = &b
			}
			continue
		case attrTimeZoneOffset:
			s, ok := one(a).Text()
			if _, err := openadr.ParseDuration(s); !ok || err != nil {
				return invalid(f, "%s must have one value, an ISO 8601 duration", a.Type)
			}
			w.TimeZoneOffset = &s
			continue
		default:
			return invalid(f, "is of type %q: an OpenADR 3.0.1 program has the attributes %s, %s, %s, %s, %s, %s, %s, %s and %s only",
				a.Type, attrProgramLongName, attrRetailerName, attrRetailerLongName, attrProgramType, attrCountry,
				attrPrincipalSubdivision, attrBindingEvents, attrLocal, attrTimeZoneOffset)
		}
		s, ok := one(a).Text()
		if !ok || s == "" {
			return invalid(f, "%s must have one string value", a.Type)
		}
		*target = s
	}
	return nil
}

// one is the value of a values map that has exactly one, or the zero Value.
func one(a openadr.ValuesMap) openadr.Value {
	if len(a.Values) != 1 {
		return openadr.Value{}
	}
	return a.Values[0]
}

func attributesFrom(w program) []openadr.ValuesMap {
	var out []openadr.ValuesMap
	text := func(typ, s string) {
		if s != "" {
			out = append(out, openadr.ValuesMap{Type: typ, Values: []openadr.Value{openadr.String(s)}})
		}
	}
	flag := func(typ string, b *bool) {
		if b != nil {
			out = append(out, openadr.ValuesMap{Type: typ, Values: []openadr.Value{openadr.Bool(*b)}})
		}
	}
	text(attrProgramLongName, w.ProgramLongName)
	text(attrRetailerName, w.RetailerName)
	text(attrRetailerLongName, w.RetailerLongName)
	text(attrProgramType, w.ProgramType)
	text(attrCountry, w.Country)
	text(attrPrincipalSubdivision, w.PrincipalSubdivision)
	flag(attrBindingEvents, w.BindingEvents)
	flag(attrLocal, w.LocalPrice)
	if w.TimeZoneOffset != nil {
		text(attrTimeZoneOffset, *w.TimeZoneOffset)
	}
	return out
}

func unmarshalProgram(data []byte) (openadr.Program, wire.Form, error) {
	var w program
	if err := decode(data, &w); err != nil {
		return openadr.Program{}, 0, err
	}
	p := openadr.Program{
		Metadata: metaFrom(w.metadata),
		Name:     w.ProgramName,
		Descriptions: mapSlice(w.ProgramDescriptions, func(d programDescription) openadr.ProgramDescription {
			return openadr.ProgramDescription(d)
		}),
		Attributes: attributesFrom(w),
	}
	if w.TimeZoneOffset != nil {
		if _, err := common.DurationFrom("timeZoneOffset", w.TimeZoneOffset); err != nil {
			return openadr.Program{}, 0, err
		}
	}
	var err error
	if p.IntervalPeriod, err = common.PeriodFrom("intervalPeriod", w.IntervalPeriod); err != nil {
		return openadr.Program{}, 0, err
	}
	if p.TypedTargets, err = common.ValuesMapsFrom("targets", w.Targets); err != nil {
		return openadr.Program{}, 0, err
	}
	if p.PayloadDescriptors, err = common.PayloadDescriptorsFrom("payloadDescriptors", w.PayloadDescriptors); err != nil {
		return openadr.Program{}, 0, err
	}
	return p, wire.Object, nil
}

// --- event

func marshalEvent(e openadr.Event, form wire.Form) ([]byte, error) {
	if e.Duration != nil {
		return nil, invalid("duration", "is of OpenADR 3.1.0; a 3.0.1 event has none")
	}
	w := event{
		ProgramID:          e.ProgramID,
		EventName:          e.Name,
		Priority:           e.Priority,
		PayloadDescriptors: mapSlice(e.PayloadDescriptors, common.EventDescriptorTo),
		IntervalPeriod:     common.PeriodTo(e.IntervalPeriod),
	}
	if form == wire.Object {
		w.metadata = metaTo(e.Metadata, openadr.ObjectEvent)
	}
	var err error
	if w.Targets, err = targetsTo("targets", e.Targets, e.TypedTargets); err != nil {
		return nil, err
	}
	for i, d := range e.ReportDescriptors {
		f := fmt.Sprintf("reportDescriptors[%d]", i)
		if d.ReportIntervals != "" && d.ReportIntervals != openadr.ReportIntervalsIntervals {
			return nil, invalid(f+".reportIntervals", "is %s; OpenADR 3.0.1 reports on intervals only", d.ReportIntervals)
		}
		rd := reportDescriptor{ReportDescriptorCommon: common.RequestDescriptorTo(d)}
		if rd.Targets, err = targetsTo(f+".targets", d.Targets, d.TypedTargets); err != nil {
			return nil, err
		}
		w.ReportDescriptors = append(w.ReportDescriptors, rd)
	}
	if w.Intervals, err = common.IntervalsTo(e.Intervals); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalEvent(data []byte) (openadr.Event, wire.Form, error) {
	var w event
	if err := decode(data, &w); err != nil {
		return openadr.Event{}, 0, err
	}
	if w.Intervals == nil {
		return openadr.Event{}, 0, invalid("intervals", "is required")
	}
	e := openadr.Event{
		Metadata:           metaFrom(w.metadata),
		ProgramID:          w.ProgramID,
		Name:               w.EventName,
		Priority:           w.Priority,
		PayloadDescriptors: mapSlice(w.PayloadDescriptors, common.EventDescriptorFrom),
	}
	var err error
	if e.TypedTargets, err = common.ValuesMapsFrom("targets", w.Targets); err != nil {
		return openadr.Event{}, 0, err
	}
	for i, d := range w.ReportDescriptors {
		rd := common.RequestDescriptorFrom(d.ReportDescriptorCommon)
		if rd.TypedTargets, err = common.ValuesMapsFrom(fmt.Sprintf("reportDescriptors[%d].targets", i), d.Targets); err != nil {
			return openadr.Event{}, 0, err
		}
		e.ReportDescriptors = append(e.ReportDescriptors, rd)
	}
	if e.IntervalPeriod, err = common.PeriodFrom("intervalPeriod", w.IntervalPeriod); err != nil {
		return openadr.Event{}, 0, err
	}
	if e.Intervals, err = common.IntervalsFrom("intervals", w.Intervals); err != nil {
		return openadr.Event{}, 0, err
	}
	return e, wire.Object, nil
}

// --- report

func marshalReport(r openadr.Report, form wire.Form) ([]byte, error) {
	if r.ProgramID == "" {
		return nil, invalid("programID", "is required by OpenADR 3.0.1")
	}
	w := report{
		ProgramID:          r.ProgramID,
		EventID:            r.EventID,
		ClientName:         r.ClientName,
		ReportName:         r.Name,
		PayloadDescriptors: mapSlice(r.PayloadDescriptors, common.ReportDescriptorTo),
	}
	if form == wire.Object {
		w.metadata = metaTo(r.Metadata, openadr.ObjectReport)
	}
	var err error
	if w.Resources, err = common.ReportResourcesTo(r.Resources); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalReport(data []byte) (openadr.Report, wire.Form, error) {
	var w report
	if err := decode(data, &w); err != nil {
		return openadr.Report{}, 0, err
	}
	r := openadr.Report{
		Metadata:           metaFrom(w.metadata),
		ProgramID:          w.ProgramID,
		EventID:            w.EventID,
		ClientName:         w.ClientName,
		Name:               w.ReportName,
		PayloadDescriptors: mapSlice(w.PayloadDescriptors, common.ReportDescriptorFrom),
	}
	var err error
	if r.Resources, err = common.ReportResourcesFrom(w.Resources); err != nil {
		return openadr.Report{}, 0, err
	}
	return r, wire.Object, nil
}

// --- ven

// noClientID refuses the clientID of 3.1.0: a 3.0.1 VTN ties VENs and
// resources to credentials in a way of its own.
func noClientID(clientID string) error {
	if clientID != "" {
		return invalid("clientID", "is of OpenADR 3.1.0; a 3.0.1 VTN ties objects to credentials in its own way")
	}
	return nil
}

func marshalVEN(v openadr.VEN, form wire.Form) ([]byte, error) {
	w := ven{VenName: v.Name}
	var err error
	// The VEN's own request leaves targets to Business Logic, as in 3.1.0.
	if form != wire.Request {
		if err := noClientID(v.ClientID); err != nil {
			return nil, err
		}
		if w.Targets, err = targetsTo("targets", v.Targets, v.TypedTargets); err != nil {
			return nil, err
		}
	}
	if form == wire.Object {
		w.metadata = metaTo(v.Metadata, openadr.ObjectVEN)
	}
	if w.Attributes, err = common.ValuesMapsTo(v.Attributes); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalVEN(data []byte) (openadr.VEN, wire.Form, error) {
	var w ven
	if err := decode(data, &w); err != nil {
		return openadr.VEN{}, 0, err
	}
	v := openadr.VEN{Metadata: metaFrom(w.metadata), Name: w.VenName}
	var err error
	if v.Attributes, err = common.ValuesMapsFrom("attributes", w.Attributes); err != nil {
		return openadr.VEN{}, 0, err
	}
	if v.TypedTargets, err = common.ValuesMapsFrom("targets", w.Targets); err != nil {
		return openadr.VEN{}, 0, err
	}
	return v, wire.Object, nil
}

// --- resource

func marshalResource(r openadr.Resource, form wire.Form) ([]byte, error) {
	w := resource{ResourceName: r.Name, VenID: r.VENID}
	var err error
	if form != wire.Request {
		if err := noClientID(r.ClientID); err != nil {
			return nil, err
		}
		if w.Targets, err = targetsTo("targets", r.Targets, r.TypedTargets); err != nil {
			return nil, err
		}
	}
	if form == wire.Object {
		w.metadata = metaTo(r.Metadata, openadr.ObjectResource)
	}
	if w.Attributes, err = common.ValuesMapsTo(r.Attributes); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalResource(data []byte) (openadr.Resource, wire.Form, error) {
	var w resource
	if err := decode(data, &w); err != nil {
		return openadr.Resource{}, 0, err
	}
	r := openadr.Resource{Metadata: metaFrom(w.metadata), VENID: w.VenID, Name: w.ResourceName}
	var err error
	if r.Attributes, err = common.ValuesMapsFrom("attributes", w.Attributes); err != nil {
		return openadr.Resource{}, 0, err
	}
	if r.TypedTargets, err = common.ValuesMapsFrom("targets", w.Targets); err != nil {
		return openadr.Resource{}, 0, err
	}
	return r, wire.Object, nil
}

// --- subscription

func marshalSubscription(sub openadr.Subscription, form wire.Form) ([]byte, error) {
	if sub.ProgramID == "" {
		return nil, invalid("programID", "is required by OpenADR 3.0.1")
	}
	w := subscription{
		ClientName:       sub.ClientName,
		ProgramID:        sub.ProgramID,
		ObjectOperations: make([]objectOperation, len(sub.ObjectOperations)),
	}
	if form == wire.Object {
		w.metadata = metaTo(sub.Metadata, openadr.ObjectSubscription)
	}
	var err error
	if w.Targets, err = targetsTo("targets", sub.Targets, sub.TypedTargets); err != nil {
		return nil, err
	}
	for i, oo := range sub.ObjectOperations {
		if oo.Mechanism != "" && oo.Mechanism != openadr.MechanismWebhook {
			return nil, invalid(fmt.Sprintf("objectOperations[%d].mechanism", i), "is %s; OpenADR 3.0.1 notifies by webhook only", oo.Mechanism)
		}
		w.ObjectOperations[i] = objectOperation{
			// Required, so written even when empty.
			Objects:     make([]string, len(oo.Objects)),
			Operations:  make([]string, len(oo.Operations)),
			CallbackURL: oo.CallbackURL,
			BearerToken: oo.BearerToken,
		}
		for j, o := range oo.Objects {
			w.ObjectOperations[i].Objects[j] = string(o)
		}
		for j, op := range oo.Operations {
			w.ObjectOperations[i].Operations[j] = string(op)
		}
	}
	return json.Marshal(w)
}

func unmarshalSubscription(data []byte) (openadr.Subscription, wire.Form, error) {
	var w subscription
	if err := decode(data, &w); err != nil {
		return openadr.Subscription{}, 0, err
	}
	if w.ObjectOperations == nil {
		return openadr.Subscription{}, 0, invalid("objectOperations", "is required")
	}
	sub := openadr.Subscription{
		Metadata:         metaFrom(w.metadata),
		ClientName:       w.ClientName,
		ProgramID:        w.ProgramID,
		ObjectOperations: make([]openadr.ObjectOperation, len(w.ObjectOperations)),
	}
	var err error
	if sub.TypedTargets, err = common.ValuesMapsFrom("targets", w.Targets); err != nil {
		return openadr.Subscription{}, 0, err
	}
	for i, oo := range w.ObjectOperations {
		f := fmt.Sprintf("objectOperations[%d]", i)
		if oo.Objects == nil {
			return openadr.Subscription{}, 0, invalid(f+".objects", "is required")
		}
		if oo.Operations == nil {
			return openadr.Subscription{}, 0, invalid(f+".operations", "is required")
		}
		out := openadr.ObjectOperation{CallbackURL: oo.CallbackURL, BearerToken: oo.BearerToken}
		for _, o := range oo.Objects {
			out.Objects = append(out.Objects, openadr.ObjectType(o))
		}
		for _, op := range oo.Operations {
			out.Operations = append(out.Operations, openadr.Operation(op))
		}
		sub.ObjectOperations[i] = out
	}
	return sub, wire.Object, nil
}

// --- notification

// 3.0.1 names the operation of a notification by its HTTP method; 3.1.0,
// and the domain model, by what it does.
var (
	methodOf = map[openadr.Operation]string{
		openadr.OperationCreate: "POST",
		openadr.OperationRead:   "GET",
		openadr.OperationUpdate: "PUT",
		openadr.OperationDelete: "DELETE",
	}
	operationOf = map[string]openadr.Operation{
		"POST":   openadr.OperationCreate,
		"GET":    openadr.OperationRead,
		"PUT":    openadr.OperationUpdate,
		"DELETE": openadr.OperationDelete,
	}
)

func marshalNotification(n openadr.Notification) ([]byte, error) {
	method, ok := methodOf[n.Operation]
	if !ok {
		return nil, invalid("operation", "is %q, not CREATE, READ, UPDATE or DELETE", n.Operation)
	}
	var (
		object []byte
		err    error
	)
	switch o := n.Object.(type) {
	case openadr.Program:
		object, err = marshalProgram(o, wire.Object)
	case openadr.Event:
		object, err = marshalEvent(o, wire.Object)
	case openadr.Report:
		object, err = marshalReport(o, wire.Object)
	case openadr.Subscription:
		object, err = marshalSubscription(o, wire.Object)
	case openadr.VEN:
		object, err = marshalVEN(o, wire.Object)
	case openadr.Resource:
		object, err = marshalResource(o, wire.Object)
	default:
		return nil, fmt.Errorf("openadr: an OpenADR 3.0.1 notification cannot carry a %T", n.Object)
	}
	if err != nil {
		return nil, err
	}
	targets, err := targetsTo("targets", n.Targets, n.TypedTargets)
	if err != nil {
		return nil, err
	}
	return json.Marshal(notification{ObjectType: string(n.ObjectType), Operation: method, Object: object, Targets: targets})
}

func unmarshalNotification(data []byte) (openadr.Notification, error) {
	var w notification
	if err := decode(data, &w); err != nil {
		return openadr.Notification{}, err
	}
	op, ok := operationOf[w.Operation]
	switch {
	case w.Operation == "":
		return openadr.Notification{}, invalid("operation", "is required")
	case !ok:
		return openadr.Notification{}, invalid("operation", "is %q, not GET, POST, PUT or DELETE", w.Operation)
	}
	if len(w.Object) == 0 || string(w.Object) == "null" {
		return openadr.Notification{}, invalid("object", "is required")
	}
	n := openadr.Notification{ObjectType: openadr.ObjectType(w.ObjectType), Operation: op}
	var err error
	if n.TypedTargets, err = common.ValuesMapsFrom("targets", w.Targets); err != nil {
		return openadr.Notification{}, err
	}
	switch n.ObjectType {
	case openadr.ObjectProgram:
		n.Object, _, err = unmarshalProgram(w.Object)
	case openadr.ObjectEvent:
		n.Object, _, err = unmarshalEvent(w.Object)
	case openadr.ObjectReport:
		n.Object, _, err = unmarshalReport(w.Object)
	case openadr.ObjectSubscription:
		n.Object, _, err = unmarshalSubscription(w.Object)
	case openadr.ObjectVEN:
		n.Object, _, err = unmarshalVEN(w.Object)
	case openadr.ObjectResource:
		n.Object, _, err = unmarshalResource(w.Object)
	case "":
		return openadr.Notification{}, invalid("objectType", "is required")
	default:
		return openadr.Notification{}, invalid("objectType", "is %q, not an object type", w.ObjectType)
	}
	if err != nil {
		return openadr.Notification{}, fmt.Errorf("object: %w", err)
	}
	return n, nil
}
