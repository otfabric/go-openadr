// SPDX-License-Identifier: MIT

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

func meta(id string) openadr.Metadata { return openadr.Metadata{ID: id} }

func TestMemoryConstraints(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	conflict := func(what string, err error) {
		t.Helper()
		var ce *ConstraintError
		if !errors.As(err, &ce) || !errors.Is(err, openadr.ErrConflict) || ce.Detail == "" {
			t.Errorf("%s: %v, want a ConstraintError", what, err)
		}
	}
	ok := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	ok("program", m.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "one"}))
	conflict("same ID", m.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "other"}))
	conflict("same name", m.Programs().Create(ctx, openadr.Program{Metadata: meta("p2"), Name: "one"}))
	ok("second program", m.Programs().Create(ctx, openadr.Program{Metadata: meta("p2"), Name: "two"}))
	conflict("rename onto another", m.Programs().Update(ctx, openadr.Program{Metadata: meta("p2"), Name: "one"}))
	ok("update keeping its name", m.Programs().Update(ctx, openadr.Program{Metadata: meta("p2"), Name: "two", Targets: []string{"t"}}))
	if err := m.Programs().Update(ctx, openadr.Program{Metadata: meta("nope"), Name: "x"}); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("update of a missing program: %v", err)
	}

	conflict("event without program", m.Events().Create(ctx, openadr.Event{Metadata: meta("e1"), ProgramID: "nope"}))
	ok("event", m.Events().Create(ctx, openadr.Event{Metadata: meta("e1"), ProgramID: "p1"}))
	_, err := m.Programs().Delete(ctx, "p1")
	conflict("delete program with events", err)

	conflict("report without event", m.Reports().Create(ctx, openadr.Report{Metadata: meta("r1"), EventID: "nope"}))
	ok("report", m.Reports().Create(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e1", ClientID: "c1", ClientName: "n1"}))
	for filter, want := range map[ReportFilter]int{
		{}:                                1,
		{ProgramID: "p1"}:                 1,
		{ProgramID: "p2"}:                 0,
		{EventID: "e1", ClientID: "c1"}:   1,
		{EventID: "e1", ClientID: "c2"}:   0,
		{ClientName: "n1"}:                1,
		{ClientName: "n1", EventID: "e2"}: 0,
	} {
		if got, _ := m.Reports().List(ctx, filter); len(got) != want {
			t.Errorf("reports %+v: %d, want %d", filter, len(got), want)
		}
	}

	// A report outlives its event and can still be updated.
	if _, err := m.Events().Delete(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	ok("update of an orphaned report", m.Reports().Update(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e1", Name: "later"}))
	conflict("moving a report to a missing event", m.Reports().Update(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e9"}))
	if got, _ := m.Reports().List(ctx, ReportFilter{ProgramID: "p1"}); len(got) != 0 {
		t.Error("an orphaned report still matches a program")
	}
	if _, err := m.Programs().Delete(ctx, "p1"); err != nil {
		t.Errorf("delete program without events: %v", err)
	}
	if _, err := m.Programs().Get(ctx, "p1"); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
	if _, err := m.Programs().Delete(ctx, "p1"); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}

	ok("ven", m.VENs().Create(ctx, openadr.VEN{Metadata: meta("v1"), Name: "site", ClientID: "c1"}))
	conflict("same ven name", m.VENs().Create(ctx, openadr.VEN{Metadata: meta("v2"), Name: "site"}))
	ok("second ven", m.VENs().Create(ctx, openadr.VEN{Metadata: meta("v2"), Name: "site-2", ClientID: "c2"}))
	conflict("resource without ven", m.Resources().Create(ctx, openadr.Resource{Metadata: meta("x1"), VENID: "nope", Name: "r"}))
	ok("resource", m.Resources().Create(ctx, openadr.Resource{Metadata: meta("x1"), VENID: "v1", Name: "r", ClientID: "c1"}))
	conflict("same resource name in the ven", m.Resources().Create(ctx, openadr.Resource{Metadata: meta("x2"), VENID: "v1", Name: "r"}))
	ok("same resource name in another ven", m.Resources().Create(ctx, openadr.Resource{Metadata: meta("x2"), VENID: "v2", Name: "r"}))
	_, err = m.VENs().Delete(ctx, "v1")
	conflict("delete ven with resources", err)
	if got, _ := m.VENs().List(ctx, VENFilter{ClientID: "c2"}); len(got) != 1 || got[0].ID != "v2" {
		t.Errorf("vens of c2 = %+v", got)
	}
	if got, _ := m.Resources().List(ctx, ResourceFilter{VENID: "v1", Name: "r"}); len(got) != 1 || got[0].ID != "x1" {
		t.Errorf("resources of v1 = %+v", got)
	}
}

func TestMemorySubscriptions(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	sub := func(id, program, client string) SubscriptionRecord {
		return SubscriptionRecord{Subscription: openadr.Subscription{Metadata: meta(id), ProgramID: program, ClientID: client, ClientName: "n-" + client}}
	}
	if err := m.Subscriptions().Create(ctx, sub("s1", "nope", "c1")); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("subscription to a missing program: %v", err)
	}
	_ = m.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "one"})
	for _, rec := range []SubscriptionRecord{sub("s1", "p1", "c1"), sub("s2", "", "c1"), sub("s3", "", "c2")} {
		if err := m.Subscriptions().Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	for filter, want := range map[SubscriptionFilter]int{
		{}:                                   3,
		{ClientID: "c1"}:                     2,
		{ProgramID: "p1"}:                    1,
		{ClientName: "n-c2"}:                 1,
		{ClientID: "c2", ProgramID: "p1"}:    0,
		{ClientID: "c1", ClientName: "n-c2"}: 0,
	} {
		if got, _ := m.Subscriptions().List(ctx, filter); len(got) != want {
			t.Errorf("subscriptions %+v: %d, want %d", filter, len(got), want)
		}
	}
	kept := sub("s2", "", "c1")
	kept.ReadsAll = true
	if err := m.Subscriptions().Update(ctx, kept); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Subscriptions().Get(ctx, "s2"); !got.ReadsAll {
		t.Error("the record lost what the VTN remembers about the subscriber")
	}
}

func TestMemoryOrderAndConcurrency(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("p%02d", i)
			_ = m.Programs().Create(ctx, openadr.Program{Metadata: meta(id), Name: id})
			_, _ = m.Programs().List(ctx, ProgramFilter{})
			_, _ = m.Programs().Get(ctx, id)
		}()
	}
	wg.Wait()
	// One name, many writers: exactly one wins.
	var created int
	var mu sync.Mutex
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.VENs().Create(ctx, openadr.VEN{Metadata: meta(fmt.Sprintf("v%d", i)), Name: "contested"}) == nil {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Errorf("%d VENs created with the same name", created)
	}

	m = NewMemory()
	for _, id := range []string{"c", "a", "b"} {
		_ = m.Programs().Create(ctx, openadr.Program{Metadata: meta(id), Name: id})
	}
	_, _ = m.Programs().Delete(ctx, "a")
	_ = m.Programs().Create(ctx, openadr.Program{Metadata: meta("a"), Name: "a"})
	got, _ := m.Programs().List(ctx, ProgramFilter{})
	if len(got) != 3 || got[0].ID != "c" || got[1].ID != "b" || got[2].ID != "a" {
		t.Errorf("order = %+v, want creation order c b a", got)
	}
}
