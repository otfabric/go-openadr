// SPDX-License-Identifier: MIT

package v310

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

func invalid(field, format string, args ...any) error {
	return &openadr.ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// decode unmarshals data into v. Properties the schema does not define are
// ignored, as the specification allows.
func decode(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return &openadr.ValidationError{Field: "body", Reason: err.Error()}
	}
	return nil
}

// --- metadata

func metaTo(m openadr.Metadata, objectType openadr.ObjectType) metadata {
	created, modified := m.Created, m.Modified
	return metadata{ID: m.ID, Created: &created, Modified: &modified, ObjectType: string(objectType)}
}

func metaFrom(m metadata) openadr.Metadata {
	out := openadr.Metadata{ID: m.ID}
	if m.Created != nil {
		out.Created = *m.Created
	}
	if m.Modified != nil {
		out.Modified = *m.Modified
	}
	return out
}

// --- durations and interval periods

func durationTo(d *openadr.Duration) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

func durationFrom(field string, s *string) (*openadr.Duration, error) {
	if s == nil {
		return nil, nil
	}
	d, err := openadr.ParseDuration(*s)
	if err != nil {
		return nil, invalid(field, "%q is not an ISO 8601 duration", *s)
	}
	return &d, nil
}

func periodTo(p *openadr.IntervalPeriod) *intervalPeriod {
	if p == nil {
		return nil
	}
	out := &intervalPeriod{Duration: durationTo(p.Duration), RandomizeStart: durationTo(p.RandomizeStart)}
	if p.Start != nil {
		start := startTime(*p.Start)
		out.Start = &start
	}
	return out
}

func periodFrom(field string, p *intervalPeriod) (*openadr.IntervalPeriod, error) {
	if p == nil {
		return nil, nil
	}
	out := &openadr.IntervalPeriod{}
	if p.Start != nil {
		start := time.Time(*p.Start)
		out.Start = &start
	}
	var err error
	if out.Duration, err = durationFrom(field+".duration", p.Duration); err != nil {
		return nil, err
	}
	if out.RandomizeStart, err = durationFrom(field+".randomizeStart", p.RandomizeStart); err != nil {
		return nil, err
	}
	return out, nil
}

// --- values

func valueTo(v openadr.Value) (json.RawMessage, error) {
	switch v.Kind() {
	case openadr.KindNumber:
		n, _ := v.Number()
		return json.Marshal(n)
	case openadr.KindString:
		s, _ := v.Text()
		return json.Marshal(s)
	case openadr.KindBool:
		b, _ := v.Bool()
		return json.Marshal(b)
	case openadr.KindPoint:
		p, _ := v.Point()
		return json.Marshal(point{X: &p.X, Y: &p.Y})
	default:
		return nil, fmt.Errorf("openadr: cannot encode the zero Value")
	}
}

func valueFrom(field string, raw json.RawMessage) (openadr.Value, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return openadr.Value{}, invalid(field, "is empty")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return openadr.Value{}, invalid(field, "%v", err)
		}
		return openadr.String(s), nil
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return openadr.Value{}, invalid(field, "%v", err)
		}
		return openadr.Bool(b), nil
	case '{':
		var p point
		if err := json.Unmarshal(raw, &p); err != nil {
			return openadr.Value{}, invalid(field, "%v", err)
		}
		if p.X == nil || p.Y == nil {
			return openadr.Value{}, invalid(field, "is an object without x and y")
		}
		return openadr.PointValue(*p.X, *p.Y), nil
	case 'n', '[':
		return openadr.Value{}, invalid(field, "must be a number, string, boolean or point")
	default:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return openadr.Value{}, invalid(field, "%v", err)
		}
		return openadr.Number(n), nil
	}
}

func valuesMapsTo(vms []openadr.ValuesMap) ([]valuesMap, error) {
	if vms == nil {
		return nil, nil
	}
	out := make([]valuesMap, len(vms))
	for i, vm := range vms {
		out[i] = valuesMap{Type: vm.Type, Values: make([]json.RawMessage, len(vm.Values))}
		for j, v := range vm.Values {
			raw, err := valueTo(v)
			if err != nil {
				return nil, err
			}
			out[i].Values[j] = raw
		}
	}
	return out, nil
}

func valuesMapsFrom(field string, vms []valuesMap) ([]openadr.ValuesMap, error) {
	if vms == nil {
		return nil, nil
	}
	out := make([]openadr.ValuesMap, len(vms))
	for i, vm := range vms {
		f := fmt.Sprintf("%s[%d]", field, i)
		if vm.Values == nil {
			return nil, invalid(f+".values", "is required")
		}
		out[i] = openadr.ValuesMap{Type: vm.Type, Values: make([]openadr.Value, len(vm.Values))}
		for j, raw := range vm.Values {
			v, err := valueFrom(fmt.Sprintf("%s.values[%d]", f, j), raw)
			if err != nil {
				return nil, err
			}
			out[i].Values[j] = v
		}
	}
	return out, nil
}

// --- intervals

// intervalsTo always returns a non-nil slice: where the schema has
// intervals at all, callers decide whether to omit it.
func intervalsTo(ivs []openadr.Interval) ([]interval, error) {
	out := make([]interval, len(ivs))
	for i, iv := range ivs {
		id := iv.ID
		payloads, err := valuesMapsTo(iv.Payloads)
		if err != nil {
			return nil, err
		}
		if payloads == nil {
			payloads = []valuesMap{}
		}
		out[i] = interval{ID: &id, IntervalPeriod: periodTo(iv.IntervalPeriod), Payloads: payloads}
	}
	return out, nil
}

func intervalsFrom(field string, ivs []interval) ([]openadr.Interval, error) {
	if ivs == nil {
		return nil, nil
	}
	out := make([]openadr.Interval, len(ivs))
	for i, iv := range ivs {
		f := fmt.Sprintf("%s[%d]", field, i)
		if iv.ID == nil {
			return nil, invalid(f+".id", "is required")
		}
		if iv.Payloads == nil {
			return nil, invalid(f+".payloads", "is required")
		}
		period, err := periodFrom(f+".intervalPeriod", iv.IntervalPeriod)
		if err != nil {
			return nil, err
		}
		payloads, err := valuesMapsFrom(f+".payloads", iv.Payloads)
		if err != nil {
			return nil, err
		}
		out[i] = openadr.Interval{ID: *iv.ID, IntervalPeriod: period, Payloads: payloads}
	}
	return out, nil
}

// --- descriptors

func eventDescriptorTo(d openadr.EventPayloadDescriptor) eventPayloadDescriptor {
	return eventPayloadDescriptor{ObjectType: typeEventPayload, PayloadType: d.PayloadType, Units: d.Units, Currency: d.Currency}
}

func eventDescriptorFrom(d eventPayloadDescriptor) openadr.EventPayloadDescriptor {
	return openadr.EventPayloadDescriptor{PayloadType: d.PayloadType, Units: d.Units, Currency: d.Currency}
}

func reportDescriptorTo(d openadr.ReportPayloadDescriptor) reportPayloadDescriptor {
	return reportPayloadDescriptor{
		ObjectType: typeReportPayload, PayloadType: d.PayloadType, ReadingType: d.ReadingType,
		Units: d.Units, Accuracy: d.Accuracy, Confidence: d.Confidence,
	}
}

func reportDescriptorFrom(d reportPayloadDescriptor) openadr.ReportPayloadDescriptor {
	return openadr.ReportPayloadDescriptor{
		PayloadType: d.PayloadType, ReadingType: d.ReadingType,
		Units: d.Units, Accuracy: d.Accuracy, Confidence: d.Confidence,
	}
}

func mapSlice[A, B any](in []A, f func(A) B) []B {
	if in == nil {
		return nil
	}
	out := make([]B, len(in))
	for i, a := range in {
		out[i] = f(a)
	}
	return out
}

func requestDescriptorTo(d openadr.ReportDescriptor) reportDescriptor {
	ri := string(d.ReportIntervals)
	return reportDescriptor{
		PayloadType: d.PayloadType, ReadingType: d.ReadingType, Units: d.Units, Targets: d.Targets,
		Aggregate: d.Aggregate, StartInterval: &d.StartInterval, NumIntervals: &d.NumIntervals,
		Historical: &d.Historical, Frequency: &d.Frequency, Repeat: &d.Repeat, ReportIntervals: &ri,
	}
}

func requestDescriptorFrom(d reportDescriptor) openadr.ReportDescriptor {
	out := openadr.NewReportDescriptor(d.PayloadType)
	out.ReadingType, out.Units, out.Targets, out.Aggregate = d.ReadingType, d.Units, d.Targets, d.Aggregate
	if d.StartInterval != nil {
		out.StartInterval = *d.StartInterval
	}
	if d.NumIntervals != nil {
		out.NumIntervals = *d.NumIntervals
	}
	if d.Historical != nil {
		out.Historical = *d.Historical
	}
	if d.Frequency != nil {
		out.Frequency = *d.Frequency
	}
	if d.Repeat != nil {
		out.Repeat = *d.Repeat
	}
	if d.ReportIntervals != nil {
		out.ReportIntervals = openadr.ReportIntervals(*d.ReportIntervals)
	}
	return out
}

// payloadDescriptorFrom decodes an element of program.payloadDescriptors,
// which the schema discriminates on objectType.
func payloadDescriptorFrom(field string, raw json.RawMessage) (openadr.PayloadDescriptor, error) {
	var head struct {
		ObjectType string `json:"objectType"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, invalid(field, "%v", err)
	}
	switch head.ObjectType {
	case typeEventPayload:
		var d eventPayloadDescriptor
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, invalid(field, "%v", err)
		}
		return eventDescriptorFrom(d), nil
	case typeReportPayload:
		var d reportPayloadDescriptor
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, invalid(field, "%v", err)
		}
		return reportDescriptorFrom(d), nil
	case "":
		return nil, invalid(field+".objectType", "is required")
	default:
		return nil, invalid(field+".objectType", "is %q, not %s or %s", head.ObjectType, typeEventPayload, typeReportPayload)
	}
}
