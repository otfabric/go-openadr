// SPDX-License-Identifier: MIT

package webhook

import (
	"errors"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The notifications that may wait in all leave room for a second host,
// and do not overflow.
func TestCapacity(t *testing.T) {
	for _, tc := range []struct{ queue, workers, want int }{
		{256, 4, 1024}, {1, 1, 2}, {5, 0, 10}, {math.MaxInt, 4, math.MaxInt}, {math.MaxInt / 2, 3, math.MaxInt},
	} {
		if got := capacity(tc.queue, tc.workers); got != tc.want {
			t.Errorf("capacity(%d, %d) = %d, want %d", tc.queue, tc.workers, got, tc.want)
		}
	}
}

// Retry-After is delay-seconds, digits only, or an HTTP-date (RFC 9110,
// section 10.2.3).
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"":                                 0,
		"0":                                0,
		"1":                                time.Second,
		" 120 ":                            2 * time.Minute,
		"1m":                               0,
		"1.5":                              0,
		"-1":                               0,
		"+1":                               0,
		"1e3":                              0,
		"0x10":                             0,
		"1_0":                              0,
		"99999999999999999999":             math.MaxInt64,
		"9223372036854775807":              math.MaxInt64,
		"Thu, 01 Jan 2026 00:01:00 GMT":    time.Minute,
		"Wed, 31 Dec 2025 23:59:00 GMT":    0,
		"Thursday, 01-Jan-26 00:00:30 GMT": 30 * time.Second,
		"tomorrow":                         0,
	} {
		if got := retryAfter(value, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", value, got, want)
		}
	}
}

// A callback URL is logged and reported without its query, fragment and
// credentials, whether it parses or not.
func TestRedacted(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/hook?token=q1#f":   "https://example.com/hook",
		"https://user:pw@example.com/hook?":     "https://example.com/hook",
		"https://example.com/hook":              "https://example.com/hook",
		"https://user:pw@exa mple.com/%zz?q=q2": "https://exa mple.com/%zz",
		"https://exa mple.com/%zz#q3":           "https://exa mple.com/%zz",
	} {
		if got := redacted(in); got != want {
			t.Errorf("redacted(%q) = %q, want %q", in, got, want)
		}
	}
	// An error whose text holds the query reads without it, and still
	// unwraps.
	inner := errors.New("refused at https://example.com/hook?token=q4")
	err := redactError(inner, "https://example.com/hook?token=q4")
	if strings.Contains(err.Error(), "q4") || !errors.Is(err, inner) {
		t.Errorf("redactError: %v", err)
	}
	err = redactError(&url.Error{Op: "Post", URL: "https://example.com/hook?token=q5", Err: inner}, "%zz?token=q5")
	if strings.Contains(err.Error(), "q5") || !errors.Is(err, inner) {
		t.Errorf("redactError of a *url.Error: %v", err)
	}
	if err := redactError(inner, "https://example.com/hook"); err != inner {
		t.Errorf("redactError without a query: %v", err)
	}
}
