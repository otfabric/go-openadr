// SPDX-License-Identifier: MIT

package goid

import "testing"

func TestID(t *testing.T) {
	id := ID()
	if id == 0 {
		t.Fatal("ID() = 0 on the test goroutine")
	}
	if again := ID(); again != id {
		t.Errorf("ID() = %d, then %d: not stable within a goroutine", id, again)
	}
	other := make(chan uint64)
	go func() { other <- ID() }()
	if o := <-other; o == 0 || o == id {
		t.Errorf("ID() of another goroutine = %d, want non-zero and not %d", o, id)
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		stack string
		want  uint64
	}{
		{"goroutine 12 [running]:\n", 12},
		{"goroutine 1 [running]:\n", 1},
		{"goroutine 4294967295 [running]:\n", 4294967295},
		{"goroutine 4294967296 [running]:\n", 4294967296},
		{"goroutine 18446744073709551615 [select]:\n", 18446744073709551615},
		{"goroutine 18446744073709551616 [select]:\n", 0}, // does not fit
		{"goroutine 0 [running]:\n", 0},
		{"thread 12 [running]", 0},
		{"goroutine 12", 0},
		{"goroutine x [running]", 0},
		{"goroutine -3 [running]", 0},
		{"", 0},
	} {
		if got := parse([]byte(tc.stack)); got != tc.want {
			t.Errorf("parse(%q) = %d, want %d", tc.stack, got, tc.want)
		}
	}
}

func TestSet(t *testing.T) {
	var s Set
	if s.Has() {
		t.Fatal("the zero Set has the calling goroutine")
	}
	leave := s.Enter()
	if !s.Has() {
		t.Fatal("not in the Set after Enter")
	}
	other := make(chan bool)
	go func() { other <- s.Has() }()
	if <-other {
		t.Error("another goroutine is in the Set")
	}
	again := s.Enter()
	leave()
	if !s.Has() {
		t.Error("not in the Set after one of two Enters left")
	}
	// Left on another goroutine: the one that entered is out.
	done := make(chan struct{})
	go func() { again(); close(done) }()
	<-done
	if s.Has() {
		t.Error("in the Set after both Enters left")
	}
	if len(s.ids) != 0 {
		t.Errorf("the Set keeps %d goroutines after all left", len(s.ids))
	}
}
