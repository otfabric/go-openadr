// SPDX-License-Identifier: MIT

// Package guard is how the module calls the application's code: a callback
// such as Notify, a metrics hook, or the handler of a logger. A call that
// does not return normally is stopped where it was made, logged at error
// level with what it ended with and its stack, once the caller holds no
// lock of its own, and the module goes on as if the call had returned.
//
// The entry is "<prefix>: callback did not return normally", with the
// fields callback (what was called, such as "Notify" or
// "Metrics.OnReceived"), value and stack. A handler of the logger that
// does not return normally loses its entry, and is reported in the same
// way with callback "Logger"; when that does not return normally either,
// it is dropped.
package guard

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"time"
)

// msgFailed is the message of the entry that reports a call of the
// application's code that did not return normally, after the prefix.
const msgFailed = "callback did not return normally"

// Failure is a call of the application's code that did not return
// normally. It is an error, for a caller that goes on as if the call had
// failed.
type Failure struct {
	callback string
	value    any
	stack    string
}

// Error says what was called and what it ended with:
// "Dial did not return normally: no route".
func (f *Failure) Error() string {
	return fmt.Sprintf("%s did not return normally: %v", f.callback, f.value)
}

// Value is what the call ended with.
func (f *Failure) Value() any { return f.value }

// Stack is the stack of the goroutine where the call ended.
func (f *Failure) Stack() string { return f.stack }

// Try calls f, the application's code that callback names. It returns nil
// when f returns, and the Failure when it does not, for the caller to give
// to Logger.Report once it has released its locks.
func Try(callback string, f func()) (fail *Failure) {
	defer func() {
		if r := recover(); r != nil {
			fail = &Failure{callback: callback, value: r, stack: string(debug.Stack())}
		}
	}()
	f()
	return nil
}

// Logger is the application's logger, or none. Each method calls the
// handler as slog.Logger does, and a handler that does not return normally
// costs that entry and nothing else. The zero Logger discards everything.
type Logger struct {
	l      *slog.Logger
	prefix string
}

// NewLogger returns the Logger of l, nil for none. prefix begins the entry
// of a failure, as the other messages of the package that logs begin:
// "mqtt", "webhook", "openadr".
func NewLogger(l *slog.Logger, prefix string) Logger {
	return Logger{l: l, prefix: prefix}
}

// Active reports whether there is a logger: a caller that has to work to
// make an entry asks first.
func (g Logger) Active() bool { return g.l != nil }

// Enabled is slog.Logger.Enabled; false without a logger, and when the
// handler's Enabled does not return normally.
func (g Logger) Enabled(ctx context.Context, level slog.Level) (enabled bool) {
	if g.l == nil {
		return false
	}
	defer g.rescue()
	if ctx == nil {
		ctx = context.Background()
	}
	return g.l.Handler().Enabled(ctx, level)
}

// Log is slog.Logger.Log.
func (g Logger) Log(ctx context.Context, level slog.Level, msg string, args ...any) {
	g.log(ctx, level, msg, args)
}

// Warn is slog.Logger.Warn.
func (g Logger) Warn(msg string, args ...any) {
	g.log(context.Background(), slog.LevelWarn, msg, args)
}

// Error is slog.Logger.Error.
func (g Logger) Error(msg string, args ...any) {
	g.log(context.Background(), slog.LevelError, msg, args)
}

// Report logs fail; nil logs nothing.
func (g Logger) Report(fail *Failure) {
	if fail == nil {
		return
	}
	g.log(context.Background(), slog.LevelError, g.prefix+": "+msgFailed,
		[]any{slog.String("callback", fail.callback), slog.Any("value", fail.value), slog.String("stack", fail.stack)})
}

// Call calls f as Try does and reports a failure at once, for a caller
// that holds no lock. It reports whether f returned normally.
func (g Logger) Call(callback string, f func()) bool {
	fail := Try(callback, f)
	g.Report(fail)
	return fail == nil
}

// log is what slog.Logger does for an entry, with the source of the entry
// where the method of this Logger was called.
func (g Logger) log(ctx context.Context, level slog.Level, msg string, args []any) {
	if g.l == nil {
		return
	}
	defer g.rescue()
	if ctx == nil {
		ctx = context.Background()
	}
	h := g.l.Handler()
	if !h.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	// Skipped: runtime.Callers, log, and the method that called log.
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(args...)
	_ = h.Handle(ctx, r)
}

// rescue ends a call of the handler that did not return normally: the
// entry is lost, and the failure is reported through the same handler.
func (g Logger) rescue() {
	if r := recover(); r != nil {
		g.lost(r, string(debug.Stack()))
	}
}

// lost reports a handler that did not return normally, once: when it does
// not return normally for this entry either, the entry is dropped.
func (g Logger) lost(value any, stack string) {
	defer func() { _ = recover() }()
	r := slog.NewRecord(time.Now(), slog.LevelError, g.prefix+": "+msgFailed, 0)
	r.Add(slog.String("callback", "Logger"), slog.Any("value", value), slog.String("stack", stack))
	_ = g.l.Handler().Handle(context.Background(), r)
}
