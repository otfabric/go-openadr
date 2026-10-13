// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// RetryPolicy decides whether a failed call is tried again, and after how
// long. It is consulted only for failures that can be retried at all, which
// the client decides and a policy cannot widen:
//
//   - the failure is one that another attempt can cure: no answer (a
//     connection that could not be made or was lost, a timeout), or an
//     answer that says to come back: 429, 502, 503 or 504;
//   - repeating the request cannot do twice what was asked once. A read, an
//     update and a delete can always be repeated. A create (POST) is
//     repeated only when it certainly did not reach the VTN (the connection
//     could not be made, or the answer was 429 or 503), unless
//     Config.RetryCreates says otherwise.
//
// A cancelled context, a 4xx other than 429, a 500 and an invalid object
// are never retried. Neither is the one retry with a fresh token after a
// 401 a matter for the policy: that always happens, once.
//
// Implementations must be safe for concurrent use.
type RetryPolicy interface {
	// ShouldRetry is called after a failed attempt that can be retried.
	// attempt counts the attempts made so far, from 1. It returns whether
	// to try again and how long to wait first. When the VTN named a wait
	// in a Retry-After header and it is longer, the client waits that long.
	ShouldRetry(attempt int, err error) (retry bool, wait time.Duration)
}

type noRetry struct{}

func (noRetry) ShouldRetry(int, error) (bool, time.Duration) { return false, 0 }

// NoRetry returns the policy that never retries, which is the default.
func NoRetry() RetryPolicy { return noRetry{} }

// ExponentialBackoffConfig configures NewExponentialBackoff.
type ExponentialBackoffConfig struct {
	// BaseDelay is the wait before the second attempt, doubled before
	// each one after; 200 ms when zero.
	BaseDelay time.Duration
	// MaxDelay is the longest wait; 30 seconds when zero.
	MaxDelay time.Duration
	// MaxAttempts is how many attempts a call gets, the first included; 4
	// when zero. A negative number is no limit: then give every call a
	// context with a deadline.
	MaxAttempts int
	// RetryOnTimeout also retries a call that timed out. Off by default: a
	// call that used up its time once will often do so again, and a
	// caller that set a short timeout meant it.
	RetryOnTimeout bool
	// NoJitter turns off the randomizing of the wait. By default each
	// wait is somewhere between half of and the whole computed delay, so
	// that clients that failed together do not come back together.
	NoJitter bool
}

type exponentialBackoff struct{ cfg ExponentialBackoffConfig }

// ExponentialBackoff returns a policy that waits base before the second
// attempt and twice as long before each one after, up to maxDelay, and
// gives up after maxAttempts attempts. It is NewExponentialBackoff with
// the rest left at its defaults.
func ExponentialBackoff(base, maxDelay time.Duration, maxAttempts int) RetryPolicy {
	return NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: base, MaxDelay: maxDelay, MaxAttempts: maxAttempts})
}

// NewExponentialBackoff returns a policy of exponential backoff.
func NewExponentialBackoff(cfg ExponentialBackoffConfig) RetryPolicy {
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 200 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 30 * time.Second
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 4
	}
	return exponentialBackoff{cfg: cfg}
}

func (p exponentialBackoff) ShouldRetry(attempt int, err error) (bool, time.Duration) {
	if p.cfg.MaxAttempts > 0 && attempt >= p.cfg.MaxAttempts {
		return false, 0
	}
	if !p.cfg.RetryOnTimeout && errors.Is(err, openadr.ErrTimeout) {
		return false, 0
	}
	delay := p.cfg.BaseDelay
	for i := 1; i < attempt && delay < p.cfg.MaxDelay; i++ {
		delay *= 2
	}
	delay = min(delay, p.cfg.MaxDelay)
	if !p.cfg.NoJitter {
		delay = delay/2 + rand.N(delay/2+1)
	}
	return true, delay
}

// retryable reports whether a failed request may be sent again: see
// RetryPolicy.
func retryable(method string, err error, retryCreates bool) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *openadr.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			// Refused before anything was done.
			return true
		case http.StatusBadGateway, http.StatusGatewayTimeout:
			// A gateway that got no answer: the VTN may have acted.
			return method != http.MethodPost || retryCreates
		}
		return false
	}
	var tokenErr *TokenError
	if errors.As(err, &tokenErr) {
		// No request went to the VTN; the token endpoint may recover.
		return tokenErr.StatusCode == http.StatusTooManyRequests || tokenErr.StatusCode >= 502 && tokenErr.StatusCode <= 504
	}
	if errors.Is(err, openadr.ErrInvalid) || errors.Is(err, openadr.ErrProtocol) || errors.Is(err, openadr.ErrConfiguration) {
		return false
	}
	if errors.Is(err, errNotSent) {
		return true
	}
	// No answer. Whether the VTN got the request is not known, unless the
	// connection was never made.
	if method != http.MethodPost || retryCreates {
		return true
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// errNotSent marks a failure before the request to the VTN was made, such
// as a token that could not be had.
var errNotSent = errors.New("openadr: the request was not sent")

// retryAfter returns the wait a VTN asked for, if it did.
func retryAfter(err error) time.Duration {
	var apiErr *openadr.Error
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

// parseRetryAfter reads a Retry-After header: seconds, or a date.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(v + "s"); err == nil && seconds >= 0 {
		return seconds
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
