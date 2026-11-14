// SPDX-License-Identifier: MIT

package openadr

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// TimeDuration gives a defined result or false: never the result of a
// float-to-int conversion out of range, which differs between platforms.
func TestTimeDurationRange(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	// 2^63 nanoseconds, the first that does not fit, in seconds.
	const over = 9223372036.854775808
	for _, tc := range []struct {
		name string
		d    Duration
		want time.Duration
		ok   bool
	}{
		{"zero", Duration{}, 0, true},
		{"one second", Duration{Seconds: 1}, time.Second, true},
		{"minus one second", Duration{Negative: true, Seconds: 1}, -time.Second, true},
		{"NaN seconds", Duration{Seconds: nan}, 0, false},
		{"NaN seconds and an hour", Duration{Hours: 1, Seconds: nan}, 0, false},
		{"infinite seconds", Duration{Seconds: inf}, 0, false},
		{"minus infinite seconds", Duration{Seconds: -inf}, 0, false},
		{"negative and minus infinite seconds", Duration{Negative: true, Seconds: -inf}, 0, false},
		{"largest hours", Duration{Hours: math.MaxInt64}, 0, false},
		{"smallest hours", Duration{Hours: math.MinInt64}, 0, false},
		{"negative and smallest hours", Duration{Negative: true, Hours: math.MinInt64}, 0, false},
		{"largest days and weeks", Duration{Days: math.MaxInt64, Weeks: math.MaxInt64}, 0, false},
		// A negative component counts down, as it always did.
		{"minus five hours", Duration{Hours: -5}, -5 * time.Hour, true},
		{"negative and minus five hours", Duration{Negative: true, Hours: -5}, 5 * time.Hour, true},
		// Around the limit of a time.Duration: the largest float64 below
		// 2^63 nanoseconds fits, 2^63 does not, and -2^63 does only as a
		// negative total.
		{"just under the largest", Duration{Seconds: 9223372036.854774784}, time.Duration(math.MaxInt64 - 1023), true},
		{"the largest and one", Duration{Seconds: over}, 0, false},
		{"negative, just under the largest", Duration{Negative: true, Seconds: 9223372036.854774784}, -time.Duration(math.MaxInt64 - 1023), true},
		{"negative, the largest and one", Duration{Negative: true, Seconds: over}, 0, false},
		{"minus the largest and one", Duration{Seconds: -over}, time.Duration(math.MinInt64), true},
		{"negative, minus the largest and one", Duration{Negative: true, Seconds: -over}, 0, false},
		{"just past minus the largest and one", Duration{Seconds: -over * (1 + 1e-15)}, 0, false},
		// The 32-bit boundary of an int is nothing special here.
		{"2^31 seconds", Duration{Seconds: 1 << 31}, (1 << 31) * time.Second, true},
		{"2^31 hours", Duration{Hours: 1 << 31}, 0, false},
	} {
		got, ok := tc.d.TimeDuration()
		if ok != tc.ok || ok && got != tc.want {
			t.Errorf("%s: TimeDuration() = %v, %v; want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// AddTo of a duration whose seconds are not a number adds the rest of it.
func TestAddToNaNSeconds(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	got := Duration{Days: 1, Hours: 1, Seconds: math.NaN()}.AddTo(start)
	if want := start.Add(25 * time.Hour); !got.Equal(want) {
		t.Errorf("AddTo = %v, want %v", got, want)
	}
}

// Validate refuses a duration that has no ISO 8601 form, wherever an object
// carries one, and accepts every one that ParseDuration gives.
func TestValidateDurations(t *testing.T) {
	bad := map[string]Duration{
		"NaN seconds":                    {Seconds: math.NaN()},
		"infinite seconds":               {Seconds: math.Inf(1)},
		"minus infinite seconds":         {Seconds: math.Inf(-1)},
		"negative seconds":               {Seconds: -1},
		"negative hours":                 {Hours: -5},
		"smallest hours":                 {Negative: true, Hours: math.MinInt64},
		"negative weeks":                 {Weeks: -1},
		"days and weeks past the limit":  {Days: 8, Weeks: math.MaxInt64/7 - 1},
		"days and weeks that wrap round": {Days: 1, Weeks: 2635249153387078803},
	}
	period := func(d Duration) *IntervalPeriod { return &IntervalPeriod{Duration: &d} }
	randomize := func(d Duration) *IntervalPeriod { return &IntervalPeriod{RandomizeStart: &d} }
	payload := []ValuesMap{{Type: "PRICE", Values: []Value{Number(1)}}}
	for name, d := range bad {
		for field, v := range map[string]interface{ Validate() error }{
			"intervalPeriod.duration":                           Program{Name: "p", IntervalPeriod: period(d)},
			"duration":                                          Event{ProgramID: "p1", Duration: &d},
			"intervalPeriod.randomizeStart":                     Event{ProgramID: "p1", IntervalPeriod: randomize(d)},
			"intervals[0].intervalPeriod.duration":              Event{ProgramID: "p1", Intervals: []Interval{{IntervalPeriod: period(d), Payloads: payload}}},
			"resources[0].intervalPeriod.duration":              Report{EventID: "e1", ClientName: "c", Resources: []ReportResource{{ResourceName: "r", IntervalPeriod: period(d)}}},
			"resources[0].intervals[0].intervalPeriod.duration": Report{EventID: "e1", ClientName: "c", Resources: []ReportResource{{ResourceName: "r", Intervals: []Interval{{IntervalPeriod: period(d), Payloads: payload}}}}},
		} {
			var verr *ValidationError
			if err := v.Validate(); !errors.As(err, &verr) || verr.Field != field {
				t.Errorf("%s in %s: Validate() = %v", name, field, err)
			}
		}
	}
	for _, s := range []string{"PT0S", "PT1H", "P1W", "-P1DT0.5S", "P9999Y", "PT9223372036854775807H", "PT0.000000001S"} {
		d, err := ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		e := Event{ProgramID: "p1", Duration: &d, IntervalPeriod: &IntervalPeriod{Duration: &d, RandomizeStart: &d}}
		if err := e.Validate(); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	if err := (Event{ProgramID: "p1", Duration: &Duration{Days: 1, Weeks: 1}}).Validate(); err != nil {
		t.Errorf("days and weeks: %v", err)
	}
	if !strings.Contains(Duration{Seconds: math.NaN()}.problem(), "finite") {
		t.Error("the reason does not say what is wrong")
	}
}
