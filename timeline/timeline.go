// SPDX-License-Identifier: MIT

// Package timeline turns the intervals of an event or a report into a
// table: one row per period of time and payload, with the start and end
// worked out.
//
// OpenADR writes intervals compactly. An event has a default period, an
// interval may override it or follow the previous one, a start may mean
// "now", a payload may pack several values into one interval, and the
// duration of the event may cut the intervals short or repeat them. Of and
// OfEvent apply those rules (User Guide, "Event and Interval Timing") so
// that a program does not have to:
//
//	rows, err := timeline.OfEvent(event, timeline.Options{SubIntervals: true})
//	for _, row := range rows.At(time.Now()) {
//		fmt.Println(row.Type, row.Values)
//	}
//
// A Table can be written as CSV and read back, and Intervals makes
// intervals of the rows again, to build an event from a table.
package timeline

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// Row is one payload over one period of time.
type Row struct {
	// IntervalID is the ID of the interval the row comes from. A report
	// uses it to say which interval of the event it is about.
	IntervalID int32
	// Sub numbers the rows that one payload with several values was split
	// into, from 0; see Options.SubIntervals.
	Sub int
	// Start is when the period begins, and End when it is over. A zero End
	// is a period without end.
	Start, End time.Time
	// RandomizeStart is the range within which a client is to shift Start,
	// when not zero. The table shows the times as announced.
	RandomizeStart openadr.Duration
	// Type says what the values are, such as "PRICE".
	Type   string
	Values []openadr.Value
}

// Table is a list of rows, in the order of their intervals.
type Table []Row

// Options decide how intervals are laid out.
type Options struct {
	// Now is the time a start of "now" stands for; the current time when
	// zero.
	Now time.Time
	// SubIntervals splits a payload that holds several values into as many
	// rows of equal length, one value each, as the specification has it
	// for values that were packed into one interval. A payload that holds
	// a point is left whole: its values are a curve, not a series.
	SubIntervals bool
	// Until bounds the rows of an event whose duration repeats its
	// intervals. Without it such an event is laid out once if it repeats
	// without end, and for its whole duration otherwise.
	Until time.Time
}

// maxRows bounds a table, so that an event which repeats short intervals
// for years is an error rather than an allocation.
const maxRows = 1 << 20

// ErrTooLarge is returned for a table of more than about a million rows.
// Set Options.Until to lay out a part of such an event.
var ErrTooLarge = errors.New("timeline: too many rows")

// isNow reports whether a start is the "beginning of time" of the
// specification, which stands for a time that follows from the context.
func isNow(t *time.Time) bool { return t != nil && t.Year() <= 1 }

// Of lays out intervals. period is the default of the intervals, as an
// event, or a resource of a report, carries it; it may be nil when every
// interval has a period of its own.
func Of(period *openadr.IntervalPeriod, intervals []openadr.Interval, o Options) (Table, error) {
	rows, err := layOut(period, intervals, o)
	if err != nil && !errors.Is(err, ErrTooLarge) {
		// The intervals do not say when they are.
		err = fmt.Errorf("%w: %w", openadr.ErrInvalid, err)
	}
	return rows, err
}

func layOut(period *openadr.IntervalPeriod, intervals []openadr.Interval, o Options) (Table, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	var (
		rows Table
		// next is where an interval without a start of its own begins:
		// the end of the one before it.
		next    time.Time
		hasNext bool
	)
	if period != nil && period.Start != nil {
		next, hasNext = *period.Start, true
		if isNow(period.Start) {
			next = now
		}
	}
	for i, iv := range intervals {
		own := iv.IntervalPeriod
		var start time.Time
		switch {
		case own != nil && own.Start != nil && !isNow(own.Start):
			start = *own.Start
		case hasNext:
			start = next
		case i == 0 && own != nil && isNow(own.Start):
			start = now
		case i == 0:
			return nil, fmt.Errorf("timeline: interval %d has no start, and there is no default", iv.ID)
		default:
			return nil, fmt.Errorf("timeline: interval %d follows one that does not end", iv.ID)
		}

		var duration *openadr.Duration
		var randomize openadr.Duration
		if period != nil {
			duration = period.Duration
			if period.RandomizeStart != nil {
				randomize = *period.RandomizeStart
			}
		}
		if own != nil {
			if own.Duration != nil {
				duration = own.Duration
			}
			if own.RandomizeStart != nil {
				randomize = *own.RandomizeStart
			}
		}
		if duration == nil {
			return nil, fmt.Errorf("timeline: interval %d has no duration, and there is no default", iv.ID)
		}
		var end time.Time
		if hasNext = !duration.IsInfinite(); hasNext {
			end = duration.AddTo(start)
			if end.Before(start) {
				return nil, fmt.Errorf("timeline: interval %d has a negative duration", iv.ID)
			}
			next = end
		}

		first := len(rows)
		for _, payload := range iv.Payloads {
			row := Row{IntervalID: iv.ID, Start: start, End: end, RandomizeStart: randomize, Type: payload.Type, Values: payload.Values}
			n := len(payload.Values)
			if !o.SubIntervals || n < 2 || end.IsZero() || slices.ContainsFunc(payload.Values, isPoint) {
				rows = append(rows, row)
				continue
			}
			if len(rows)+n > maxRows {
				return nil, ErrTooLarge
			}
			// The k-th of n parts of the interval begins at k/n of its
			// length: computed without multiplying the whole length by
			// k, which for a year of hourly values would not fit.
			at := fractions(start, end, n)
			for k := range n {
				row.Sub = k
				row.Start, row.End = at(k), at(k+1)
				row.Values = payload.Values[k : k+1 : k+1]
				rows = append(rows, row)
			}
		}
		if len(rows) > maxRows {
			return nil, ErrTooLarge
		}
		// Within an interval, by time: the rows of several split payloads
		// interleave.
		slices.SortStableFunc(rows[first:], func(a, b Row) int { return a.Start.Compare(b.Start) })
	}
	return rows, nil
}

// fractions returns the function that gives the start of the k-th of n
// equal parts of the period from start to end.
func fractions(start, end time.Time, n int) func(k int) time.Time {
	length, parts := end.Sub(start), time.Duration(n)
	if start.Add(length).Equal(end) {
		// Computed without multiplying the whole length by k, which for a
		// year of hourly values would not fit.
		return func(k int) time.Time {
			return start.Add(length/parts*time.Duration(k) + length%parts*time.Duration(k)/parts)
		}
	}
	// Longer than a time.Duration holds (about 292 years): in nanoseconds
	// as a big number.
	total := new(big.Int).Mul(big.NewInt(end.Unix()-start.Unix()), big.NewInt(int64(time.Second)))
	total.Add(total, big.NewInt(int64(end.Nanosecond()-start.Nanosecond())))
	return func(k int) time.Time {
		if k == n {
			return end
		}
		off := new(big.Int).Mul(total, big.NewInt(int64(k)))
		sec, nsec := off.QuoRem(off.Quo(off, big.NewInt(int64(n))), big.NewInt(int64(time.Second)), new(big.Int))
		return time.Unix(start.Unix()+sec.Int64(), int64(start.Nanosecond())+nsec.Int64()).In(start.Location())
	}
}

// shifter returns the function that moves a time by the span from start to
// end. The rows it moves all have an end.
func shifter(start, end time.Time) func(time.Time) time.Time {
	if span := end.Sub(start); start.Add(span).Equal(end) {
		return func(t time.Time) time.Time { return t.Add(span) }
	}
	sec, nsec := end.Unix()-start.Unix(), int64(end.Nanosecond()-start.Nanosecond())
	return func(t time.Time) time.Time {
		return time.Unix(t.Unix()+sec, int64(t.Nanosecond())+nsec).In(t.Location())
	}
}

func isPoint(v openadr.Value) bool { return v.Kind() == openadr.KindPoint }

// OfEvent lays out the intervals of an event, and applies the duration of
// the event: one shorter than the intervals leaves out what lies beyond
// it, and a longer one repeats them.
func OfEvent(e openadr.Event, o Options) (Table, error) {
	rows, err := Of(e.IntervalPeriod, e.Intervals, o)
	if err != nil || e.Duration == nil || len(rows) == 0 {
		return rows, err
	}
	start, end := rows.Span()
	limit := o.Until
	if !e.Duration.IsInfinite() {
		if bound := e.Duration.AddTo(start); limit.IsZero() || bound.Before(limit) {
			limit = bound
		}
	}
	if limit.IsZero() {
		// The intervals repeat without end, and nobody said how far to go.
		return rows, nil
	}
	if !end.IsZero() && end.After(start) && end.Before(limit) {
		// Each repetition is the one before moved by the span of the
		// intervals, which may be too long for a time.Duration.
		shift := shifter(start, end)
		prev := rows
		for first := shift(start); first.Before(limit); first = shift(first) {
			if len(rows)+len(prev) > maxRows {
				return nil, ErrTooLarge
			}
			from := len(rows)
			for _, row := range prev {
				row.Start, row.End = shift(row.Start), shift(row.End)
				rows = append(rows, row)
			}
			prev = rows[from:]
		}
	}
	out := rows[:0:0]
	for _, row := range rows {
		if !row.Start.Before(limit) {
			continue
		}
		if row.End.IsZero() || row.End.After(limit) {
			row.End = limit
		}
		out = append(out, row)
	}
	return out, nil
}

// Span returns the earliest start and the latest end of the rows. The end
// is zero when a row has none, and both are for an empty table.
func (t Table) Span() (start, end time.Time) {
	open := false
	for i, row := range t {
		if i == 0 || row.Start.Before(start) {
			start = row.Start
		}
		open = open || row.End.IsZero()
		if row.End.After(end) {
			end = row.End
		}
	}
	if open {
		end = time.Time{}
	}
	return start, end
}

// At returns the rows whose period holds the given time: what applies then.
func (t Table) At(when time.Time) Table {
	var out Table
	for _, row := range t {
		if !when.Before(row.Start) && (row.End.IsZero() || when.Before(row.End)) {
			out = append(out, row)
		}
	}
	return out
}

// Intervals makes intervals of the rows, each with a period of its own, so
// that they need no default. Consecutive rows with the same IntervalID
// become one interval that spans them; within it, the rows of one Type
// become one payload, with their values in order. It is the reverse of Of
// with or without SubIntervals, for rows that are contiguous.
func (t Table) Intervals() []openadr.Interval {
	var out []openadr.Interval
	for i := 0; i < len(t); {
		j := i + 1
		for j < len(t) && t[j].IntervalID == t[i].IntervalID {
			j++
		}
		group := t[i:j]
		start, end := group.Span()
		period := &openadr.IntervalPeriod{Start: &start}
		duration := openadr.Infinite
		if !end.IsZero() {
			duration = openadr.DurationOf(end.Sub(start))
		}
		period.Duration = &duration
		if r := group[0].RandomizeStart; !r.IsZero() {
			period.RandomizeStart = &r
		}
		iv := openadr.Interval{ID: group[0].IntervalID, IntervalPeriod: period}
		for _, row := range group {
			k := slices.IndexFunc(iv.Payloads, func(p openadr.ValuesMap) bool { return p.Type == row.Type })
			if k < 0 {
				iv.Payloads = append(iv.Payloads, openadr.ValuesMap{Type: row.Type})
				k = len(iv.Payloads) - 1
			}
			iv.Payloads[k].Values = append(iv.Payloads[k].Values, row.Values...)
		}
		out = append(out, iv)
		i = j
	}
	return out
}
