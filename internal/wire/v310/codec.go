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
