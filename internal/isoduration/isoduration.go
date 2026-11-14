// SPDX-License-Identifier: MIT

// Package isoduration says whether the components of a duration can be
// written in the ISO 8601 form of OpenADR so that they read back as the
// same duration. It is shared by Validate in the root package, the wire
// encoders and the CSV writer of package timeline, which must not write a
// duration that ParseDuration refuses.
package isoduration

import "math"

// Problem returns why a duration with these components cannot be written,
// or "" when it can. The form has no sign per component (a negative
// duration is a minus before the whole), no seconds that are not a number
// or infinite, and writes days and weeks together as one number of days.
func Problem(years, months, weeks, days, hours, minutes int64, seconds float64) string {
	switch {
	case math.IsNaN(seconds) || math.IsInf(seconds, 0):
		return "has seconds that are not a finite number"
	case years < 0 || months < 0 || weeks < 0 || days < 0 || hours < 0 || minutes < 0 || seconds < 0:
		return "has a negative component; a negative duration sets Negative instead"
	case days != 0 && weeks > (math.MaxInt64-days)/7:
		return "has more days and weeks together than one number of days holds"
	}
	return ""
}
