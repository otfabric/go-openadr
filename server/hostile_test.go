// SPDX-License-Identifier: MIT

package server_test

import (
	"math"
	"testing"

	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// A rate so low that the time to refill a bucket is more than a Duration
// holds: the wait is the longest there is, not a conversion that went
// negative, and the bucket of a client that used it up is not forgotten,
// which would hand it a new burst with every request.
func TestRateLimiterBeyondADuration(t *testing.T) {
	l := server.NewRateLimiter(1e-12, 1)
	if ok, _ := l.Allow("a", "GET /programs"); !ok {
		t.Fatal("the first request was refused")
	}
	for i := range 3 {
		ok, wait := l.Allow("a", "GET /programs")
		if ok {
			t.Fatalf("request %d after the burst was taken", i+2)
		}
		if wait != math.MaxInt64 {
			t.Errorf("wait %v, want the longest Duration", wait)
		}
	}
}

// The longest wait a Limiter can name is sent as whole seconds, also where
// an int has 32 bits.
func TestRetryAfterOfTheLongestWait(t *testing.T) {
	l := &refuse{op: "GET /programs", wait: math.MaxInt64}
	vtn, err := server.New(server.Config{Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token", Limiter: l})
	if err != nil {
		t.Fatal(err)
	}
	r := call(vtn, "GET", "/programs", "bl", "")
	if want := "9223372037"; r.status != 429 || r.header.Get("Retry-After") != want {
		t.Errorf("%d, Retry-After %q, want %q", r.status, r.header.Get("Retry-After"), want)
	}
}
