// SPDX-License-Identifier: MIT

package server_test

import (
	"testing"
	"time"

	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

func TestRateLimiter(t *testing.T) {
	l := server.NewRateLimiter(20, 3)
	// A burst, and then no more.
	for i := range 3 {
		if ok, _ := l.Allow("a", "GET /programs"); !ok {
			t.Fatalf("request %d of a burst of 3 was refused", i+1)
		}
	}
	ok, wait := l.Allow("a", "GET /programs")
	if ok || wait <= 0 || wait > 60*time.Millisecond {
		t.Errorf("a fourth request: taken %v, wait %v; want a wait of at most 50 ms", ok, wait)
	}
	// Each client has a budget of its own, the one without a name too.
	for _, id := range []string{"b", ""} {
		if ok, _ := l.Allow(id, "GET /programs"); !ok {
			t.Errorf("client %q was refused for what another did", id)
		}
	}
	// It refills with time.
	time.Sleep(wait + 10*time.Millisecond)
	if ok, _ := l.Allow("a", "GET /programs"); !ok {
		t.Error("refused after the wait it named")
	}
	// A burst below 1 is 1, and a rate of nothing takes nothing.
	one := server.NewRateLimiter(1000, 0)
	if ok, _ := one.Allow("a", ""); !ok {
		t.Error("a burst of 0 took nothing")
	}
	if ok, wait := server.NewRateLimiter(0, 5).Allow("a", ""); ok || wait != 0 {
		t.Errorf("a rate of 0: %v, %v", ok, wait)
	}
}

// refuse is a Limiter that refuses one operation.
type refuse struct {
	op    string
	wait  time.Duration
	asked []string
}

func (l *refuse) Allow(clientID, op string) (bool, time.Duration) {
	l.asked = append(l.asked, clientID+" "+op)
	return op != l.op, l.wait
}

func TestLimiterHook(t *testing.T) {
	l := &refuse{op: "POST /programs", wait: 1200 * time.Millisecond}
	vtn, err := server.New(server.Config{Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token", Limiter: l})
	if err != nil {
		t.Fatal(err)
	}
	// Refused: 429, a problem, and the wait in whole seconds, rounded up.
	r := call(vtn, "POST", "/programs", "bl", `{"programName":"x"}`)
	if r.status != 429 || r.header.Get("Retry-After") != "2" || r.json["status"] != float64(429) {
		t.Errorf("refused: %d, Retry-After %q, %s", r.status, r.header.Get("Retry-After"), r.body)
	}
	if list := call(vtn, "GET", "/programs", "bl", ""); list.status != 200 || list.body != "[]" {
		t.Errorf("the refused request was carried out, or another refused: %d %s", list.status, list.body)
	}
	// Not asked for what is refused before: no token, a bad one, no scope,
	// an unknown path.
	call(vtn, "GET", "/programs", "", "")
	call(vtn, "GET", "/programs", "nonsense", "")
	call(vtn, "POST", "/events", "ven", `{}`)
	call(vtn, "GET", "/nowhere", "bl", "")
	call(vtn, "GET", "/programs/abc", "ven", "")
	want := []string{"bl POST /programs", "bl GET /programs", "ven GET /programs/{id}"}
	if len(l.asked) != len(want) {
		t.Fatalf("the limiter was asked %v, want %v", l.asked, want)
	}
	for i := range want {
		if l.asked[i] != want[i] {
			t.Errorf("the limiter was asked %v, want %v", l.asked, want)
		}
	}
	// A wait of nothing sends no header.
	l.wait = 0
	if r := call(vtn, "POST", "/programs", "bl", `{"programName":"x"}`); r.status != 429 || r.header.Get("Retry-After") != "" {
		t.Errorf("refused without a wait: %d, Retry-After %q", r.status, r.header.Get("Retry-After"))
	}
}
