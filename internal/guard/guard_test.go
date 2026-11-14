// SPDX-License-Identifier: MIT

package guard_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/otfabric/go-openadr/internal/guard"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
)

func TestTry(t *testing.T) {
	ran := false
	if fail := guard.Try("Notify", func() { ran = true }); fail != nil || !ran {
		t.Fatalf("a call that returns: %v, ran %v", fail, ran)
	}
	fail := guard.Try("Notify", guardtest.Stop)
	if fail == nil {
		t.Fatal("a call that does not return normally gave no Failure")
	}
	if fail.Value() != guardtest.Value || !strings.Contains(fail.Stack(), "guardtest.Stop") {
		t.Errorf("value %v, stack %q", fail.Value(), fail.Stack())
	}
	if want := "Notify did not return normally: " + guardtest.Value; fail.Error() != want {
		t.Errorf("Error() = %q, want %q", fail.Error(), want)
	}
}

func TestCallAndReport(t *testing.T) {
	h := guardtest.NewHandler()
	g := guard.NewLogger(h.Logger(), "mqtt")
	if !g.Call("Metrics.OnReceived", func() {}) {
		t.Error("a call that returns reported false")
	}
	if g.Call("Metrics.OnReceived", guardtest.Stop) {
		t.Error("a call that does not return normally reported true")
	}
	entries := h.Entries()
	if len(entries) != 1 || !guardtest.IsFailure(entries[0], "Metrics.OnReceived") || entries[0].Message != "mqtt: "+guardtest.Message {
		t.Fatalf("entries %+v", entries)
	}
	g.Report(nil)
	if n := len(h.Entries()); n != 1 {
		t.Errorf("Report(nil) logged: %d entries", n)
	}
}

func TestNoLogger(t *testing.T) {
	var g guard.Logger
	if g.Active() || g.Enabled(context.Background(), slog.LevelError) {
		t.Error("the zero Logger is active")
	}
	g.Warn("nothing")
	g.Error("nothing")
	g.Log(context.Background(), slog.LevelInfo, "nothing")
	if g.Call("Notify", guardtest.Stop) {
		t.Error("a call that does not return normally reported true")
	}
	if !guard.NewLogger(guardtest.NewHandler().Logger(), "x").Active() {
		t.Error("a Logger with a logger is not active")
	}
}

func TestLevelsAndContext(t *testing.T) {
	h := guardtest.NewHandler()
	g := guard.NewLogger(h.Logger(), "openadr")
	g.Warn("w", slog.String("k", "v"))
	g.Error("e")
	var none context.Context // slog.Logger takes a nil context too
	g.Log(none, slog.LevelDebug, "d")
	if !g.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("not enabled")
	}
	got := h.Entries()
	if len(got) != 3 || got[0].Level != slog.LevelWarn || got[0].Attrs["k"] != "v" ||
		got[1].Level != slog.LevelError || got[2].Level != slog.LevelDebug || got[2].Message != "d" {
		t.Errorf("entries %+v", got)
	}
}

// A handler that does not return normally loses that entry and is reported
// once, through the same handler; the caller goes on.
func TestHandlerThatDoesNotReturnNormally(t *testing.T) {
	h := guardtest.NewHandler()
	h.FailWhen(func(e guardtest.Entry) bool { return e.Message == "lost" })
	g := guard.NewLogger(h.Logger(), "webhook")
	g.Warn("lost")
	g.Warn("kept")
	got := h.Entries()
	if len(got) != 3 || got[0].Message != "lost" || !guardtest.IsFailure(got[1], "Logger") ||
		got[1].Message != "webhook: "+guardtest.Message || got[2].Message != "kept" {
		t.Errorf("entries %+v", got)
	}

	// One that does not return normally for the report either: dropped.
	all := guardtest.NewHandler()
	all.FailWhen(func(guardtest.Entry) bool { return true })
	g = guard.NewLogger(all.Logger(), "webhook")
	g.Warn("lost")
	if g.Call("Notify", guardtest.Stop) {
		t.Error("a call that does not return normally reported true")
	}
	if n := len(all.Entries()); n != 4 {
		t.Errorf("%d entries, want 4: two entries and their two reports, each recorded before it ended", n)
	}

	// Enabled that does not return normally: not enabled, and nothing else.
	en := guardtest.NewHandler()
	en.FailEnabled()
	g = guard.NewLogger(en.Logger(), "x")
	if g.Enabled(context.Background(), slog.LevelError) {
		t.Error("enabled")
	}
	g.Warn("lost")
	if got := en.Entries(); len(got) != 2 || !guardtest.IsFailure(got[0], "Logger") {
		t.Errorf("entries %+v", got)
	}
}

// The source of an entry is where the method of the Logger was called, as
// with slog.Logger.
func TestSource(t *testing.T) {
	var sb strings.Builder
	l := slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{AddSource: true}))
	guard.NewLogger(l, "x").Warn("here")
	if !strings.Contains(sb.String(), "guard_test.go:") {
		t.Errorf("source not the caller: %s", sb.String())
	}
}
