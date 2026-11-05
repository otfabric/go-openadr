// SPDX-License-Identifier: MIT

package openadr

import (
	"math"
	"testing"
	"time"
	_ "time/tzdata" // the test needs a zone with summer time wherever it runs
)

// TestAddToLong checks durations too long for a time.Duration: they moved a
// time by nothing at all (PT3000000H, 342 years of hours), or wrapped the
// calendar around (P9223372036854775807Y), before AddTo learnt to apply them.
func TestAddToLong(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	hours := 3_000_000
	want := start.AddDate(0, 0, hours/24)
	for _, tc := range []struct {
		d    Duration
		want time.Time
	}{
		{Duration{Hours: int64(hours)}, want},
		{Duration{Minutes: int64(hours) * 60}, want},
		{Duration{Seconds: float64(hours) * 3600}, want},
		{Duration{Hours: int64(hours) + 25, Minutes: 61, Seconds: 3661.5}, want.Add(25*time.Hour + 61*time.Minute + 3661500*time.Millisecond)},
		{Duration{Negative: true, Hours: int64(hours)}, start.AddDate(0, 0, -hours/24)},
		{Duration{Days: 1, Hours: int64(hours)}, want.AddDate(0, 0, 1)},
	} {
		if got := tc.d.AddTo(start); !got.Equal(tc.want) {
			t.Errorf("%v after %v = %v, want %v", tc.d, start, got, tc.want)
		}
	}

	// Far past P9999Y, it is about two million years; never before start.
	for _, d := range []Duration{
		{Years: math.MaxInt64}, {Months: math.MaxInt64}, {Weeks: math.MaxInt64}, {Days: math.MaxInt64},
		{Weeks: math.MaxInt64, Days: math.MaxInt64}, {Hours: math.MaxInt64}, {Minutes: math.MaxInt64},
		{Seconds: math.MaxFloat64}, {Seconds: math.Inf(1)},
		// An int of 32 bits would have taken 2^32+1 years for one.
		{Years: 1<<32 + 1},
	} {
		got := d.AddTo(start)
		if got.Year() < 1_000_000 {
			t.Errorf("%+v after %v = %v, want over a million years on", d, start, got)
		}
		d.Negative = true
		if got := d.AddTo(start); got.Year() > -1_000_000 {
			t.Errorf("-%+v before %v = %v, want over a million years back", d, start, got)
		}
	}

	// The calendar of t's location is kept for days, elapsed time for the clock.
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	beforeDST := time.Date(2030, 3, 30, 12, 0, 0, 0, ams)
	if got := (Duration{Hours: int64(hours)}).AddTo(beforeDST); !got.Equal(beforeDST.UTC().AddDate(0, 0, hours/24)) {
		t.Errorf("PT%dH after %v = %v: not elapsed time", hours, beforeDST, got)
	}
	if got := (Duration{Days: 1}).AddTo(beforeDST); got.Sub(beforeDST) != 23*time.Hour {
		t.Errorf("P1D across the change to summer time = %v", got.Sub(beforeDST))
	}
}

func TestSaturatingArithmetic(t *testing.T) {
	for _, tc := range []struct{ a, b, sum, product int64 }{
		{1, 2, 3, 2},
		{math.MaxInt64, 1, math.MaxInt64, math.MaxInt64},
		{math.MinInt64, -1, math.MinInt64, math.MaxInt64},
		{math.MaxInt64, -1, math.MaxInt64 - 1, -math.MaxInt64},
		{math.MinInt64, 2, math.MinInt64 + 2, math.MinInt64},
		{-1, math.MinInt64, math.MinInt64, math.MaxInt64},
		{0, math.MinInt64, math.MinInt64, 0},
		{1 << 40, 1 << 40, 1 << 41, math.MaxInt64},
		{-(1 << 40), 1 << 40, 0, math.MinInt64},
	} {
		if got := addSat(tc.a, tc.b); got != tc.sum {
			t.Errorf("addSat(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.sum)
		}
		if got := mulSat(tc.a, tc.b); got != tc.product {
			t.Errorf("mulSat(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.product)
		}
	}
}

// FuzzDurationAddTo applies untrusted durations, as they come from a VTN:
// a duration that is not negative never moves a time back, a negative one
// never on, and one that fits in a time.Duration moves it by just that.
func FuzzDurationAddTo(f *testing.F) {
	for _, s := range []string{"PT1H", "P1Y2M3DT4H5M6.7S", "-P2W", "P9999Y", "PT3000000H", "P9223372036854775807Y",
		"P1317624576693539401W", "PT99999999999999999999.5S", "-PT9223372036854775807M"} {
		f.Add(s, int64(1893456000))
	}
	f.Fuzz(func(t *testing.T, s string, unix int64) {
		d, err := ParseDuration(s)
		if err != nil {
			return
		}
		start := time.Unix(unix%(1<<37), 0).UTC() // within a few thousand years
		got := d.AddTo(start)
		if d.Negative && got.After(start) || !d.Negative && got.Before(start) {
			t.Fatalf("%s from %v = %v: the wrong way", s, start, got)
		}
		if td, ok := d.TimeDuration(); ok && !got.Equal(start.Add(td)) {
			t.Fatalf("%s from %v = %v, want %v", s, start, got, start.Add(td))
		}
	})
}
