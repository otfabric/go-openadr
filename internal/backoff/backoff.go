// SPDX-License-Identifier: MIT

// Package backoff doubles the waits between attempts.
package backoff

import "time"

// Double is twice wait, up to limit. A wait of more than half of limit is
// limit, so that the doubling never goes past what a Duration holds.
func Double(wait, limit time.Duration) time.Duration {
	if wait > limit/2 {
		return limit
	}
	return wait * 2
}
