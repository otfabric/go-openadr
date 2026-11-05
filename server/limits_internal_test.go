// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"errors"
	"testing"
)

// A quota whose count fails refuses with that error; a quota without a
// limit, or for an object no client keeps, does not count at all.
func TestQuotaAdmit(t *testing.T) {
	broken := errors.New("store down")
	counted := 0
	q := quota{max: 1, noun: "things", count: func(context.Context, string) (int, error) { counted++; return 0, broken }}
	if err := q.admit(context.Background(), "c"); !errors.Is(err, broken) {
		t.Errorf("a failing count: %v", err)
	}
	if err := q.admit(context.Background(), ""); err != nil || counted != 1 {
		t.Errorf("an object of no client: %v, %d counts", err, counted)
	}
	q.max = 0
	if err := q.admit(context.Background(), "c"); err != nil || counted != 1 {
		t.Errorf("no limit: %v, %d counts", err, counted)
	}
	if l := newLimits(Limits{Subscriptions: -1, CallbackURLs: 3, Reports: 7}); l != (limits{callbackURLs: 3, reports: 7}) {
		t.Errorf("newLimits = %+v", l)
	}
}
