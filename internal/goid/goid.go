// SPDX-License-Identifier: MIT

// Package goid returns the identifier of the calling goroutine.
//
// The MQTT and WebSocket subscribers call Notify on a goroutine of their
// own and wait for that goroutine, or for Notify, in Close. A Close called
// from Notify would wait for itself; with the identifier Close can tell
// that it runs on the goroutine it would wait for. It is used once per
// Close and once per call of Notify, never per byte.
//
// The MQTT publisher and the webhook sender keep the goroutines their Close
// waits for in a Set: theirs, and one that runs a callback which a goroutine
// of theirs may wait for.
package goid

import (
	"bytes"
	"runtime"
	"strconv"
	"sync"
)

// ID returns the identifier of the calling goroutine, 0 if the runtime
// does not report it.
func ID() uint64 {
	var buf [64]byte
	return parse(buf[:runtime.Stack(buf[:], false)])
}

// parse reads the identifier from the first line of a goroutine's stack:
// "goroutine 12 [running]:".
func parse(stack []byte) uint64 {
	rest, ok := bytes.CutPrefix(stack, []byte("goroutine "))
	if !ok {
		return 0
	}
	n := bytes.IndexByte(rest, ' ')
	if n < 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(rest[:n]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// Set is a set of goroutines. The zero Set is empty and ready for use; a
// Set is safe for concurrent use and must not be copied after first use.
type Set struct {
	mu  sync.Mutex
	ids map[uint64]int // goroutine: how many times it entered and has not left
}

// Enter adds the calling goroutine to s until leave is called, once. A
// goroutine may enter more than once; it is in s until it has left as
// often. A goroutine whose identifier the runtime does not report is not
// added.
func (s *Set) Enter() (leave func()) {
	id := ID()
	if id == 0 {
		return func() {}
	}
	s.mu.Lock()
	if s.ids == nil {
		s.ids = make(map[uint64]int)
	}
	s.ids[id]++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ids[id]--; s.ids[id] <= 0 {
			delete(s.ids, id)
		}
	}
}

// Has reports whether the calling goroutine is in s.
func (s *Set) Has() bool {
	id := ID()
	if id == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids[id] > 0
}
