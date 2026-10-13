// SPDX-License-Identifier: MIT

package timeline_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/timeline"
)

var midnight = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func at(hours float64) time.Time {
	return midnight.Add(time.Duration(hours * float64(time.Hour)))
}

func duration(s string) *openadr.Duration {
	d, err := openadr.ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return &d
}

func period(start time.Time, length string) *openadr.IntervalPeriod {
	return &openadr.IntervalPeriod{Start: &start, Duration: duration(length)}
}

// beginning is a period with the start the specification writes for "now"
// and for "after the previous interval".
func beginning(length string) *openadr.IntervalPeriod {
	p := &openadr.IntervalPeriod{Start: &time.Time{}}
	if length != "" {
		p.Duration = duration(length)
	}
	return p
}

func prices(id int32, p *openadr.IntervalPeriod, values ...float64) openadr.Interval {
	iv := openadr.Interval{ID: id, IntervalPeriod: p, Payloads: []openadr.ValuesMap{{Type: "PRICE"}}}
	for _, v := range values {
		iv.Payloads[0].Values = append(iv.Payloads[0].Values, openadr.Number(v))
	}
	return iv
}

// spans writes a table as "id.sub start-end values", in hours from midnight.
func spans(t timeline.Table) []string {
	var out []string
	for _, row := range t {
		end := "∞"
		if !row.End.IsZero() {
			end = fmt.Sprint(row.End.Sub(midnight).Hours())
		}
		out = append(out, fmt.Sprintf("%d.%d %v-%s %v", row.IntervalID, row.Sub, row.Start.Sub(midnight).Hours(), end, row.Values))
	}
	return out
}

func TestOf(t *testing.T) {
	now := at(6.5)
	tests := []struct {
		name      string
		period    *openadr.IntervalPeriod
		intervals []openadr.Interval
		options   timeline.Options
		want      []string
	}{
		{
			name:      "the default period applies to every interval in turn",
			period:    period(midnight, "PT1H"),
			intervals: []openadr.Interval{prices(0, nil, 0.17), prices(1, nil, 0.21), prices(2, nil, 0.09)},
			want:      []string{"0.0 0-1 [0.17]", "1.0 1-2 [0.21]", "2.0 2-3 [0.09]"},
		},
		{
			name:   "an interval overrides the duration, and the next follows it",
			period: period(midnight, "PT1H"),
			intervals: []openadr.Interval{
				prices(0, nil, 1), prices(1, &openadr.IntervalPeriod{Duration: duration("PT3H")}, 2), prices(2, nil, 3),
			},
			want: []string{"0.0 0-1 [1]", "1.0 1-4 [2]", "2.0 4-5 [3]"},
		},
		{
			name:      "an interval with a start of its own leaves a gap, and the next follows it",
			period:    period(midnight, "PT1H"),
			intervals: []openadr.Interval{prices(0, nil, 1), prices(1, period(at(5), "PT2H"), 2), prices(2, nil, 3)},
			want:      []string{"0.0 0-1 [1]", "1.0 5-7 [2]", "2.0 7-8 [3]"},
		},
		{
			name:      "the beginning of time on an interval means: after the previous one",
			period:    period(midnight, "PT1H"),
			intervals: []openadr.Interval{prices(0, beginning(""), 1), prices(1, beginning("PT30M"), 2)},
			want:      []string{"0.0 0-1 [1]", "1.0 1-1.5 [2]"},
		},
		{
			name:      "the beginning of time on the event means: now",
			period:    beginning("PT1H"),
			intervals: []openadr.Interval{prices(0, nil, 1), prices(1, nil, 2)},
			options:   timeline.Options{Now: now},
			want:      []string{"0.0 6.5-7.5 [1]", "1.0 7.5-8.5 [2]"},
		},
		{
			name:      "without a default, the beginning of time on the first interval means: now",
			intervals: []openadr.Interval{prices(0, beginning("PT1H"), 1), prices(1, beginning("PT1H"), 2)},
			options:   timeline.Options{Now: now},
			want:      []string{"0.0 6.5-7.5 [1]", "1.0 7.5-8.5 [2]"},
		},
		{
			name:      "P9999Y does not end",
			period:    period(midnight, "P9999Y"),
			intervals: []openadr.Interval{prices(0, nil, 1)},
			want:      []string{"0.0 0-∞ [1]"},
		},
		{
			name:      "several values stay together by default",
			period:    period(midnight, "PT3H"),
			intervals: []openadr.Interval{prices(0, nil, 1, 2, 3)},
			want:      []string{"0.0 0-3 [1 2 3]"},
		},
		{
			name:      "several values are sub-intervals of equal length when asked",
			period:    period(midnight, "PT3H"),
			intervals: []openadr.Interval{prices(0, nil, 1, 2, 3), prices(1, nil, 4, 5)},
			options:   timeline.Options{SubIntervals: true},
			want:      []string{"0.0 0-1 [1]", "0.1 1-2 [2]", "0.2 2-3 [3]", "1.0 3-4.5 [4]", "1.1 4.5-6 [5]"},
		},
		{
			name:   "a curve of points is not a series",
			period: period(midnight, "PT1H"),
			intervals: []openadr.Interval{{ID: 7, Payloads: []openadr.ValuesMap{
				{Type: "CURVE", Values: []openadr.Value{openadr.PointValue(0, 1), openadr.PointValue(1, 2)}},
			}}},
			options: timeline.Options{SubIntervals: true},
			want:    []string{"7.0 0-1 [(0,1) (1,2)]"},
		},
		{
			name:      "months are calendar months",
			period:    period(midnight, "P1M"),
			intervals: []openadr.Interval{prices(0, nil, 1), prices(1, nil, 2)},
			want:      []string{"0.0 0-744 [1]", "1.0 744-1416 [2]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := timeline.Of(tt.period, tt.intervals, tt.options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spans(got), tt.want) {
				t.Errorf("got\n  %s\nwant\n  %s", strings.Join(spans(got), "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

func TestOfInterleavesSplitPayloads(t *testing.T) {
	iv := prices(0, nil, 1, 2)
	iv.Payloads = append(iv.Payloads, openadr.ValuesMap{Type: "GHG", Values: []openadr.Value{openadr.Number(9)}})
	rows, err := timeline.Of(period(midnight, "PT2H"), []openadr.Interval{iv}, timeline.Options{SubIntervals: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		got = append(got, fmt.Sprintf("%s %v-%v", row.Type, row.Start.Sub(midnight).Hours(), row.End.Sub(midnight).Hours()))
	}
	if want := []string{"PRICE 0-1", "GHG 0-2", "PRICE 1-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A long interval with many values: the parts are still equal, begin where
// the one before ended, and end where the interval does.
func TestSubIntervalsOfALongInterval(t *testing.T) {
	const hours = 10 * 8760
	values := make([]float64, hours)
	rows, err := timeline.Of(period(midnight, "PT87600H"), []openadr.Interval{prices(0, nil, values...)}, timeline.Options{SubIntervals: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != hours {
		t.Fatalf("%d rows", len(rows))
	}
	for k, row := range rows {
		if !row.Start.Equal(at(float64(k))) || !row.End.Equal(at(float64(k+1))) {
			t.Fatalf("row %d is %v to %v", k, row.Start, row.End)
		}
	}
	// And a length that does not divide evenly: no gaps, no overlaps.
	rows, err = timeline.Of(period(midnight, "PT10S"), []openadr.Interval{prices(0, nil, 1, 2, 3, 4, 5, 6, 7)}, timeline.Options{SubIntervals: true})
	if err != nil {
		t.Fatal(err)
	}
	for k := 1; k < len(rows); k++ {
		if !rows[k].Start.Equal(rows[k-1].End) || !rows[k].End.After(rows[k].Start) {
			t.Errorf("row %d: %v to %v after %v", k, rows[k].Start, rows[k].End, rows[k-1].End)
		}
	}
	if !rows[0].Start.Equal(midnight) || !rows[6].End.Equal(midnight.Add(10*time.Second)) {
		t.Errorf("the parts span %v to %v", rows[0].Start, rows[6].End)
	}
}

func TestOfErrors(t *testing.T) {
	negative := openadr.Duration{Negative: true, Hours: 1}
	tests := []struct {
		name      string
		period    *openadr.IntervalPeriod
		intervals []openadr.Interval
		want      string
	}{
		{"no start anywhere", &openadr.IntervalPeriod{Duration: duration("PT1H")}, []openadr.Interval{prices(3, nil, 1)}, "interval 3 has no start"},
		{"no duration anywhere", &openadr.IntervalPeriod{Start: &midnight}, []openadr.Interval{prices(3, nil, 1)}, "interval 3 has no duration"},
		{"no period at all", nil, []openadr.Interval{prices(3, nil, 1)}, "interval 3 has no start"},
		{"after an interval without end", period(midnight, "P9999Y"), []openadr.Interval{prices(0, nil, 1), prices(1, nil, 2)}, "interval 1 follows one that does not end"},
		{"a negative duration", &openadr.IntervalPeriod{Start: &midnight, Duration: &negative}, []openadr.Interval{prices(0, nil, 1)}, "negative duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := timeline.Of(tt.period, tt.intervals, timeline.Options{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got error %v, want one that says %q", err, tt.want)
			}
		})
	}
}

func TestOfEventDuration(t *testing.T) {
	day := func(length string, until time.Time) ([]string, error) {
		e := openadr.Event{
			IntervalPeriod: period(midnight, "PT1H"),
			Intervals:      []openadr.Interval{prices(0, nil, 1), prices(1, nil, 2), prices(2, nil, 3)},
		}
		if length != "" {
			e.Duration = duration(length)
		}
		rows, err := timeline.OfEvent(e, timeline.Options{Until: until})
		return spans(rows), err
	}
	tests := []struct {
		name   string
		length string
		until  time.Time
		want   []string
	}{
		{"no duration: the intervals as they are", "", time.Time{}, []string{"0.0 0-1 [1]", "1.0 1-2 [2]", "2.0 2-3 [3]"}},
		{"a shorter duration leaves intervals out", "PT2H", time.Time{}, []string{"0.0 0-1 [1]", "1.0 1-2 [2]"}},
		{"and cuts the one it ends in", "PT90M", time.Time{}, []string{"0.0 0-1 [1]", "1.0 1-1.5 [2]"}},
		{"a longer duration repeats them", "PT7H", time.Time{}, []string{
			"0.0 0-1 [1]", "1.0 1-2 [2]", "2.0 2-3 [3]", "0.0 3-4 [1]", "1.0 4-5 [2]", "2.0 5-6 [3]", "0.0 6-7 [1]"}},
		{"forever is laid out once", "P9999Y", time.Time{}, []string{"0.0 0-1 [1]", "1.0 1-2 [2]", "2.0 2-3 [3]"}},
		{"or as far as asked", "P9999Y", at(4), []string{"0.0 0-1 [1]", "1.0 1-2 [2]", "2.0 2-3 [3]", "0.0 3-4 [1]"}},
		{"until does not extend a duration", "PT2H", at(100), []string{"0.0 0-1 [1]", "1.0 1-2 [2]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := day(tt.length, tt.until)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}

	t.Run("a table that would be huge is refused", func(t *testing.T) {
		e := openadr.Event{
			Duration:       duration("P50Y"),
			IntervalPeriod: period(midnight, "PT1S"),
			Intervals:      []openadr.Interval{prices(0, nil, 1)},
		}
		if _, err := timeline.OfEvent(e, timeline.Options{}); !errors.Is(err, timeline.ErrTooLarge) {
			t.Errorf("got %v, want ErrTooLarge", err)
		}
	})
}

func TestAt(t *testing.T) {
	rows, err := timeline.Of(period(midnight, "PT1H"), []openadr.Interval{prices(0, nil, 1), prices(1, nil, 2)}, timeline.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for when, want := range map[float64]string{-0.1: "[]", 0: "[0.0 0-1 [1]]", 0.99: "[0.0 0-1 [1]]", 1: "[1.0 1-2 [2]]", 2: "[]"} {
		if got := fmt.Sprint(spans(rows.At(at(when)))); got != want {
			t.Errorf("at %v h: got %s, want %s", when, got, want)
		}
	}
	if start, end := rows.Span(); !start.Equal(midnight) || !end.Equal(at(2)) {
		t.Errorf("span %v to %v", start, end)
	}
}

// What Of lays out, Intervals folds back: the result lays out the same.
func TestIntervalsRoundTrip(t *testing.T) {
	iv := prices(4, nil, 1, 2, 3)
	iv.Payloads = append(iv.Payloads, openadr.ValuesMap{Type: "GHG", Values: []openadr.Value{openadr.Number(9), openadr.Number(8), openadr.Number(7)}})
	def := period(midnight, "PT3H")
	def.RandomizeStart = &openadr.Duration{Minutes: 5}
	source := []openadr.Interval{iv, prices(5, nil, 4), prices(6, period(at(10), "P9999Y"), 5)}

	for _, split := range []bool{false, true} {
		rows, err := timeline.Of(def, source, timeline.Options{SubIntervals: split})
		if err != nil {
			t.Fatal(err)
		}
		folded := rows.Intervals()
		if len(folded) != 3 || len(folded[0].Payloads) != 2 || len(folded[0].Payloads[0].Values) != 3 {
			t.Fatalf("split=%v: folded into %+v", split, folded)
		}
		if got := folded[0].IntervalPeriod; !got.Start.Equal(midnight) || got.Duration.String() != "PT3H" || got.RandomizeStart.String() != "PT5M" {
			t.Errorf("split=%v: period %v %v %v", split, got.Start, got.Duration, got.RandomizeStart)
		}
		if !folded[2].IntervalPeriod.Duration.IsInfinite() {
			t.Errorf("split=%v: the interval without end got duration %v", split, folded[2].IntervalPeriod.Duration)
		}
		again, err := timeline.Of(nil, folded, timeline.Options{SubIntervals: split})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(spans(again), spans(rows)) {
			t.Errorf("split=%v: got\n  %s\nwant\n  %s", split, strings.Join(spans(again), "\n  "), strings.Join(spans(rows), "\n  "))
		}
		e := openadr.Event{ProgramID: "p1", Intervals: folded}
		if err := e.Validate(); err != nil {
			t.Errorf("split=%v: the folded intervals do not make a valid event: %v", split, err)
		}
	}
}

func TestCSVRoundTrip(t *testing.T) {
	zone := time.FixedZone("CET", 3600)
	table := timeline.Table{
		{IntervalID: 0, Start: midnight, End: at(1), Type: "PRICE", Values: []openadr.Value{openadr.Number(0.17)}},
		{IntervalID: 0, Sub: 1, Start: at(1).In(zone), End: at(2).In(zone), Type: "PRICE", Values: []openadr.Value{openadr.Number(-3)}},
		{IntervalID: 1, Start: at(2), Type: "SIMPLE", Values: []openadr.Value{openadr.Number(1), openadr.Number(2)}, RandomizeStart: openadr.Duration{Minutes: 5}},
		{IntervalID: -2, Start: at(3), End: at(3.5), Type: "X-NOTE, \"quoted\"", Values: []openadr.Value{
			openadr.String("a, b\nc"), openadr.String("12"), openadr.String("true"), openadr.Bool(true), openadr.PointValue(1.5, -2),
		}},
	}
	var buf bytes.Buffer
	if err := table.WriteCSV(&buf); err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(buf.String(), "\n"); first != "interval,sub,start,end,type,values,randomize_start" {
		t.Errorf("header %q", first)
	}
	if !strings.Contains(buf.String(), "0,0,2030-01-01T00:00:00Z,2030-01-01T01:00:00Z,PRICE,0.17,\n") {
		t.Errorf("the first row is not as documented:\n%s", buf.String())
	}
	got, err := timeline.ReadCSV(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(table) {
		t.Fatalf("read %d rows, wrote %d", len(got), len(table))
	}
	for i := range table {
		w, g := table[i], got[i]
		if !g.Start.Equal(w.Start) || !g.End.Equal(w.End) || g.Start.Format(time.RFC3339) != w.Start.Format(time.RFC3339) {
			t.Errorf("row %d: times %v-%v, want %v-%v", i, g.Start, g.End, w.Start, w.End)
		}
		g.Start, g.End, w.Start, w.End = time.Time{}, time.Time{}, time.Time{}, time.Time{}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("row %d: got %+v, want %+v", i, g, w)
		}
	}
}

func TestReadCSVByHand(t *testing.T) {
	// Columns in another order, one unknown, no interval, text not quoted,
	// and the byte order mark of a spreadsheet.
	in := "\xef\xbb\xbftype,values,note,start,end\n" +
		"PRICE,0.25,cheap,2030-01-01T00:00:00Z,2030-01-01T01:00:00Z\n" +
		"MODE,eco,,2030-01-01T01:00:00+01:00,\n" +
		"SIMPLE,\"[1, 2]\",,2030-01-01T02:00:00Z,2030-01-01T03:00:00Z\n"
	got, err := timeline.ReadCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0.0 0-1 [0.25]", "1.0 0-∞ [\"eco\"]", "2.0 2-3 [1 2]"}
	if !reflect.DeepEqual(spans(got), want) {
		t.Errorf("got %v, want %v", spans(got), want)
	}
}

func TestReadCSVErrors(t *testing.T) {
	const head = "interval,sub,start,end,type,values,randomize_start\n"
	tests := map[string]string{
		"":             "no header line",
		"start,type\n": `no column "values"`,
		head + "x,0,2030-01-01T00:00:00Z,,PRICE,1,\n":                     "row 0, column interval",
		head + "0,-1,2030-01-01T00:00:00Z,,PRICE,1,\n":                    "row 0, column sub",
		head + "0,0,yesterday,,PRICE,1,\n":                                "row 0, column start",
		head + "0,0,2030-01-01T00:00:00Z,2029-01-01T00:00:00Z,PRICE,1,\n": "is before start",
		head + "0,0,2030-01-01T00:00:00Z,,,1,\n":                          "row 0, column type",
		head + "0,0,2030-01-01T00:00:00Z,,PRICE,,\n":                      "row 0, column values",
		head + "0,0,2030-01-01T00:00:00Z,,PRICE,null,\n":                  "row 0, column values",
		head + "0,0,2030-01-01T00:00:00Z,,PRICE,\"{\"\"x\"\":1}\",\n":     "row 0, column values",
		head + "0,0,2030-01-01T00:00:00Z,,PRICE,1,soon\n":                 "row 0, column randomize_start",
		head + "0,0,2030-01-01T00:00:00Z,,PRICE,1,\n\"open":               "timeline:",
	}
	for in, want := range tests {
		if _, err := timeline.ReadCSV(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want one that says %q", in, err, want)
		}
	}
}

func TestWriteCSVRejectsUnsetValue(t *testing.T) {
	table := timeline.Table{{Start: midnight, Type: "PRICE", Values: []openadr.Value{{}}}}
	if err := table.WriteCSV(&bytes.Buffer{}); err == nil {
		t.Error("an unset value was written")
	}
}

func FuzzReadCSV(f *testing.F) {
	f.Add("interval,sub,start,end,type,values,randomize_start\n0,0,2030-01-01T00:00:00Z,2030-01-01T01:00:00Z,PRICE,0.17,PT5M\n")
	f.Add("type,values,start\nMODE,eco,2030-01-01T01:00:00+01:00\nP,\"[{\"\"x\"\":1,\"\"y\"\":2},true]\",2030-01-01T01:00:00Z\n")
	f.Fuzz(func(t *testing.T, in string) {
		table, err := timeline.ReadCSV(strings.NewReader(in))
		if err != nil {
			return
		}
		// What was read can be written, and reads back the same.
		var buf bytes.Buffer
		if err := table.WriteCSV(&buf); err != nil {
			t.Fatalf("writing what was read: %v", err)
		}
		again, err := timeline.ReadCSV(&buf)
		if err != nil {
			t.Fatalf("reading what was written: %v", err)
		}
		if len(again) != len(table) {
			t.Fatalf("%d rows became %d", len(table), len(again))
		}
		_ = table.Intervals()
	})
}

func ExampleOfEvent() {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	event := openadr.Event{
		IntervalPeriod: &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Hours: 1}},
		Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.17)}}}},
			// Two prices packed into one interval: half an hour each.
			{ID: 1, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.21), openadr.Number(0.09)}}}},
		},
	}
	rows, err := timeline.OfEvent(event, timeline.Options{SubIntervals: true})
	if err != nil {
		panic(err)
	}
	for _, row := range rows {
		fmt.Println(row.Start.Format("15:04"), row.End.Format("15:04"), row.Type, row.Values[0])
	}
	// Output:
	// 00:00 01:00 PRICE 0.17
	// 01:00 01:30 PRICE 0.21
	// 01:30 02:00 PRICE 0.09
}
