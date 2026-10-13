// SPDX-License-Identifier: MIT

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

func openFile(t *testing.T, path string) *File {
	t.Helper()
	f, err := OpenFile(path, FileOptions{NoSync: true})
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// dump lists everything in a store, for comparing two of them.
func dump(t *testing.T, s Store) []any {
	t.Helper()
	ctx := context.Background()
	programs, _ := s.Programs().List(ctx, ProgramFilter{})
	events, _ := s.Events().List(ctx, EventFilter{})
	reports, _ := s.Reports().List(ctx, ReportFilter{})
	vens, _ := s.VENs().List(ctx, VENFilter{})
	resources, _ := s.Resources().List(ctx, ResourceFilter{})
	subscriptions, _ := s.Subscriptions().List(ctx, SubscriptionFilter{})
	return []any{programs, events, reports, vens, resources, subscriptions}
}

func TestFileSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vtn.journal")
	f := openFile(t, path)

	at := time.Date(2026, 10, 12, 8, 30, 0, 123456789, time.UTC)
	m := func(id string) openadr.Metadata {
		return openadr.Metadata{ID: id, Created: at, Modified: at.Add(time.Second)}
	}
	start := at.Add(time.Hour)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.Programs().Create(ctx, openadr.Program{Metadata: m("p2"), Name: "second", Targets: []string{"group-a"},
		PayloadDescriptors: []openadr.PayloadDescriptor{openadr.EventPayloadDescriptor{PayloadType: "PRICE", Currency: "EUR"}}}))
	must(f.Programs().Create(ctx, openadr.Program{Metadata: m("p1"), Name: "first"}))
	must(f.Programs().Create(ctx, openadr.Program{Metadata: m("p3"), Name: "gone"}))
	must(f.Events().Create(ctx, openadr.Event{Metadata: m("e1"), ProgramID: "p1", Name: "prices", Priority: new(int),
		IntervalPeriod: &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Minutes: 15}},
		Intervals: []openadr.Interval{{ID: 3, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{
			openadr.Number(0.25), openadr.String("x"), openadr.Bool(true), openadr.PointValue(1, 2)}}}}}}))
	must(f.Events().Create(ctx, openadr.Event{Metadata: m("e2"), ProgramID: "p1", Name: "short-lived"}))
	must(f.Reports().Create(ctx, openadr.Report{Metadata: m("r1"), EventID: "e1", ClientID: "c1", ClientName: "n", Resources: []openadr.ReportResource{}}))
	must(f.Reports().Create(ctx, openadr.Report{Metadata: m("r2"), EventID: "e2", ClientID: "c1", ClientName: "n", Resources: []openadr.ReportResource{}}))
	must(f.VENs().Create(ctx, openadr.VEN{Metadata: m("v1"), Name: "site", ClientID: "c1", Targets: []string{"group-a"}}))
	must(f.Resources().Create(ctx, openadr.Resource{Metadata: m("x1"), VENID: "v1", Name: "battery", ClientID: "c1"}))
	must(f.Subscriptions().Create(ctx, SubscriptionRecord{ReadsAll: true, Subscription: openadr.Subscription{Metadata: m("s1"), ClientID: "bl", ClientName: "bl",
		ObjectOperations: []openadr.ObjectOperation{{Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate},
			CallbackURL: "https://example.com/h", BearerToken: "t"}}}}))
	// Changes after creation, and a report that outlives its event.
	must(f.Programs().Update(ctx, openadr.Program{Metadata: m("p1"), Name: "first-renamed"}))
	_, err := f.Programs().Delete(ctx, "p3")
	must(err)
	_, err = f.Events().Delete(ctx, "e2")
	must(err)

	want := dump(t, f)
	must(f.Close())
	if err := f.Programs().Create(ctx, openadr.Program{Metadata: m("late"), Name: "late"}); err == nil {
		t.Error("a write after Close succeeded")
	}

	for round := range 2 {
		g := openFile(t, path)
		if got := dump(t, g); !reflect.DeepEqual(got, want) {
			t.Fatalf("reopen %d:\n got %+v\nwant %+v", round, got, want)
		}
		// The rules still hold, on what was restored.
		var constraint *ConstraintError
		if err := g.Programs().Create(ctx, openadr.Program{Metadata: m("p9"), Name: "second"}); !errors.As(err, &constraint) {
			t.Errorf("reopen %d: duplicate name after restart: %v", round, err)
		}
		if _, err := g.Programs().Delete(ctx, "p1"); !errors.As(err, &constraint) {
			t.Errorf("reopen %d: deleting a program with events after restart: %v", round, err)
		}
		// A refused write leaves no trace in the journal.
		must(g.Close())
	}
	// Reopening rewrote the journal as a snapshot: one line per object.
	data, _ := os.ReadFile(path)
	if lines := strings.Count(string(data), "\n"); lines != 8 {
		t.Errorf("the compacted journal has %d lines, want 8:\n%s", lines, data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("the journal has mode %v; it holds bearer tokens of subscriptions", info.Mode().Perm())
	}
}

func TestFileCrashAndCorruption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "vtn.journal")
	f := openFile(t, path)
	for i := range 3 {
		if err := f.Programs().Create(ctx, openadr.Program{Metadata: meta(fmt.Sprintf("p%d", i)), Name: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	whole, _ := os.ReadFile(path)

	// A crash in the middle of the last write: that change is gone, the rest is there.
	cut := filepath.Join(dir, "cut.journal")
	_ = os.WriteFile(cut, whole[:len(whole)-20], 0o600)
	g := openFile(t, cut)
	if got, _ := g.Programs().List(ctx, ProgramFilter{}); len(got) != 2 {
		t.Errorf("after a cut-off last line: %d programs, want 2", len(got))
	}

	// Damage elsewhere is not guessed around.
	for name, damage := range map[string]func([]byte) []byte{
		"a line that is not JSON": func(b []byte) []byte { return append([]byte("{ not json\n"), b...) },
		"an unknown kind": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"kind":"program"`, `"kind":"tariff"`, 1))
		},
		"a change to a missing object": func(b []byte) []byte {
			return append(b, []byte(`{"op":"delete","kind":"program","id":"nope"}`+"\n")...)
		},
	} {
		bad := filepath.Join(dir, "bad.journal")
		_ = os.WriteFile(bad, damage(whole), 0o600)
		if _, err := OpenFile(bad, FileOptions{}); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v, want ErrCorrupt", name, err)
		}
		// And the file is left as it was, for whoever has to look at it.
		if after, _ := os.ReadFile(bad); string(after) != string(damage(whole)) {
			t.Errorf("%s: OpenFile rewrote a journal it could not read", name)
		}
	}
	if _, err := OpenFile(filepath.Join(dir, "no-such-dir", "x"), FileOptions{}); err == nil {
		t.Error("OpenFile in a directory that does not exist succeeded")
	}
}

// When the journal cannot be written, the store stops accepting changes
// instead of holding what the file does not.
func TestFileStopsOnWriteFailure(t *testing.T) {
	ctx := context.Background()
	f := openFile(t, filepath.Join(t.TempDir(), "vtn.journal"))
	if err := f.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "one"}); err != nil {
		t.Fatal(err)
	}
	_ = f.file.Close() // the disk goes away
	if err := f.Programs().Create(ctx, openadr.Program{Metadata: meta("p2"), Name: "two"}); err == nil || errors.Is(err, openadr.ErrConflict) {
		t.Fatalf("a write the journal could not take: %v", err)
	}
	if err := f.Programs().Create(ctx, openadr.Program{Metadata: meta("p3"), Name: "three"}); !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a write after the failure: %v", err)
	}
	if _, err := f.Programs().Get(ctx, "p1"); err != nil {
		t.Errorf("a read after the failure: %v", err)
	}
	if _, err := f.Programs().Get(ctx, "p3"); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("the refused write is in the store: %v", err)
	}
}

func TestFileConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vtn.journal")
	f := openFile(t, path)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("p%02d", i)
			_ = f.Programs().Create(ctx, openadr.Program{Metadata: meta(id), Name: id})
			_ = f.Programs().Update(ctx, openadr.Program{Metadata: meta(id), Name: id + "-renamed"})
			if i%4 == 0 {
				_, _ = f.Programs().Delete(ctx, id)
			}
		}()
	}
	wg.Wait()
	want := dump(t, f)
	_ = f.Close()
	// The journal was written in the order memory changed, so that it
	// replays to the same state, order of creation included.
	if got := dump(t, openFile(t, path)); !reflect.DeepEqual(got, want) {
		t.Errorf("replay differs from the state that was written:\n got %+v\nwant %+v", got[0], want[0])
	}
}

func TestFileClosed(t *testing.T) {
	f := openFile(t, filepath.Join(t.TempDir(), "vtn.journal"))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err := f.Programs().Create(context.Background(), openadr.Program{Metadata: meta("p1"), Name: "one"})
	if !errors.Is(err, openadr.ErrClosed) || errors.Is(err, ErrReadOnly) {
		t.Errorf("a write to a closed store: %v", err)
	}
}
