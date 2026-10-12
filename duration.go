// SPDX-License-Identifier: MIT

package openadr

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Duration is an ISO 8601 duration as OpenADR uses it, for example PT1H or
// P1DT12H. Years and months are kept as written because their length depends
// on the date they are applied to; use AddTo for calendar arithmetic and
// TimeDuration when the duration has no such components.
//
// The format allows either days or weeks, not both; String writes weeks only
// when Days is zero.
type Duration struct {
	Negative bool
	Years    int64
	Months   int64
	Weeks    int64
	Days     int64
	Hours    int64
	Minutes  int64
	Seconds  float64
}

// Infinite is P9999Y, which the specification uses to mean "indefinitely".
var Infinite = Duration{Years: 9999}

// The pattern of the specification, without its lookahead (which Go's regexp
// does not support); ParseDuration applies that part separately.
var durationRE = regexp.MustCompile(
	`^(-?)P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)([DW]))?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// ParseDuration parses an ISO 8601 duration in the form the OpenADR 3
// schema accepts.
func ParseDuration(s string) (Duration, error) {
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return Duration{}, fmt.Errorf("openadr: invalid duration %q", s)
	}
	// The lookahead: "P" is followed by a digit, or by "T" and a digit.
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "P")
	rest = strings.TrimPrefix(rest, "T")
	if rest == "" || rest[0] < '0' || rest[0] > '9' {
		return Duration{}, fmt.Errorf("openadr: invalid duration %q", s)
	}

	d := Duration{Negative: m[1] == "-"}
	dayOrWeek := &d.Days
	if m[5] == "W" {
		dayOrWeek = &d.Weeks
	}
	for i, dst := range map[int]*int64{2: &d.Years, 3: &d.Months, 4: dayOrWeek, 6: &d.Hours, 7: &d.Minutes} {
		if m[i] == "" {
			continue
		}
		n, err := strconv.ParseInt(m[i], 10, 64)
		if err != nil {
			return Duration{}, fmt.Errorf("openadr: invalid duration %q: %w", s, err)
		}
		*dst = n
	}
	if m[8] != "" {
		sec, err := strconv.ParseFloat(m[8], 64)
		if err != nil || math.IsInf(sec, 0) {
			return Duration{}, fmt.Errorf("openadr: invalid duration %q: seconds out of range", s)
		}
		d.Seconds = sec
	}
	return d, nil
}

// DurationOf converts a time.Duration, using hours, minutes and seconds only.
func DurationOf(td time.Duration) Duration {
	d := Duration{}
	if td < 0 {
		d.Negative = true
		// Negating math.MinInt64 overflows; work on the unsigned magnitude.
		u := uint64(-(td + 1)) + 1
		d.Hours = int64(u / uint64(time.Hour))
		u %= uint64(time.Hour)
		d.Minutes = int64(u / uint64(time.Minute))
		u %= uint64(time.Minute)
		d.Seconds = float64(u) / float64(time.Second)
		return d
	}
	d.Hours = int64(td / time.Hour)
	td %= time.Hour
	d.Minutes = int64(td / time.Minute)
	td %= time.Minute
	d.Seconds = td.Seconds()
	return d
}

// IsZero reports whether the duration has no length.
func (d Duration) IsZero() bool {
	return d.Years == 0 && d.Months == 0 && d.Weeks == 0 && d.Days == 0 &&
		d.Hours == 0 && d.Minutes == 0 && d.Seconds == 0
}

// IsInfinite reports whether the duration is P9999Y.
func (d Duration) IsInfinite() bool {
	return d == Infinite
}

// String returns the ISO 8601 form. The zero Duration is "PT0S".
func (d Duration) String() string {
	if d.IsZero() {
		return "PT0S"
	}
	var b strings.Builder
	if d.Negative {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	part := func(n int64, unit byte) {
		if n != 0 {
			b.WriteString(strconv.FormatInt(n, 10))
			b.WriteByte(unit)
		}
	}
	part(d.Years, 'Y')
	part(d.Months, 'M')
	if d.Days != 0 {
		// The format has no way to write both; a week is seven days.
		part(d.Days+7*d.Weeks, 'D')
	} else {
		part(d.Weeks, 'W')
	}
	if d.Hours != 0 || d.Minutes != 0 || d.Seconds != 0 {
		b.WriteByte('T')
		part(d.Hours, 'H')
		part(d.Minutes, 'M')
		if d.Seconds != 0 {
			b.WriteString(strconv.FormatFloat(d.Seconds, 'f', -1, 64))
			b.WriteByte('S')
		}
	}
	return b.String()
}

// TimeDuration converts to a time.Duration, taking a day as 24 hours. It
// reports false when the duration has years or months, whose length is not
// fixed, or does not fit in a time.Duration.
func (d Duration) TimeDuration() (time.Duration, bool) {
	if d.Years != 0 || d.Months != 0 {
		return 0, false
	}
	total := (float64(d.Weeks)*7+float64(d.Days))*float64(24*time.Hour) +
		float64(d.Hours)*float64(time.Hour) +
		float64(d.Minutes)*float64(time.Minute) +
		d.Seconds*float64(time.Second)
	if total >= float64(math.MaxInt64) {
		return 0, false
	}
	if d.Negative {
		total = -total
	}
	return time.Duration(math.Round(total)), true
}

// AddTo returns t moved by the duration, applying years, months and days on
// the calendar in t's location.
func (d Duration) AddTo(t time.Time) time.Time {
	sign := 1
	if d.Negative {
		sign = -1
	}
	t = t.AddDate(sign*int(d.Years), sign*int(d.Months), sign*int(d.Weeks*7+d.Days))
	clock, _ := Duration{Hours: d.Hours, Minutes: d.Minutes, Seconds: d.Seconds, Negative: d.Negative}.TimeDuration()
	return t.Add(clock)
}
