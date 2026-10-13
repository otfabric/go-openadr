// SPDX-License-Identifier: MIT

package v310

import (
	"encoding/json"
	"fmt"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// Codec returns the OpenADR 3.1.0 representation.
func Codec() *wire.Codec {
	return &wire.Codec{
		Version:   openadr.Version310,
		Programs:  wire.ObjectCodec[openadr.Program]{Marshal: marshalProgram, Unmarshal: unmarshalProgram},
		Events:    wire.ObjectCodec[openadr.Event]{Marshal: marshalEvent, Unmarshal: unmarshalEvent},
		Reports:   wire.ObjectCodec[openadr.Report]{Marshal: marshalReport, Unmarshal: unmarshalReport},
		VENs:      wire.ObjectCodec[openadr.VEN]{Marshal: marshalVEN, Unmarshal: unmarshalVEN},
		Resources: wire.ObjectCodec[openadr.Resource]{Marshal: marshalResource, Unmarshal: unmarshalResource},

		ResourceGroups: wire.ObjectCodec[openadr.ResourceGroup]{Marshal: marshalResourceGroup, Unmarshal: unmarshalResourceGroup},

		Subscriptions:         wire.ObjectCodec[openadr.Subscription]{Marshal: marshalSubscription, Unmarshal: unmarshalSubscription},
		MarshalNotification:   marshalNotification,
		UnmarshalNotification: unmarshalNotification,
		MarshalNotifiers:      marshalNotifiers,
		UnmarshalNotifiers:    unmarshalNotifiers,
		MarshalTopics: func(t openadr.Topics) ([]byte, error) {
			return json.Marshal(topicsResponse{Topics: topics{Create: t.Create, Update: t.Update, Delete: t.Delete, All: t.All}})
		},
		UnmarshalTopics: func(data []byte) (openadr.Topics, error) {
			var w topicsResponse
			if err := decode(data, &w); err != nil {
				return openadr.Topics{}, err
			}
			return openadr.Topics{Create: w.Topics.Create, Update: w.Topics.Update, Delete: w.Topics.Delete, All: w.Topics.All}, nil
		},

		MarshalProblem: func(p openadr.Problem) ([]byte, error) {
			return json.Marshal(problem(p))
		},
		UnmarshalProblem: func(data []byte) (openadr.Problem, error) {
			var p problem
			err := json.Unmarshal(data, &p)
			return openadr.Problem(p), err
		},
		MarshalAuthServer: func(tokenURL string) ([]byte, error) {
			return json.Marshal(authServerInfo{TokenURL: tokenURL})
		},
		UnmarshalAuthServer: func(data []byte) (string, error) {
			var info authServerInfo
			if err := decode(data, &info); err != nil {
				return "", err
			}
			if info.TokenURL == "" {
				return "", invalid("tokenURL", "is required")
			}
			return info.TokenURL, nil
		},
	}
}

// requestMeta is the metadata of a request: nothing, or a discriminator.
func requestMeta(discriminator string) metadata {
	return metadata{ObjectType: discriminator}
}

// --- program

func marshalProgram(p openadr.Program, form wire.Form) ([]byte, error) {
	w := program{
		ProgramName:    p.Name,
		IntervalPeriod: periodTo(p.IntervalPeriod),
		ProgramDescriptions: mapSlice(p.Descriptions, func(d openadr.ProgramDescription) programDescription {
			return programDescription(d)
		}),
		Targets: p.Targets,
	}
	if form == wire.Object {
		w.metadata = metaTo(p.Metadata, openadr.ObjectProgram)
	}
	var err error
	if w.Attributes, err = valuesMapsTo(p.Attributes); err != nil {
		return nil, err
	}
	for i, d := range p.PayloadDescriptors {
		var v any
		switch d := d.(type) {
		case openadr.EventPayloadDescriptor:
			v = eventDescriptorTo(d)
		case openadr.ReportPayloadDescriptor:
			v = reportDescriptorTo(d)
		default:
			return nil, fmt.Errorf("openadr: program payload descriptor %d is nil", i)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		w.PayloadDescriptors = append(w.PayloadDescriptors, raw)
	}
	return json.Marshal(w)
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
		Targets: w.Targets,
	}
	var err error
	if p.IntervalPeriod, err = periodFrom("intervalPeriod", w.IntervalPeriod); err != nil {
		return openadr.Program{}, 0, err
	}
	if p.Attributes, err = valuesMapsFrom("attributes", w.Attributes); err != nil {
		return openadr.Program{}, 0, err
	}
	for i, raw := range w.PayloadDescriptors {
		d, err := payloadDescriptorFrom(fmt.Sprintf("payloadDescriptors[%d]", i), raw)
		if err != nil {
			return openadr.Program{}, 0, err
		}
		p.PayloadDescriptors = append(p.PayloadDescriptors, d)
	}
	return p, wire.Object, nil
}

// --- event

func marshalEvent(e openadr.Event, form wire.Form) ([]byte, error) {
	w := event{
		ProgramID:          e.ProgramID,
		EventName:          e.Name,
		Duration:           durationTo(e.Duration),
		Priority:           e.Priority,
		Targets:            e.Targets,
		ReportDescriptors:  mapSlice(e.ReportDescriptors, requestDescriptorTo),
		PayloadDescriptors: mapSlice(e.PayloadDescriptors, eventDescriptorTo),
		IntervalPeriod:     periodTo(e.IntervalPeriod),
	}
	if form == wire.Object {
		w.metadata = metaTo(e.Metadata, openadr.ObjectEvent)
	}
	if e.Intervals != nil {
		var err error
		if w.Intervals, err = intervalsTo(e.Intervals); err != nil {
			return nil, err
		}
	}
	return json.Marshal(w)
}

func unmarshalEvent(data []byte) (openadr.Event, wire.Form, error) {
	var w event
	if err := decode(data, &w); err != nil {
		return openadr.Event{}, 0, err
	}
	e := openadr.Event{
		Metadata:           metaFrom(w.metadata),
		ProgramID:          w.ProgramID,
		Name:               w.EventName,
		Priority:           w.Priority,
		Targets:            w.Targets,
		ReportDescriptors:  mapSlice(w.ReportDescriptors, requestDescriptorFrom),
		PayloadDescriptors: mapSlice(w.PayloadDescriptors, eventDescriptorFrom),
	}
	var err error
	if e.Duration, err = durationFrom("duration", w.Duration); err != nil {
		return openadr.Event{}, 0, err
	}
	if e.IntervalPeriod, err = periodFrom("intervalPeriod", w.IntervalPeriod); err != nil {
		return openadr.Event{}, 0, err
	}
	if e.Intervals, err = intervalsFrom("intervals", w.Intervals); err != nil {
		return openadr.Event{}, 0, err
	}
	return e, wire.Object, nil
}

// --- report

func marshalReport(r openadr.Report, form wire.Form) ([]byte, error) {
	w := report{
		EventID:            r.EventID,
		ClientName:         r.ClientName,
		ReportName:         r.Name,
		PayloadDescriptors: mapSlice(r.PayloadDescriptors, reportDescriptorTo),
		Resources:          make([]reportResource, len(r.Resources)),
	}
	if form == wire.Object {
		w.metadata = metaTo(r.Metadata, openadr.ObjectReport)
		w.ClientID = r.ClientID
	}
	for i, res := range r.Resources {
		ivs, err := intervalsTo(res.Intervals)
		if err != nil {
			return nil, err
		}
		w.Resources[i] = reportResource{ResourceName: res.ResourceName, IntervalPeriod: periodTo(res.IntervalPeriod), Intervals: ivs}
	}
	return json.Marshal(w)
}

func unmarshalReport(data []byte) (openadr.Report, wire.Form, error) {
	var w report
	if err := decode(data, &w); err != nil {
		return openadr.Report{}, 0, err
	}
	if w.Resources == nil {
		return openadr.Report{}, 0, invalid("resources", "is required")
	}
	r := openadr.Report{
		Metadata:           metaFrom(w.metadata),
		ClientID:           w.ClientID,
		EventID:            w.EventID,
		ClientName:         w.ClientName,
		Name:               w.ReportName,
		PayloadDescriptors: mapSlice(w.PayloadDescriptors, reportDescriptorFrom),
		Resources:          make([]openadr.ReportResource, len(w.Resources)),
	}
	for i, res := range w.Resources {
		f := fmt.Sprintf("resources[%d]", i)
		if res.Intervals == nil {
			return openadr.Report{}, 0, invalid(f+".intervals", "is required")
		}
		period, err := periodFrom(f+".intervalPeriod", res.IntervalPeriod)
		if err != nil {
			return openadr.Report{}, 0, err
		}
		ivs, err := intervalsFrom(f+".intervals", res.Intervals)
		if err != nil {
			return openadr.Report{}, 0, err
		}
		r.Resources[i] = openadr.ReportResource{ResourceName: res.ResourceName, IntervalPeriod: period, Intervals: ivs}
	}
	return r, wire.Object, nil
}

// --- ven

func marshalVEN(v openadr.VEN, form wire.Form) ([]byte, error) {
	w := ven{VenName: v.Name}
	switch form {
	case wire.Object:
		w.metadata = metaTo(v.Metadata, openadr.ObjectVEN)
		w.ClientID, w.Targets = v.ClientID, v.Targets
	case wire.BLRequest:
		w.metadata = requestMeta(typeBLVENRequest)
		w.ClientID, w.Targets = v.ClientID, v.Targets
	default:
		// The VEN's own request has neither clientID nor targets.
		w.metadata = requestMeta(typeVENVENRequest)
	}
	var err error
	if w.Attributes, err = valuesMapsTo(v.Attributes); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalVEN(data []byte) (openadr.VEN, wire.Form, error) {
	var w ven
	if err := decode(data, &w); err != nil {
		return openadr.VEN{}, 0, err
	}
	form := formOf(w.ObjectType, typeBLVENRequest, typeVENVENRequest)
	var err error
	v := openadr.VEN{Metadata: metaFrom(w.metadata), ClientID: w.ClientID, Name: w.VenName, Targets: w.Targets}
	if v.Attributes, err = valuesMapsFrom("attributes", w.Attributes); err != nil {
		return openadr.VEN{}, 0, err
	}
	return v, form, nil
}

// --- resource

func marshalResource(r openadr.Resource, form wire.Form) ([]byte, error) {
	w := resource{ResourceName: r.Name, VenID: r.VENID}
	switch form {
	case wire.Object:
		w.metadata = metaTo(r.Metadata, openadr.ObjectResource)
		w.ClientID, w.Targets = r.ClientID, r.Targets
	case wire.BLRequest:
		w.metadata = requestMeta(typeBLResourceRequest)
		w.ClientID, w.Targets = r.ClientID, r.Targets
	default:
		w.metadata = requestMeta(typeVENResourceRequest)
	}
	var err error
	if w.Attributes, err = valuesMapsTo(r.Attributes); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalResource(data []byte) (openadr.Resource, wire.Form, error) {
	var w resource
	if err := decode(data, &w); err != nil {
		return openadr.Resource{}, 0, err
	}
	form := formOf(w.ObjectType, typeBLResourceRequest, typeVENResourceRequest)
	var err error
	r := openadr.Resource{Metadata: metaFrom(w.metadata), ClientID: w.ClientID, VENID: w.VenID, Name: w.ResourceName, Targets: w.Targets}
	if r.Attributes, err = valuesMapsFrom("attributes", w.Attributes); err != nil {
		return openadr.Resource{}, 0, err
	}
	return r, form, nil
}

// --- subscription

func marshalSubscription(sub openadr.Subscription, form wire.Form) ([]byte, error) {
	w := subscription{
		ClientName:       sub.ClientName,
		ProgramID:        sub.ProgramID,
		Targets:          sub.Targets,
		ObjectOperations: make([]objectOperation, len(sub.ObjectOperations)),
	}
	if form == wire.Object {
		w.metadata = metaTo(sub.Metadata, openadr.ObjectSubscription)
		w.ClientID = sub.ClientID
	}
	for i, oo := range sub.ObjectOperations {
		w.ObjectOperations[i] = objectOperation{
			// Required, so written even when empty.
			Objects:     make([]string, len(oo.Objects)),
			Operations:  make([]string, len(oo.Operations)),
			CallbackURL: oo.CallbackURL,
			BearerToken: oo.BearerToken,
		}
		if oo.Mechanism != openadr.MechanismWebhook {
			w.ObjectOperations[i].Mechanism = string(oo.Mechanism)
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
		ClientID:         w.ClientID,
		ClientName:       w.ClientName,
		ProgramID:        w.ProgramID,
		Targets:          w.Targets,
		ObjectOperations: make([]openadr.ObjectOperation, len(w.ObjectOperations)),
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
		if oo.Mechanism != string(openadr.MechanismWebhook) {
			out.Mechanism = openadr.Mechanism(oo.Mechanism)
		}
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

func marshalNotification(n openadr.Notification) ([]byte, error) {
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
	case openadr.ResourceGroup:
		object, err = marshalResourceGroup(o, wire.Object)
	default:
		return nil, fmt.Errorf("openadr: a notification cannot carry a %T", n.Object)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(notification{
		ObjectType: string(n.ObjectType), Operation: string(n.Operation), Object: object, Targets: n.Targets,
	})
}

func unmarshalNotification(data []byte) (openadr.Notification, error) {
	var w notification
	if err := decode(data, &w); err != nil {
		return openadr.Notification{}, err
	}
	if w.Operation == "" {
		return openadr.Notification{}, invalid("operation", "is required")
	}
	if len(w.Object) == 0 || string(w.Object) == "null" {
		return openadr.Notification{}, invalid("object", "is required")
	}
	n := openadr.Notification{
		ID:         w.ID,
		ObjectType: openadr.ObjectType(w.ObjectType), Operation: openadr.Operation(w.Operation), Targets: w.Targets,
	}
	var err error
	// The schema discriminates the object on its own objectType; the one
	// of the notification says the same and is always there.
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
	case openadr.ObjectResourceGroup:
		n.Object, _, err = unmarshalResourceGroup(w.Object)
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

// --- notifiers

func marshalNotifiers(n openadr.Notifiers) ([]byte, error) {
	w := notifiers{Webhook: n.Webhook, WebSocket: n.WebSocket}
	if n.MQTT != nil {
		a := n.MQTT.Authentication
		w.MQTT = &mqttBinding{
			URIs: n.MQTT.URIs, Serialization: n.MQTT.Serialization,
			Authentication: mqttAuthentication{Method: a.Method, Username: a.Username, CACert: a.CACert, ClientCert: a.ClientCert, ClientKey: a.ClientKey},
		}
		if w.MQTT.URIs == nil {
			w.MQTT.URIs = []string{}
		}
	}
	return json.Marshal(w)
}

func unmarshalNotifiers(data []byte) (openadr.Notifiers, error) {
	var w notifiers
	if err := decode(data, &w); err != nil {
		return openadr.Notifiers{}, err
	}
	n := openadr.Notifiers{Webhook: w.Webhook, WebSocket: w.WebSocket}
	if w.MQTT != nil {
		a := w.MQTT.Authentication
		n.MQTT = &openadr.MQTTBinding{
			URIs: w.MQTT.URIs, Serialization: w.MQTT.Serialization,
			Authentication: openadr.MQTTAuthentication{Method: a.Method, Username: a.Username, CACert: a.CACert, ClientCert: a.ClientCert, ClientKey: a.ClientKey},
		}
	}
	return n, nil
}

// formOf maps the objectType of a ven or resource message to the form of
// the message. The request schemas require it as a discriminator. Anything
// else is taken as the VTN's representation: the response schemas cannot be
// met as written (objectType would have to be both VEN and BL_VEN_REQUEST),
// and VTNs differ in what they send, including nothing at all.
func formOf(objectType, blRequest, venRequest string) wire.Form {
	switch objectType {
	case blRequest:
		return wire.BLRequest
	case venRequest:
		return wire.Request
	default:
		return wire.Object
	}
}
