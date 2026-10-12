// SPDX-License-Identifier: MIT

package openadr

import (
	"regexp"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	valid := []struct {
		in   string
		want Duration
		out  string // canonical form when it differs from in
	}{
		{"PT1H", Duration{Hours: 1}, ""},
		{"PT0S", Duration{}, ""},
		{"P1D", Duration{Days: 1}, ""},
		{"P2W", Duration{Weeks: 2}, ""},
		{"P1Y2M3DT4H5M6S", Duration{Years: 1, Months: 2, Days: 3, Hours: 4, Minutes: 5, Seconds: 6}, ""},
		{"-PT15M", Duration{Negative: true, Minutes: 15}, ""},
		{"PT0.5S", Duration{Seconds: 0.5}, ""},
		{"P9999Y", Infinite, ""},
		{"P0D", Duration{}, "PT0S"},
		{"P1DT", Duration{Days: 1}, "P1D"},
	}
	for _, tc := range valid {
		got, err := ParseDuration(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseDuration(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			continue
		}
		want := tc.out
		if want == "" {
			want = tc.in
		}
		if got.String() != want {
			t.Errorf("ParseDuration(%q).String() = %q, want %q", tc.in, got.String(), want)
		}
	}
	for _, in := range []string{"", "P", "PT", "1H", "PT1", "P1H", "PT1H1D", "P1W2D", "P-1D", "PT1.S", "pt1h", " PT1H", "P1D1W", "P99999999999999999999D"} {
		if d, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = %+v, want an error", in, d)
		}
	}
}

func TestDurationArithmetic(t *testing.T) {
	if d, ok := (Duration{Days: 1, Hours: 12}).TimeDuration(); !ok || d != 36*time.Hour {
		t.Errorf("P1DT12H = %v, %v", d, ok)
	}
	if d, ok := (Duration{Negative: true, Seconds: 1.5}).TimeDuration(); !ok || d != -1500*time.Millisecond {
		t.Errorf("-PT1.5S = %v, %v", d, ok)
	}
	if _, ok := (Duration{Months: 1}).TimeDuration(); ok {
		t.Error("P1M converted to a fixed duration")
	}
	if _, ok := Infinite.TimeDuration(); ok {
		t.Error("P9999Y converted to a fixed duration")
	}
	jan31 := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	if got := (Duration{Months: 1, Hours: 2}).AddTo(jan31); !got.Equal(time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("P1MT2H after Jan 31 = %v", got)
	}
	if got := (Duration{Negative: true, Weeks: 1}).AddTo(jan31); !got.Equal(time.Date(2026, 1, 24, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("-P1W before Jan 31 = %v", got)
	}
	for _, td := range []time.Duration{0, time.Second, 90 * time.Minute, -36 * time.Hour, 1500 * time.Millisecond, 1<<63 - 1, -1 << 63} {
		back, ok := DurationOf(td).TimeDuration()
		// The extremes do not survive the float64 seconds.
		if td > -1<<62 && td < 1<<62 && (!ok || back != td) {
			t.Errorf("DurationOf(%v) = %v, back to %v, %v", td, DurationOf(td), back, ok)
		}
	}
}

// The specification's pattern, with its lookahead replaced by the two
// alternatives it allows.
var specDuration = regexp.MustCompile(
	`^(-?)P(\d|T\d)`)
var specDurationBody = regexp.MustCompile(
	`^(-?)P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)([DW]))?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

func FuzzParseDuration(f *testing.F) {
	for _, s := range []string{"PT1H", "P1Y2M3DT4H5M6.7S", "-P2W", "P", "PT", "P1DT", "P9999Y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDuration(s)
		matches := specDuration.MatchString(s) && specDurationBody.MatchString(s)
		if err != nil {
			// Numbers too large for the fields are the only strings the
			// pattern accepts and ParseDuration does not.
			if matches && len(s) < 18 {
				t.Fatalf("ParseDuration(%q) failed on a string the pattern accepts: %v", s, err)
			}
			return
		}
		if !matches {
			t.Fatalf("ParseDuration(%q) accepted a string the pattern rejects", s)
		}
		again, err := ParseDuration(d.String())
		if err != nil {
			t.Fatalf("ParseDuration(%q).String() = %q does not parse: %v", s, d.String(), err)
		}
		if again.String() != d.String() {
			t.Fatalf("%q: String is not stable: %q then %q", s, d.String(), again.String())
		}
	})
}
