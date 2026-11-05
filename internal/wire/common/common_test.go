// SPDX-License-Identifier: MIT

package common

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

func TestValueFrom(t *testing.T) {
	for raw, want := range map[string]openadr.Value{
		`1.5`:                 openadr.Number(1.5),
		` -2 `:                openadr.Number(-2),
		`"a"`:                 openadr.String("a"),
		`true`:                openadr.Bool(true),
		`false`:               openadr.Bool(false),
		`{"x":1,"y":2}`:       openadr.PointValue(1, 2),
		`{"x":1,"y":2,"z":3}`: openadr.PointValue(1, 2),
	} {
		got, err := valueFrom("v", json.RawMessage(raw))
		if err != nil || got != want {
			t.Errorf("valueFrom(%s) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{``, `  `, `null`, `[1]`, `{"x":1}`, `{"y":1}`, `{"x":"1","y":2}`, `"unterminated`, `tru`, `fals`, `1e400`, `-`, `{`} {
		var ve *openadr.ValidationError
		if _, err := valueFrom("v", json.RawMessage(raw)); !errors.As(err, &ve) || ve.Field != "v" {
			t.Errorf("valueFrom(%q) = %v, want a ValidationError for v", raw, err)
		}
	}
}

func TestValueTo(t *testing.T) {
	if _, err := valueTo(openadr.Value{}); err == nil {
		t.Error("the zero Value was encoded")
	}
	// Not a number JSON has.
	if _, err := valueTo(openadr.Number(math.NaN())); err == nil {
		t.Error("NaN was encoded")
	}
	if _, err := ValuesMapsTo([]openadr.ValuesMap{{Type: "X", Values: []openadr.Value{{}}}}); err == nil {
		t.Error("a values map with the zero Value was encoded")
	}
	if _, err := IntervalsTo([]openadr.Interval{{Payloads: []openadr.ValuesMap{{Type: "X", Values: []openadr.Value{{}}}}}}); err == nil {
		t.Error("an interval with the zero Value was encoded")
	}
	if _, err := ReportResourcesTo([]openadr.ReportResource{{Intervals: []openadr.Interval{{Payloads: []openadr.ValuesMap{{Type: "X", Values: []openadr.Value{{}}}}}}}}); err == nil {
		t.Error("a report resource with the zero Value was encoded")
	}
	if _, err := PayloadDescriptorsTo([]openadr.PayloadDescriptor{nil}); err == nil {
		t.Error("a nil payload descriptor was encoded")
	}
	if got, err := PayloadDescriptorsTo(nil); got != nil || err != nil {
		t.Errorf("no payload descriptors = %s, %v", got, err)
	}
}

func TestStartTime(t *testing.T) {
	for _, raw := range []string{`"0001-01-01"`, `"0001-01-01T00:00:00"`, `"0001-01-01T00:00:00Z"`} {
		var s StartTime
		if err := json.Unmarshal([]byte(raw), &s); err != nil || !time.Time(s).IsZero() {
			t.Errorf("%s = %v, %v; want the zero time", raw, time.Time(s), err)
		}
	}
	for _, raw := range []string{`"0001-01-02"`, `"2030-01-01T00:00:00"`, `"yesterday"`, `0`, `"10000-01-01T00:00:00Z"`} {
		var s StartTime
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			t.Errorf("%s = %v, want an error", raw, time.Time(s))
		}
	}
	// A time outside what JSON can write is an error, not a silent change.
	if _, err := json.Marshal(StartTime(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))); err == nil {
		t.Error("the year 10000 was encoded")
	}
}

func TestFromErrors(t *testing.T) {
	bad := "PT"
	one := int32(1)
	for name, err := range map[string]error{
		"duration": func() error { _, err := DurationFrom("d", &bad); return err }(),
		"period duration": func() error {
			_, err := PeriodFrom("p", &IntervalPeriod{Duration: &bad})
			return err
		}(),
		"period randomizeStart": func() error {
			_, err := PeriodFrom("p", &IntervalPeriod{RandomizeStart: &bad})
			return err
		}(),
		"values missing": func() error {
			_, err := ValuesMapsFrom("m", []ValuesMap{{Type: "X"}})
			return err
		}(),
		"interval without id": func() error {
			_, err := IntervalsFrom("i", []Interval{{Payloads: []ValuesMap{}}})
			return err
		}(),
		"interval without payloads": func() error {
			_, err := IntervalsFrom("i", []Interval{{ID: &one}})
			return err
		}(),
		"interval period": func() error {
			_, err := IntervalsFrom("i", []Interval{{ID: &one, Payloads: []ValuesMap{}, IntervalPeriod: &IntervalPeriod{Duration: &bad}}})
			return err
		}(),
		"interval payload": func() error {
			_, err := IntervalsFrom("i", []Interval{{ID: &one, Payloads: []ValuesMap{{Type: "X", Values: []json.RawMessage{json.RawMessage(`null`)}}}}})
			return err
		}(),
		"resources missing": func() error { _, err := ReportResourcesFrom(nil); return err }(),
		"resource intervals missing": func() error {
			_, err := ReportResourcesFrom([]ReportResource{{ResourceName: "r"}})
			return err
		}(),
		"resource period": func() error {
			_, err := ReportResourcesFrom([]ReportResource{{Intervals: []Interval{}, IntervalPeriod: &IntervalPeriod{Duration: &bad}}})
			return err
		}(),
		"resource interval": func() error {
			_, err := ReportResourcesFrom([]ReportResource{{Intervals: []Interval{{}}}})
			return err
		}(),
		"body": Decode([]byte(`{`), &struct{}{}),
	} {
		var ve *openadr.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: %v, want a ValidationError", name, err)
		}
	}
}

func TestPayloadDescriptorFrom(t *testing.T) {
	for raw, field := range map[string]string{
		`1`:                      "d",
		`{}`:                     "d.objectType",
		`{"objectType":"OTHER"}`: "d.objectType",
		`{"objectType":7}`:       "d",
		`{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":1}`:    "d",
		`{"objectType":"REPORT_PAYLOAD_DESCRIPTOR","confidence":"x"}`:  "d",
		`{"objectType":"REPORT_PAYLOAD_DESCRIPTOR","confidence":1e40}`: "d",
	} {
		var ve *openadr.ValidationError
		if _, err := payloadDescriptorFrom("d", json.RawMessage(raw)); !errors.As(err, &ve) || ve.Field != field {
			t.Errorf("%s: %v, want a ValidationError for %s", raw, err, field)
		}
	}
	got, err := PayloadDescriptorsFrom("d", []json.RawMessage{
		json.RawMessage(`{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE","currency":"EUR"}`),
		json.RawMessage(`{"objectType":"REPORT_PAYLOAD_DESCRIPTOR","payloadType":"USAGE","confidence":90}`),
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("PayloadDescriptorsFrom = %v, %v", got, err)
	}
	if _, err := PayloadDescriptorsFrom("d", []json.RawMessage{json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "d[0]") {
		t.Errorf("a bad descriptor: %v", err)
	}
}

func TestProblem(t *testing.T) {
	p := openadr.Problem{Type: "about:blank", Title: "Bad Request", Status: 400, Detail: "d", Instance: "/x"}
	data, err := MarshalProblem(p)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := UnmarshalProblem(data); err != nil || back != p {
		t.Errorf("round trip = %+v, %v", back, err)
	}
	if _, err := UnmarshalProblem([]byte(`{"status":"x"}`)); err == nil {
		t.Error("a problem with a string status")
	}
}

func TestMapSlice(t *testing.T) {
	if MapSlice[int, int](nil, func(i int) int { return i }) != nil {
		t.Error("nil did not stay nil")
	}
	if got := MapSlice([]int{}, func(i int) int { return i }); got == nil || len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
}

// TestDecodeUTF8 checks that a body that is not UTF-8 is refused: it was
// read with U+FFFD in place of what was sent.
func TestDecodeUTF8(t *testing.T) {
	var v struct{ Name string }
	for _, body := range []string{"{\"Name\":\"\xff\xfe\"}", "{\"Name\":\"a\xc3\"}", "{\"Name\":\"\xed\xa0\x80\"}", "{\"Name\":\"x\"}\xff"} {
		var ve *openadr.ValidationError
		if err := Decode([]byte(body), &v); !errors.As(err, &ve) || ve.Field != "body" {
			t.Errorf("Decode(%q) = %v (%q), want a ValidationError for body", body, err, v.Name)
		}
	}
	if err := Decode([]byte(`{"Name":"café é 😀"}`), &v); err != nil || v.Name != "café é 😀" {
		t.Errorf("UTF-8 = %q, %v", v.Name, err)
	}
}
