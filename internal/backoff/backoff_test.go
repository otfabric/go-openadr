// SPDX-License-Identifier: MIT

package backoff

import (
	"math"
	"testing"
	"time"
)

func TestDouble(t *testing.T) {
	for _, tc := range []struct{ wait, limit, want time.Duration }{
		{time.Second, time.Minute, 2 * time.Second},
		{30 * time.Second, time.Minute, time.Minute},
		{31 * time.Second, time.Minute, time.Minute},
		{time.Hour, time.Minute, time.Minute},
		{math.MaxInt64, time.Minute, time.Minute},
		{math.MaxInt64/2 + 1, math.MaxInt64, math.MaxInt64},
		{math.MaxInt64 / 2, math.MaxInt64, math.MaxInt64 - 1},
	} {
		if got := Double(tc.wait, tc.limit); got != tc.want || got < 0 {
			t.Errorf("Double(%v, %v) = %v, want %v", tc.wait, tc.limit, got, tc.want)
		}
	}
}
