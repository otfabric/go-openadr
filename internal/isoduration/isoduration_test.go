// SPDX-License-Identifier: MIT

package isoduration

import (
	"math"
	"testing"
)

func TestProblem(t *testing.T) {
	const max = math.MaxInt64
	for _, tc := range []struct {
		name                                    string
		years, months, weeks, days, hours, mins int64
		seconds                                 float64
		ok                                      bool
	}{
		{name: "zero", ok: true},
		{name: "one of each", years: 1, months: 1, days: 1, hours: 1, mins: 1, seconds: 1, ok: true},
		{name: "weeks only", weeks: max, ok: true},
		{name: "largest components", years: max, months: max, days: max, hours: max, mins: max, seconds: math.MaxFloat64, ok: true},
		{name: "smallest seconds", seconds: math.SmallestNonzeroFloat64, ok: true},
		{name: "negative zero seconds", seconds: math.Copysign(0, -1), ok: true},
		{name: "NaN seconds", seconds: math.NaN()},
		{name: "infinite seconds", seconds: math.Inf(1)},
		{name: "minus infinite seconds", seconds: math.Inf(-1)},
		{name: "negative seconds", seconds: -1},
		{name: "negative years", years: -1},
		{name: "negative months", months: -1},
		{name: "negative weeks", weeks: -1},
		{name: "negative days", days: -1},
		{name: "negative hours", hours: math.MinInt64},
		{name: "negative minutes", mins: -1},
		{name: "days and weeks", days: 1, weeks: 1, ok: true},
		// MaxInt64 is a multiple of 7.
		{name: "days and weeks at the limit", days: 7, weeks: max/7 - 1, ok: true},
		{name: "days and weeks one past", days: 8, weeks: max/7 - 1},
		{name: "days and the largest weeks", days: 1, weeks: max},
		// 7 times it is 2^64 + 5: as an int64, 5 days.
		{name: "weeks whose days wrap to a small number", days: 1, weeks: 2635249153387078803},
	} {
		got := Problem(tc.years, tc.months, tc.weeks, tc.days, tc.hours, tc.mins, tc.seconds)
		if (got == "") != tc.ok {
			t.Errorf("%s: Problem = %q, want ok %v", tc.name, got, tc.ok)
		}
	}
}
