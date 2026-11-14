// SPDX-License-Identifier: MIT

// Package guardtest is for the tests of application code that does not
// return normally: a value such code ends with, and a log handler that
// records entries and can be made to end its calls the same way. It is not
// for any other use.
package guardtest

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Value is what the application code of the tests ends with.
const Value = "stopped by test"

// Stop ends the calling function abnormally, with Value.
func Stop() { panic(Value) }

// Message is the message of the entry that reports a call that did not
// return normally, after its prefix.
const Message = "callback did not return normally"

// Entry is a recorded log entry.
type Entry struct {
	Level   slog.Level
	Message string
	Attrs   map[string]string
}

// Handler is a slog.Handler that records every entry.
type Handler struct {
	mu      sync.Mutex
	entries []Entry
	changed chan struct{}
	// fail is called for every entry; when it reports true, Handle does
	// not return normally for that entry.
	fail func(Entry) bool
	// failEnabled makes Enabled not return normally.
	failEnabled bool
}

// NewHandler returns a Handler that records everything.
func NewHandler() *Handler { return &Handler{changed: make(chan struct{})} }

// Logger returns a logger of h.
func (h *Handler) Logger() *slog.Logger { return slog.New(h) }

// FailWhen makes Handle not return normally for the entries f reports true
// for, after it recorded them.
func (h *Handler) FailWhen(f func(Entry) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail = f
}

// FailEnabled makes Enabled not return normally.
func (h *Handler) FailEnabled() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failEnabled = true
}

// Enabled implements slog.Handler: every level.
func (h *Handler) Enabled(context.Context, slog.Level) bool {
	h.mu.Lock()
	fail := h.failEnabled
	h.mu.Unlock()
	if fail {
		Stop()
	}
	return true
}

// Handle implements slog.Handler.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	e := Entry{Level: r.Level, Message: r.Message, Attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		e.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.entries = append(h.entries, e)
	close(h.changed)
	h.changed = make(chan struct{})
	fail := h.fail
	h.mu.Unlock()
	if fail != nil && fail(e) {
		Stop()
	}
	return nil
}

// WithAttrs implements slog.Handler; the attributes are not recorded.
func (h *Handler) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup implements slog.Handler; the group is not recorded.
func (h *Handler) WithGroup(string) slog.Handler { return h }

// Entries returns what was recorded so far.
func (h *Handler) Entries() []Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Entry(nil), h.entries...)
}

// Failures counts the entries that report a call of callback that did not
// return normally, at error level, with Value and a stack.
func (h *Handler) Failures(callback string) int {
	n := 0
	for _, e := range h.Entries() {
		if IsFailure(e, callback) {
			n++
		}
	}
	return n
}

// IsFailure reports whether e reports a call of callback that did not
// return normally, at error level, with Value and a stack that names the
// function that ended it.
func IsFailure(e Entry, callback string) bool {
	return e.Level == slog.LevelError && strings.HasSuffix(e.Message, ": "+Message) &&
		e.Attrs["callback"] == callback && e.Attrs["value"] == Value &&
		strings.Contains(e.Attrs["stack"], "guardtest.Stop")
}

// Wait waits until ok reports true for the entries recorded, and fails the
// test when that has not happened within 10 seconds.
func (h *Handler) Wait(t testing.TB, what string, ok func([]Entry) bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		entries := append([]Entry(nil), h.entries...)
		changed := h.changed
		h.mu.Unlock()
		if ok(entries) {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("waited 10s for %s; the log has %d entries: %v", what, len(entries), entries)
		}
	}
}

// WaitFailures waits until at least n failures of callback are recorded.
func (h *Handler) WaitFailures(t testing.TB, callback string, n int) {
	t.Helper()
	h.Wait(t, callback+" reported "+strconv.Itoa(n)+" times", func(entries []Entry) bool {
		got := 0
		for _, e := range entries {
			if IsFailure(e, callback) {
				got++
			}
		}
		return got >= n
	})
}
