// SPDX-License-Identifier: MIT

package mqtt

import (
	"math"
	"testing"
	"time"
)

// The waits between attempts: a default, doubling up to a minute without
// overflow, and starting again after a session that lasted.
func TestWaits(t *testing.T) {
	if got := (Broker{}).firstWait(); got != time.Second {
		t.Errorf("first wait without Backoff = %v", got)
	}
	if got := (Broker{Backoff: 3 * time.Second}).firstWait(); got != 3*time.Second {
		t.Errorf("first wait with Backoff 3s = %v", got)
	}
	for _, tc := range [][2]time.Duration{
		{time.Second, 2 * time.Second},
		{30 * time.Second, time.Minute},
		{31 * time.Second, time.Minute},
		{math.MaxInt64, time.Minute},
	} {
		if got := nextWait(tc[0]); got != tc[1] {
			t.Errorf("nextWait(%v) = %v, want %v", tc[0], got, tc[1])
		}
	}
	b := Broker{Backoff: 2 * time.Second}
	if got := b.afterSession(time.Second, 40*time.Second); got != 40*time.Second {
		t.Errorf("after a short session: %v", got)
	}
	if got := b.afterSession(2*time.Minute, 40*time.Second); got != 2*time.Second {
		t.Errorf("after a session of two minutes: %v", got)
	}
	stop := make(chan struct{})
	if !pause(stop, time.Millisecond) {
		t.Error("pause reported stop without it")
	}
	close(stop)
	if pause(stop, time.Hour) {
		t.Error("pause did not end at stop")
	}
}
