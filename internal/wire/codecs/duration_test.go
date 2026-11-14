// SPDX-License-Identifier: MIT

package codecs

import (
	"errors"
	"math"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// What an encoder writes, its decoder reads back as the same duration; a
// duration that has no ISO 8601 form is refused with the field it is in.
func TestDurationsReadBack(t *testing.T) {
	const max = math.MaxInt64
	good := []openadr.Duration{
		{},
		{Hours: 1},
		{Negative: true, Days: 1, Seconds: 0.5},
		{Weeks: max},
		{Days: max},
		{Days: 7, Weeks: max/7 - 1},
		{Years: max, Months: max, Days: max, Hours: max, Minutes: max, Seconds: math.MaxFloat64},
		{Seconds: math.SmallestNonzeroFloat64},
		{Seconds: math.Copysign(0, -1)},
	}
	bad := []openadr.Duration{
		{Seconds: math.NaN()},
		{Seconds: math.Inf(1)},
		{Seconds: math.Inf(-1)},
		{Seconds: -1},
		{Hours: -5},
		{Negative: true, Hours: math.MinInt64},
		{Years: -1},
		{Months: -1},
		{Weeks: -1},
		{Days: -1},
		{Minutes: -1},
		{Days: 8, Weeks: max/7 - 1},
		{Days: 1, Weeks: max},
		{Days: 1, Weeks: 2635249153387078803},
	}
	t0 := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	meta := openadr.Metadata{ID: "o1", Created: t0, Modified: t0}
	type place struct {
		field string
		// write encodes d where field says; read gives it back.
		write func(c *wire.Codec, d openadr.Duration) ([]byte, error)
		read  func(c *wire.Codec, data []byte) (openadr.Duration, error)
	}
	// A 3.0.1 report names its program; a 3.1.0 report does not.
	var programID string
	period := func(d openadr.Duration) *openadr.IntervalPeriod { return &openadr.IntervalPeriod{Duration: &d} }
	intervals := func(d openadr.Duration) []openadr.Interval {
		return []openadr.Interval{{IntervalPeriod: &openadr.IntervalPeriod{RandomizeStart: &d}, Payloads: []openadr.ValuesMap{}}}
	}
	places := []place{
		{"intervalPeriod.duration",
			func(c *wire.Codec, d openadr.Duration) ([]byte, error) {
				return c.Programs.Marshal(openadr.Program{Metadata: meta, Name: "p", IntervalPeriod: period(d)}, wire.Object)
			},
			func(c *wire.Codec, data []byte) (openadr.Duration, error) {
				p, _, err := c.Programs.Unmarshal(data)
				if err != nil {
					return openadr.Duration{}, err
				}
				return *p.IntervalPeriod.Duration, nil
			}},
		{"intervals[0].intervalPeriod.randomizeStart",
			func(c *wire.Codec, d openadr.Duration) ([]byte, error) {
				return c.Events.Marshal(openadr.Event{Metadata: meta, ProgramID: "p1", Intervals: intervals(d)}, wire.Object)
			},
			func(c *wire.Codec, data []byte) (openadr.Duration, error) {
				e, _, err := c.Events.Unmarshal(data)
				if err != nil {
					return openadr.Duration{}, err
				}
				return *e.Intervals[0].IntervalPeriod.RandomizeStart, nil
			}},
		{"resources[0].intervalPeriod.duration",
			func(c *wire.Codec, d openadr.Duration) ([]byte, error) {
				r := openadr.Report{Metadata: meta, ProgramID: programID, EventID: "e1", ClientName: "c",
					Resources: []openadr.ReportResource{{ResourceName: "r", IntervalPeriod: period(d), Intervals: []openadr.Interval{}}}}
				return c.Reports.Marshal(r, wire.Object)
			},
			func(c *wire.Codec, data []byte) (openadr.Duration, error) {
				r, _, err := c.Reports.Unmarshal(data)
				if err != nil {
					return openadr.Duration{}, err
				}
				return *r.Resources[0].IntervalPeriod.Duration, nil
			}},
		{"resources[0].intervals[0].intervalPeriod.randomizeStart",
			func(c *wire.Codec, d openadr.Duration) ([]byte, error) {
				r := openadr.Report{Metadata: meta, ProgramID: programID, EventID: "e1", ClientName: "c",
					Resources: []openadr.ReportResource{{ResourceName: "r", Intervals: intervals(d)}}}
				return c.Reports.Marshal(r, wire.Object)
			},
			func(c *wire.Codec, data []byte) (openadr.Duration, error) {
				r, _, err := c.Reports.Unmarshal(data)
				if err != nil {
					return openadr.Duration{}, err
				}
				return *r.Resources[0].Intervals[0].IntervalPeriod.RandomizeStart, nil
			}},
	}
	for _, v := range []openadr.Version{openadr.Version310, openadr.Version301} {
		c, err := For(v)
		if err != nil {
			t.Fatal(err)
		}
		programID = ""
		if v == openadr.Version301 {
			programID = "p1"
		}
		all := places
		if v == openadr.Version310 {
			// 3.0.1 has no duration of an event.
			all = append(all, place{"duration",
				func(c *wire.Codec, d openadr.Duration) ([]byte, error) {
					return c.Events.Marshal(openadr.Event{Metadata: meta, ProgramID: "p1", Duration: &d}, wire.Object)
				},
				func(c *wire.Codec, data []byte) (openadr.Duration, error) {
					e, _, err := c.Events.Unmarshal(data)
					if err != nil {
						return openadr.Duration{}, err
					}
					return *e.Duration, nil
				}})
		}
		for _, p := range all {
			for _, d := range good {
				data, err := p.write(c, d)
				if err != nil {
					t.Errorf("%s %s %s: Marshal: %v", v, p.field, d, err)
					continue
				}
				back, err := p.read(c, data)
				if err != nil || back.String() != d.String() {
					t.Errorf("%s %s %s: read back as %s, %v", v, p.field, d, back, err)
				}
			}
			for _, d := range bad {
				data, err := p.write(c, d)
				var ve *openadr.ValidationError
				if !errors.As(err, &ve) || ve.Field != p.field {
					t.Errorf("%s %s %+v: Marshal = %s, %v; want a ValidationError of %s", v, p.field, d, data, err, p.field)
				}
			}
		}
	}
}
