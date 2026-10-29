// SPDX-License-Identifier: MIT

// Package common holds the JSON that OpenADR 3.0.1 and 3.1.0 write alike:
// object metadata, durations, interval periods, values maps, intervals,
// payload descriptors and problems, with their conversions to and from the
// domain model. The codec of each revision builds on it.
package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// Discriminator values of the payload descriptors.
const (
	TypeEventPayload  = "EVENT_PAYLOAD_DESCRIPTOR"
	TypeReportPayload = "REPORT_PAYLOAD_DESCRIPTOR"
)

// Invalid returns a *openadr.ValidationError.
func Invalid(field, format string, args ...any) error {
	return &openadr.ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Decode unmarshals data into v. Properties the schema does not define are
// ignored, as the specification allows.
func Decode(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return &openadr.ValidationError{Field: "body", Reason: err.Error()}
	}
	return nil
}

// MapSlice applies f to every element of in; nil stays nil.
func MapSlice[A, B any](in []A, f func(A) B) []B {
	if in == nil {
		return nil
	}
	out := make([]B, len(in))
	for i, a := range in {
		out[i] = f(a)
	}
	return out
}

// --- metadata

// Metadata is the objectMetadata schema. All of it is absent in requests,
// except objectType where a request schema uses it as a discriminator.
type Metadata struct {
	ID         string     `json:"id,omitempty"`
	Created    *time.Time `json:"createdDateTime,omitempty"`
	Modified   *time.Time `json:"modificationDateTime,omitempty"`
	ObjectType string     `json:"objectType,omitempty"`
}

// MetaTo is the metadata of an object in the VTN's representation.
func MetaTo(m openadr.Metadata, objectType openadr.ObjectType) Metadata {
	created, modified := m.Created, m.Modified
	return Metadata{ID: m.ID, Created: &created, Modified: &modified, ObjectType: string(objectType)}
}

// MetaFrom is the domain metadata of a message.
func MetaFrom(m Metadata) openadr.Metadata {
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

// DurationTo writes a duration in ISO 8601.
func DurationTo(d *openadr.Duration) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// DurationFrom reads an ISO 8601 duration.
func DurationFrom(field string, s *string) (*openadr.Duration, error) {
	if s == nil {
		return nil, nil
	}
	d, err := openadr.ParseDuration(*s)
	if err != nil {
		return nil, Invalid(field, "%q is not an ISO 8601 duration", *s)
	}
	return &d, nil
}

// StartTime is the start of an interval period. The schema makes it a
// date-time, and the User Guide writes "the beginning of time", which means
// "now" or "after the previous interval", in two forms that are not one:
// 0001-01-01 and 0001-01-01T00:00:00. Both are read as the zero time.
type StartTime time.Time

// MarshalJSON implements json.Marshaler.
func (t StartTime) MarshalJSON() ([]byte, error) { return time.Time(t).MarshalJSON() }

// UnmarshalJSON implements json.Unmarshaler.
func (t *StartTime) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case `"0001-01-01"`, `"0001-01-01T00:00:00"`:
		*t = StartTime(time.Time{})
		return nil
	}
	return (*time.Time)(t).UnmarshalJSON(data)
}

// IntervalPeriod is the intervalPeriod schema.
type IntervalPeriod struct {
	Start          *StartTime `json:"start,omitempty"`
	Duration       *string    `json:"duration,omitempty"`
	RandomizeStart *string    `json:"randomizeStart,omitempty"`
}

// PeriodTo writes an interval period.
func PeriodTo(p *openadr.IntervalPeriod) *IntervalPeriod {
	if p == nil {
		return nil
	}
	out := &IntervalPeriod{Duration: DurationTo(p.Duration), RandomizeStart: DurationTo(p.RandomizeStart)}
	if p.Start != nil {
		start := StartTime(*p.Start)
		out.Start = &start
	}
	return out
}

// PeriodFrom reads an interval period.
func PeriodFrom(field string, p *IntervalPeriod) (*openadr.IntervalPeriod, error) {
	if p == nil {
		return nil, nil
	}
	out := &openadr.IntervalPeriod{}
	if p.Start != nil {
		start := time.Time(*p.Start)
		out.Start = &start
	}
	var err error
	if out.Duration, err = DurationFrom(field+".duration", p.Duration); err != nil {
		return nil, err
	}
	if out.RandomizeStart, err = DurationFrom(field+".randomizeStart", p.RandomizeStart); err != nil {
		return nil, err
	}
	return out, nil
}

// --- values

// ValuesMap is the valuesMap schema.
type ValuesMap struct {
	Type   string            `json:"type"`
	Values []json.RawMessage `json:"values"`
}

type point struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

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
		return openadr.Value{}, Invalid(field, "is empty")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return openadr.Value{}, Invalid(field, "%v", err)
		}
		return openadr.String(s), nil
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return openadr.Value{}, Invalid(field, "%v", err)
		}
		return openadr.Bool(b), nil
	case '{':
		var p point
		if err := json.Unmarshal(raw, &p); err != nil {
			return openadr.Value{}, Invalid(field, "%v", err)
		}
		if p.X == nil || p.Y == nil {
			return openadr.Value{}, Invalid(field, "is an object without x and y")
		}
		return openadr.PointValue(*p.X, *p.Y), nil
	case 'n', '[':
		return openadr.Value{}, Invalid(field, "must be a number, string, boolean or point")
	default:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return openadr.Value{}, Invalid(field, "%v", err)
		}
		return openadr.Number(n), nil
	}
}

// ValuesMapsTo writes values maps; nil stays nil.
func ValuesMapsTo(vms []openadr.ValuesMap) ([]ValuesMap, error) {
	if vms == nil {
		return nil, nil
	}
	out := make([]ValuesMap, len(vms))
	for i, vm := range vms {
		out[i] = ValuesMap{Type: vm.Type, Values: make([]json.RawMessage, len(vm.Values))}
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

// ValuesMapsFrom reads values maps; nil stays nil.
func ValuesMapsFrom(field string, vms []ValuesMap) ([]openadr.ValuesMap, error) {
	if vms == nil {
		return nil, nil
	}
	out := make([]openadr.ValuesMap, len(vms))
	for i, vm := range vms {
		f := fmt.Sprintf("%s[%d]", field, i)
		if vm.Values == nil {
			return nil, Invalid(f+".values", "is required")
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

// Interval is the interval schema.
type Interval struct {
	// A pointer so that a missing id can be told from id 0.
	ID             *int32          `json:"id"`
	IntervalPeriod *IntervalPeriod `json:"intervalPeriod,omitempty"`
	Payloads       []ValuesMap     `json:"payloads"`
}

// IntervalsTo always returns a non-nil slice: where the schema has
// intervals at all, callers decide whether to omit it.
func IntervalsTo(ivs []openadr.Interval) ([]Interval, error) {
	out := make([]Interval, len(ivs))
	for i, iv := range ivs {
		id := iv.ID
		payloads, err := ValuesMapsTo(iv.Payloads)
		if err != nil {
			return nil, err
		}
		if payloads == nil {
			payloads = []ValuesMap{}
		}
		out[i] = Interval{ID: &id, IntervalPeriod: PeriodTo(iv.IntervalPeriod), Payloads: payloads}
	}
	return out, nil
}

// IntervalsFrom reads intervals; nil stays nil.
func IntervalsFrom(field string, ivs []Interval) ([]openadr.Interval, error) {
	if ivs == nil {
		return nil, nil
	}
	out := make([]openadr.Interval, len(ivs))
	for i, iv := range ivs {
		f := fmt.Sprintf("%s[%d]", field, i)
		if iv.ID == nil {
			return nil, Invalid(f+".id", "is required")
		}
		if iv.Payloads == nil {
			return nil, Invalid(f+".payloads", "is required")
		}
		period, err := PeriodFrom(f+".intervalPeriod", iv.IntervalPeriod)
		if err != nil {
			return nil, err
		}
		payloads, err := ValuesMapsFrom(f+".payloads", iv.Payloads)
		if err != nil {
			return nil, err
		}
		out[i] = openadr.Interval{ID: *iv.ID, IntervalPeriod: period, Payloads: payloads}
	}
	return out, nil
}

// --- descriptors

// EventPayloadDescriptor is the eventPayloadDescriptor schema.
type EventPayloadDescriptor struct {
	ObjectType  string `json:"objectType"`
	PayloadType string `json:"payloadType"`
	Units       string `json:"units,omitempty"`
	Currency    string `json:"currency,omitempty"`
}

// ReportPayloadDescriptor is the reportPayloadDescriptor schema.
type ReportPayloadDescriptor struct {
	ObjectType  string   `json:"objectType"`
	PayloadType string   `json:"payloadType"`
	ReadingType string   `json:"readingType,omitempty"`
	Units       string   `json:"units,omitempty"`
	Accuracy    *float64 `json:"accuracy,omitempty"`
	Confidence  *int     `json:"confidence,omitempty"`
}

// EventDescriptorTo writes an event payload descriptor.
func EventDescriptorTo(d openadr.EventPayloadDescriptor) EventPayloadDescriptor {
	return EventPayloadDescriptor{ObjectType: TypeEventPayload, PayloadType: d.PayloadType, Units: d.Units, Currency: d.Currency}
}

// EventDescriptorFrom reads an event payload descriptor.
func EventDescriptorFrom(d EventPayloadDescriptor) openadr.EventPayloadDescriptor {
	return openadr.EventPayloadDescriptor{PayloadType: d.PayloadType, Units: d.Units, Currency: d.Currency}
}

// ReportDescriptorTo writes a report payload descriptor.
func ReportDescriptorTo(d openadr.ReportPayloadDescriptor) ReportPayloadDescriptor {
	return ReportPayloadDescriptor{
		ObjectType: TypeReportPayload, PayloadType: d.PayloadType, ReadingType: d.ReadingType,
		Units: d.Units, Accuracy: d.Accuracy, Confidence: d.Confidence,
	}
}

// ReportDescriptorFrom reads a report payload descriptor.
func ReportDescriptorFrom(d ReportPayloadDescriptor) openadr.ReportPayloadDescriptor {
	return openadr.ReportPayloadDescriptor{
		PayloadType: d.PayloadType, ReadingType: d.ReadingType,
		Units: d.Units, Accuracy: d.Accuracy, Confidence: d.Confidence,
	}
}

// PayloadDescriptorsTo writes program.payloadDescriptors, whose elements
// the schema discriminates on objectType.
func PayloadDescriptorsTo(ds []openadr.PayloadDescriptor) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for i, d := range ds {
		var v any
		switch d := d.(type) {
		case openadr.EventPayloadDescriptor:
			v = EventDescriptorTo(d)
		case openadr.ReportPayloadDescriptor:
			v = ReportDescriptorTo(d)
		default:
			return nil, fmt.Errorf("openadr: program payload descriptor %d is nil", i)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// PayloadDescriptorsFrom reads program.payloadDescriptors.
func PayloadDescriptorsFrom(field string, raws []json.RawMessage) ([]openadr.PayloadDescriptor, error) {
	var out []openadr.PayloadDescriptor
	for i, raw := range raws {
		d, err := payloadDescriptorFrom(fmt.Sprintf("%s[%d]", field, i), raw)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func payloadDescriptorFrom(field string, raw json.RawMessage) (openadr.PayloadDescriptor, error) {
	var head struct {
		ObjectType string `json:"objectType"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, Invalid(field, "%v", err)
	}
	switch head.ObjectType {
	case TypeEventPayload:
		var d EventPayloadDescriptor
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, Invalid(field, "%v", err)
		}
		return EventDescriptorFrom(d), nil
	case TypeReportPayload:
		var d ReportPayloadDescriptor
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, Invalid(field, "%v", err)
		}
		return ReportDescriptorFrom(d), nil
	case "":
		return nil, Invalid(field+".objectType", "is required")
	default:
		return nil, Invalid(field+".objectType", "is %q, not %s or %s", head.ObjectType, TypeEventPayload, TypeReportPayload)
	}
}

// --- report descriptors

// ReportDescriptorCommon is what the reportDescriptor schemas of both
// revisions have, with pointers for the properties whose schema default is
// not the Go zero value, so that an absent property decodes to its default.
type ReportDescriptorCommon struct {
	PayloadType   string `json:"payloadType"`
	ReadingType   string `json:"readingType,omitempty"`
	Units         string `json:"units,omitempty"`
	Aggregate     bool   `json:"aggregate"`
	StartInterval *int32 `json:"startInterval"`
	NumIntervals  *int32 `json:"numIntervals"`
	Historical    *bool  `json:"historical"`
	Frequency     *int32 `json:"frequency"`
	Repeat        *int32 `json:"repeat"`
}

// RequestDescriptorTo writes what both revisions have of a report descriptor.
func RequestDescriptorTo(d openadr.ReportDescriptor) ReportDescriptorCommon {
	return ReportDescriptorCommon{
		PayloadType: d.PayloadType, ReadingType: d.ReadingType, Units: d.Units,
		Aggregate: d.Aggregate, StartInterval: &d.StartInterval, NumIntervals: &d.NumIntervals,
		Historical: &d.Historical, Frequency: &d.Frequency, Repeat: &d.Repeat,
	}
}

// RequestDescriptorFrom reads what both revisions have of a report
// descriptor, with the schema's defaults for what is absent.
func RequestDescriptorFrom(d ReportDescriptorCommon) openadr.ReportDescriptor {
	out := openadr.NewReportDescriptor(d.PayloadType)
	out.ReadingType, out.Units, out.Aggregate = d.ReadingType, d.Units, d.Aggregate
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
	return out
}

// --- report resources

// ReportResource is an element of report.resources.
type ReportResource struct {
	ResourceName   string          `json:"resourceName"`
	IntervalPeriod *IntervalPeriod `json:"intervalPeriod,omitempty"`
	Intervals      []Interval      `json:"intervals"`
}

// ReportResourcesTo writes report.resources, always as an array: it is
// required.
func ReportResourcesTo(rs []openadr.ReportResource) ([]ReportResource, error) {
	out := make([]ReportResource, len(rs))
	for i, res := range rs {
		ivs, err := IntervalsTo(res.Intervals)
		if err != nil {
			return nil, err
		}
		out[i] = ReportResource{ResourceName: res.ResourceName, IntervalPeriod: PeriodTo(res.IntervalPeriod), Intervals: ivs}
	}
	return out, nil
}

// ReportResourcesFrom reads report.resources, which is required.
func ReportResourcesFrom(rs []ReportResource) ([]openadr.ReportResource, error) {
	if rs == nil {
		return nil, Invalid("resources", "is required")
	}
	out := make([]openadr.ReportResource, len(rs))
	for i, res := range rs {
		f := fmt.Sprintf("resources[%d]", i)
		if res.Intervals == nil {
			return nil, Invalid(f+".intervals", "is required")
		}
		period, err := PeriodFrom(f+".intervalPeriod", res.IntervalPeriod)
		if err != nil {
			return nil, err
		}
		ivs, err := IntervalsFrom(f+".intervals", res.Intervals)
		if err != nil {
			return nil, err
		}
		out[i] = openadr.ReportResource{ResourceName: res.ResourceName, IntervalPeriod: period, Intervals: ivs}
	}
	return out, nil
}

// --- problems and subscriptions

// Problem is the problem schema (RFC 9457).
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// MarshalProblem writes a problem.
func MarshalProblem(p openadr.Problem) ([]byte, error) { return json.Marshal(Problem(p)) }

// UnmarshalProblem reads a problem.
func UnmarshalProblem(data []byte) (openadr.Problem, error) {
	var p Problem
	err := json.Unmarshal(data, &p)
	return openadr.Problem(p), err
}
