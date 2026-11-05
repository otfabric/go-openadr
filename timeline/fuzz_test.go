// SPDX-License-Identifier: MIT

package timeline_test

import (
	"errors"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/objectjson"
	"github.com/otfabric/go-openadr/timeline"
)

// FuzzOfEvent lays out events decoded from untrusted JSON, as a VEN does
// with what its VTN sends. It must not panic or loop, and what it returns
// must hold together: every row ends at or after its start, the rows of an
// event with a duration lie within it, and the rows a payload was split
// into tile the interval they came from.
func FuzzOfEvent(f *testing.F) {
	for _, s := range []string{
		`{"programID":"p","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"PT1H"},"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[1,2,3]}]},{"id":1,"payloads":[{"type":"PRICE","values":[4]}]}]}`,
		`{"programID":"p","duration":"P1D","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"PT1H"},"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[1]}]}]}`,
		`{"programID":"p","duration":"P9000Y","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"P1000Y"},"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[1,2]}]}]}`,
		`{"programID":"p","duration":"PT1H","intervalPeriod":{"start":"0001-01-01","duration":"PT3000000H"},"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[1]}]}]}`,
		`{"programID":"p","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"P4294967297Y"},"intervals":[{"id":0,"payloads":[]}]}`,
		`{"programID":"p","intervals":[{"id":0,"intervalPeriod":{"start":"2030-01-01T00:00:00+05:00","duration":"P1W","randomizeStart":"PT5M"},"payloads":[{"type":"X","values":[{"x":1,"y":2}]}]},{"id":1,"intervalPeriod":{"duration":"P9999Y"},"payloads":[{"type":"X","values":[true]}]}]}`,
	} {
		f.Add([]byte(s), true)
	}
	now := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, data []byte, split bool) {
		e, err := objectjson.Unmarshal[openadr.Event]("", data)
		if err != nil {
			return
		}
		rows, err := timeline.OfEvent(e, timeline.Options{Now: now, SubIntervals: split})
		if err != nil {
			if !errors.Is(err, openadr.ErrInvalid) && !errors.Is(err, timeline.ErrTooLarge) {
				t.Fatalf("OfEvent: %v is neither ErrInvalid nor ErrTooLarge", err)
			}
			return
		}
		for i, row := range rows {
			if !row.End.IsZero() && row.End.Before(row.Start) {
				t.Fatalf("row %d ends %v before it starts %v", i, row.End, row.Start)
			}
		}
		if e.Duration == nil || e.Duration.IsInfinite() || len(rows) == 0 {
			return
		}
		plain, err := timeline.Of(e.IntervalPeriod, e.Intervals, timeline.Options{Now: now, SubIntervals: split})
		if err != nil || len(plain) == 0 {
			t.Fatalf("Of failed where OfEvent did not: %v", err)
		}
		start, _ := plain.Span()
		limit := e.Duration.AddTo(start)
		for i, row := range rows {
			if row.Start.Before(start) || !row.Start.Before(limit) || row.End.After(limit) {
				t.Fatalf("row %d (%v to %v) is outside the event, %v to %v", i, row.Start, row.End, start, limit)
			}
		}
	})
}

// FuzzOfSplit checks that the rows a payload is split into tile its
// interval: the first starts with it, each starts where the one before
// ended, and the last ends with it.
func FuzzOfSplit(f *testing.F) {
	f.Add("PT1H", uint16(3))
	f.Add("P1000Y", uint16(2))
	f.Add("PT0.000000001S", uint16(7))
	f.Add("P300Y", uint16(1000))
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, length string, n uint16) {
		d, err := openadr.ParseDuration(length)
		if err != nil || d.IsInfinite() || n == 0 {
			return
		}
		values := make([]openadr.Value, n)
		for i := range values {
			values[i] = openadr.Number(float64(i))
		}
		rows, err := timeline.Of(&openadr.IntervalPeriod{Start: &start, Duration: &d},
			[]openadr.Interval{{Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: values}}}},
			timeline.Options{SubIntervals: true})
		if err != nil {
			return
		}
		end := d.AddTo(start)
		if !rows[0].Start.Equal(start) || !rows[len(rows)-1].End.Equal(end) {
			t.Fatalf("%s in %d: rows span %v to %v, the interval %v to %v", length, n, rows[0].Start, rows[len(rows)-1].End, start, end)
		}
		for i := 1; i < len(rows); i++ {
			if !rows[i].Start.Equal(rows[i-1].End) {
				t.Fatalf("%s in %d: row %d starts %v, the one before ends %v", length, n, i, rows[i].Start, rows[i-1].End)
			}
		}
	})
}

// TestLongerThanADuration lays out intervals longer than a time.Duration
// holds (about 292 years). Their split rows ended at 2322, wherever the
// interval did, and an event repeating them was ErrTooLarge, as the shift
// of each repetition overflowed.
func TestLongerThanADuration(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	thousand := openadr.Duration{Years: 1000}
	rows, err := timeline.Of(&openadr.IntervalPeriod{Start: &start, Duration: &thousand},
		[]openadr.Interval{{Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(1), openadr.Number(2)}}}}},
		timeline.Options{SubIntervals: true})
	if err != nil || len(rows) != 2 {
		t.Fatalf("split = %v, %v", rows, err)
	}
	end := start.AddDate(1000, 0, 0)
	span := end.Unix() - start.Unix()
	mid := time.Unix(start.Unix()+span/2, 0).UTC()
	if !rows[0].Start.Equal(start) || !rows[0].End.Equal(mid) || !rows[1].Start.Equal(mid) || !rows[1].End.Equal(end) {
		t.Errorf("split rows: %v to %v, %v to %v", rows[0].Start, rows[0].End, rows[1].Start, rows[1].End)
	}

	// Repeated by the span of the intervals, as a shorter one is, up to
	// the end of the event, which cuts the last repetition.
	event := openadr.Event{
		Duration:       &openadr.Duration{Years: 9000},
		IntervalPeriod: &openadr.IntervalPeriod{Start: &start, Duration: &thousand},
		Intervals:      []openadr.Interval{{Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(1)}}}}},
	}
	rows, err = timeline.OfEvent(event, timeline.Options{})
	limit := start.AddDate(9000, 0, 0)
	if want := int((limit.Unix() - start.Unix() + span - 1) / span); err != nil || len(rows) != want {
		t.Fatalf("P9000Y of P1000Y = %d rows, %v; want %d", len(rows), err, want)
	}
	for i, row := range rows {
		want := time.Unix(start.Unix()+int64(i)*span, 0).UTC()
		wantEnd := time.Unix(start.Unix()+int64(i+1)*span, 0).UTC()
		if i == len(rows)-1 {
			wantEnd = limit
		}
		if !row.Start.Equal(want) || !row.End.Equal(wantEnd) {
			t.Errorf("repetition %d is %v to %v, want %v to %v", i, row.Start, row.End, want, wantEnd)
		}
	}

	// 342 years of hours made an interval of no length.
	hours := openadr.Duration{Hours: 3_000_000}
	rows, err = timeline.Of(&openadr.IntervalPeriod{Start: &start, Duration: &hours},
		[]openadr.Interval{{Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(1)}}}}}, timeline.Options{})
	if err != nil || len(rows) != 1 || !rows[0].End.Equal(start.AddDate(0, 0, 125_000)) {
		t.Errorf("PT3000000H = %v, %v", rows, err)
	}
}
