// SPDX-License-Identifier: MIT

package webhook

import (
	"math"
	"testing"
)

// The queue of a hash is in range for any number of queues, and the same
// on a platform with 32-bit int.
func TestQueueOf(t *testing.T) {
	for _, tc := range []struct {
		hash uint32
		n    int
		want int
	}{
		{0, 1, 0}, {math.MaxUint32, 1, 0}, {7, 0, 0},
		{math.MaxUint32, 4, 3}, {1<<31 + 5, 4, 1},
		{math.MaxUint32, math.MaxInt32, 1},
	} {
		if got := queueOf(tc.hash, tc.n); got != tc.want || got < 0 || (tc.n > 0 && got >= tc.n) {
			t.Errorf("queueOf(%d, %d) = %d, want %d", tc.hash, tc.n, got, tc.want)
		}
	}
}
