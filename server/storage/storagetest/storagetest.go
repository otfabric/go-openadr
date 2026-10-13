// SPDX-License-Identifier: MIT

// Package storagetest is a test suite for implementations of storage.Store.
//
// A VTN relies on its store for more than keeping objects: for the rules
// that have to be decided together with a write (unique names, references
// that lead somewhere, what may not be deleted), for the order of a search,
// and for returning what it was given. Run checks all of it, so that a
// store on a database of your own behaves as the ones of this module do:
//
//	func TestMyStore(t *testing.T) {
//		storagetest.Run(t, storagetest.Config{
//			New: func(t *testing.T) storage.Store { return newMyStore(t) },
//		})
//	}
//
// The stores of this module pass it, and so do the SQLite and PostgreSQL
// stores in examples/storage.
package storagetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/server/storage"
)

// Config says how to make the store under test.
type Config struct {
	// New returns an empty store. It is called once per test of the suite,
	// and closes what it opens with t.Cleanup. Required.
	New func(t *testing.T) storage.Store
	// Reopen, when set, closes a store and returns one that holds what the
	// first held, as after a restart: the suite then checks that
	// everything is still there. Leave it nil for a store that does not
	// outlive its process.
	Reopen func(t *testing.T, s storage.Store) storage.Store
}

// Run runs the suite.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.New == nil {
		t.Fatal("storagetest: Config.New is required")
	}
	t.Run("Rules", func(t *testing.T) { rules(t, cfg.New(t)) })
	t.Run("Subscriptions", func(t *testing.T) { subscriptions(t, cfg.New(t)) })
	t.Run("ResourceGroups", func(t *testing.T) { resourceGroups(t, cfg.New(t)) })
	t.Run("Order", func(t *testing.T) { order(t, cfg.New(t)) })
	t.Run("Concurrency", func(t *testing.T) { concurrency(t, cfg.New(t)) })
	t.Run("Fidelity", func(t *testing.T) { fidelity(t, cfg) })
	// A store that is also an Outbox is held to that too.
	if _, ok := cfg.New(t).(storage.Outbox); ok {
		t.Run("Outbox", func(t *testing.T) { outbox(t, cfg) })
	}
}

func outbox(t *testing.T, cfg Config) {
	ctx := context.Background()
	s := cfg.New(t)
	box := s.(storage.Outbox)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	delivery := func(id string) storage.Delivery {
		return storage.Delivery{ID: id, SubscriptionID: "s-" + id, CallbackURL: "https://example.com/hook?" + id, BearerToken: "token-" + id,
			Body: []byte(`{"objectType":"PROGRAM","operation":"CREATE","object":{"programName":"` + id + ` \u00e9"}}`), Created: at}
	}
	if list, err := box.Deliveries(ctx); err != nil || len(list) != 0 {
		t.Fatalf("an empty outbox: %+v, %v", list, err)
	}
	for _, id := range []string{"c", "a", "b"} {
		must(t, "delivery "+id, box.AddDelivery(ctx, delivery(id)))
	}
	if err := box.AddDelivery(ctx, delivery("a")); err == nil {
		t.Error("a second delivery with an ID in use was taken")
	}
	must(t, "removing a delivery", box.RemoveDelivery(ctx, "a"))
	must(t, "removing a delivery that is not there", box.RemoveDelivery(ctx, "a"))
	must(t, "removing a delivery that never was", box.RemoveDelivery(ctx, "nope"))
	must(t, "a delivery after a removal", box.AddDelivery(ctx, delivery("d")))
	check := func(when string, box storage.Outbox) {
		t.Helper()
		list, err := box.Deliveries(ctx)
		if err != nil || len(list) != 3 || list[0].ID != "c" || list[1].ID != "b" || list[2].ID != "d" {
			t.Fatalf("%s: the outbox holds %+v, %v; want c, b, d, oldest first", when, list, err)
		}
		got, want := list[1], delivery("b")
		if got.SubscriptionID != want.SubscriptionID || got.CallbackURL != want.CallbackURL || got.BearerToken != want.BearerToken || !got.Created.Equal(want.Created) {
			t.Errorf("%s: a delivery came back as %+v, want %+v", when, got, want)
		}
		// The body is JSON, and comes back as the same JSON.
		var a, b any
		if json.Unmarshal(got.Body, &a) != nil || json.Unmarshal(want.Body, &b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s: the body came back as %s, want %s", when, got.Body, want.Body)
		}
	}
	check("as recorded", box)
	// Many at once: all are kept, none twice.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("p%02d", i)
			if err := box.AddDelivery(ctx, delivery(id)); err != nil {
				t.Errorf("adding %s: %v", id, err)
			}
			if i%2 == 0 {
				if err := box.RemoveDelivery(ctx, id); err != nil {
					t.Errorf("removing %s: %v", id, err)
				}
			}
		}()
	}
	wg.Wait()
	if list, _ := box.Deliveries(ctx); len(list) != 13 {
		t.Errorf("%d deliveries are kept, want 13", len(list))
	}
	for i := 1; i < 20; i += 2 {
		must(t, "removing", box.RemoveDelivery(ctx, fmt.Sprintf("p%02d", i)))
	}
	if cfg.Reopen == nil {
		return
	}
	// What an outbox is for: it is all there after a restart.
	check("after a restart", cfg.Reopen(t, s).(storage.Outbox))
}

func meta(id string) openadr.Metadata { return openadr.Metadata{ID: id} }

func must(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// conflict requires a *storage.ConstraintError, which is what tells the
// VTN to answer 409 and with what words.
func conflict(t *testing.T, what string, err error) {
	t.Helper()
	var ce *storage.ConstraintError
	if !errors.As(err, &ce) || !errors.Is(err, openadr.ErrConflict) || ce.Detail == "" {
		t.Errorf("%s: got %v, want a *storage.ConstraintError with a Detail", what, err)
	}
}

func notFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("%s: got %v, want openadr.ErrNotFound", what, err)
	}
}

func rules(t *testing.T, s storage.Store) {
	ctx := context.Background()

	must(t, "program", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "one"}))
	conflict(t, "a program with an ID in use", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "other"}))
	conflict(t, "a program with a name in use", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p2"), Name: "one"}))
	must(t, "second program", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p2"), Name: "two"}))
	conflict(t, "renaming a program onto another", s.Programs().Update(ctx, openadr.Program{Metadata: meta("p2"), Name: "one"}))
	must(t, "updating a program that keeps its name", s.Programs().Update(ctx, openadr.Program{Metadata: meta("p2"), Name: "two", Targets: []string{"t"}}))
	if got, err := s.Programs().Get(ctx, "p2"); err != nil || len(got.Targets) != 1 {
		t.Errorf("the updated program: %+v, %v", got, err)
	}
	notFound(t, "updating a program that is not there", s.Programs().Update(ctx, openadr.Program{Metadata: meta("nope"), Name: "x"}))
	_, err := s.Programs().Get(ctx, "nope")
	notFound(t, "reading a program that is not there", err)

	conflict(t, "an event of a program that is not there", s.Events().Create(ctx, openadr.Event{Metadata: meta("e1"), ProgramID: "nope"}))
	must(t, "event", s.Events().Create(ctx, openadr.Event{Metadata: meta("e1"), ProgramID: "p1"}))
	must(t, "second event", s.Events().Create(ctx, openadr.Event{Metadata: meta("e2"), ProgramID: "p2"}))
	conflict(t, "moving an event to a program that is not there", s.Events().Update(ctx, openadr.Event{Metadata: meta("e1"), ProgramID: "nope"}))
	if got, _ := s.Events().List(ctx, storage.EventFilter{ProgramID: "p1"}); len(got) != 1 || got[0].ID != "e1" {
		t.Errorf("events of p1: %+v", got)
	}
	if got, _ := s.Events().List(ctx, storage.EventFilter{}); len(got) != 2 {
		t.Errorf("all events: %+v", got)
	}
	_, err = s.Programs().Delete(ctx, "p1")
	conflict(t, "deleting a program that has events", err)

	conflict(t, "a report on an event that is not there", s.Reports().Create(ctx, openadr.Report{Metadata: meta("r1"), EventID: "nope"}))
	must(t, "report", s.Reports().Create(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e1", ClientID: "c1", ClientName: "n1"}))
	for filter, want := range map[storage.ReportFilter]int{
		{}:                                1,
		{ProgramID: "p1"}:                 1,
		{ProgramID: "p2"}:                 0,
		{EventID: "e1", ClientID: "c1"}:   1,
		{EventID: "e1", ClientID: "c2"}:   0,
		{ClientName: "n1"}:                1,
		{ClientName: "n1", EventID: "e2"}: 0,
	} {
		if got, err := s.Reports().List(ctx, filter); err != nil || len(got) != want {
			t.Errorf("reports matching %+v: %d, %v; want %d", filter, len(got), err, want)
		}
	}

	// A report outlives its event, and can still be changed.
	deleted, err := s.Events().Delete(ctx, "e1")
	if err != nil || deleted.ID != "e1" || deleted.ProgramID != "p1" {
		t.Fatalf("deleting an event returns it: %+v, %v", deleted, err)
	}
	must(t, "updating a report whose event is gone", s.Reports().Update(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e1", ClientID: "c1", Name: "later"}))
	conflict(t, "moving a report to an event that is not there", s.Reports().Update(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e9"}))
	must(t, "moving a report to an event that is", s.Reports().Update(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e2", ClientID: "c1"}))
	conflict(t, "moving a report back to the event that is gone", s.Reports().Update(ctx, openadr.Report{Metadata: meta("r1"), EventID: "e1"}))
	if got, _ := s.Reports().List(ctx, storage.ReportFilter{ProgramID: "p1"}); len(got) != 0 {
		t.Error("a report still matches the program of an event it no longer belongs to")
	}
	if got, _ := s.Reports().List(ctx, storage.ReportFilter{ProgramID: "p2"}); len(got) != 1 {
		t.Error("a report does not match the program of its event")
	}
	if _, err := s.Programs().Delete(ctx, "p1"); err != nil {
		t.Errorf("deleting a program without events: %v", err)
	}
	_, err = s.Programs().Get(ctx, "p1")
	notFound(t, "reading a deleted program", err)
	_, err = s.Programs().Delete(ctx, "p1")
	notFound(t, "deleting a program twice", err)
	// Its name is free again.
	must(t, "a new program with the name of a deleted one", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p3"), Name: "one"}))

	must(t, "ven", s.VENs().Create(ctx, openadr.VEN{Metadata: meta("v1"), Name: "site", ClientID: "c1"}))
	conflict(t, "a ven with a name in use", s.VENs().Create(ctx, openadr.VEN{Metadata: meta("v2"), Name: "site"}))
	must(t, "second ven", s.VENs().Create(ctx, openadr.VEN{Metadata: meta("v2"), Name: "site-2", ClientID: "c2"}))
	conflict(t, "renaming a ven onto another", s.VENs().Update(ctx, openadr.VEN{Metadata: meta("v2"), Name: "site", ClientID: "c2"}))
	conflict(t, "a resource of a ven that is not there", s.Resources().Create(ctx, openadr.Resource{Metadata: meta("x1"), VENID: "nope", Name: "r"}))
	must(t, "resource", s.Resources().Create(ctx, openadr.Resource{Metadata: meta("x1"), VENID: "v1", Name: "r", ClientID: "c1"}))
	conflict(t, "a resource with a name in use in its ven", s.Resources().Create(ctx, openadr.Resource{Metadata: meta("x2"), VENID: "v1", Name: "r"}))
	must(t, "a resource with the same name in another ven", s.Resources().Create(ctx, openadr.Resource{Metadata: meta("x2"), VENID: "v2", Name: "r", ClientID: "c2"}))
	conflict(t, "moving a resource to a ven where its name is in use", s.Resources().Update(ctx, openadr.Resource{Metadata: meta("x2"), VENID: "v1", Name: "r"}))
	_, err = s.VENs().Delete(ctx, "v1")
	conflict(t, "deleting a ven that has resources", err)
	for filter, want := range map[storage.VENFilter]string{
		{ClientID: "c2"}: "v2", {Name: "site"}: "v1", {Name: "site-2", ClientID: "c2"}: "v2", {Name: "site", ClientID: "c2"}: "",
	} {
		got, err := s.VENs().List(ctx, filter)
		if err != nil || (want == "") != (len(got) == 0) || want != "" && got[0].ID != want {
			t.Errorf("vens matching %+v: %+v, %v; want %q", filter, got, err, want)
		}
	}
	for filter, want := range map[storage.ResourceFilter]int{
		{}: 2, {VENID: "v1", Name: "r"}: 1, {Name: "r"}: 2, {ClientID: "c2"}: 1, {VENID: "v1", ClientID: "c2"}: 0,
	} {
		if got, err := s.Resources().List(ctx, filter); err != nil || len(got) != want {
			t.Errorf("resources matching %+v: %d, %v; want %d", filter, len(got), err, want)
		}
	}
	if _, err := s.Resources().Delete(ctx, "x1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VENs().Delete(ctx, "v1"); err != nil {
		t.Errorf("deleting a ven whose resources are gone: %v", err)
	}
	// An empty search is an empty list, not an error.
	if got, err := s.Programs().List(ctx, storage.ProgramFilter{}); err != nil || len(got) != 2 {
		t.Errorf("programs: %+v, %v", got, err)
	}
	if got, err := s.Events().List(ctx, storage.EventFilter{ProgramID: "nope"}); err != nil || len(got) != 0 {
		t.Errorf("events of no program: %+v, %v", got, err)
	}
}

func subscriptions(t *testing.T, s storage.Store) {
	ctx := context.Background()
	sub := func(id, program, client string) storage.SubscriptionRecord {
		return storage.SubscriptionRecord{Subscription: openadr.Subscription{Metadata: meta(id), ProgramID: program, ClientID: client, ClientName: "n-" + client}}
	}
	conflict(t, "a subscription to a program that is not there", s.Subscriptions().Create(ctx, sub("s1", "nope", "c1")))
	must(t, "program", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p1"), Name: "one"}))
	for _, rec := range []storage.SubscriptionRecord{sub("s1", "p1", "c1"), sub("s2", "", "c1"), sub("s3", "", "c2")} {
		must(t, "subscription "+rec.ID, s.Subscriptions().Create(ctx, rec))
	}
	conflict(t, "a subscription with an ID in use", s.Subscriptions().Create(ctx, sub("s1", "", "c9")))
	for filter, want := range map[storage.SubscriptionFilter]int{
		{}:                                   3,
		{ClientID: "c1"}:                     2,
		{ProgramID: "p1"}:                    1,
		{ClientName: "n-c2"}:                 1,
		{ClientID: "c2", ProgramID: "p1"}:    0,
		{ClientID: "c1", ClientName: "n-c2"}: 0,
	} {
		if got, err := s.Subscriptions().List(ctx, filter); err != nil || len(got) != want {
			t.Errorf("subscriptions matching %+v: %d, %v; want %d", filter, len(got), err, want)
		}
	}
	kept := sub("s2", "", "c1")
	kept.ReadsAll = true
	kept.ObjectOperations = []openadr.ObjectOperation{{
		Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate},
		CallbackURL: "https://example.com/hook", BearerToken: "a-secret",
	}, {
		Objects: []openadr.ObjectType{openadr.ObjectProgram}, Operations: []openadr.Operation{openadr.OperationDelete},
		Mechanism: openadr.MechanismWebSocket,
	}}
	must(t, "updating a subscription", s.Subscriptions().Update(ctx, kept))
	got, err := s.Subscriptions().Get(ctx, "s2")
	if err != nil || !got.ReadsAll {
		t.Errorf("the record lost what the VTN remembers about the subscriber: %+v, %v", got, err)
	}
	if len(got.ObjectOperations) != 2 || got.ObjectOperations[0].BearerToken != "a-secret" || got.ObjectOperations[1].Mechanism != openadr.MechanismWebSocket {
		t.Errorf("the object operations came back as %+v", got.ObjectOperations)
	}
	conflict(t, "pointing a subscription at a program that is not there", s.Subscriptions().Update(ctx, sub("s3", "nope", "c2")))
	if deleted, err := s.Subscriptions().Delete(ctx, "s2"); err != nil || !deleted.ReadsAll {
		t.Errorf("deleting a subscription returns the record: %+v, %v", deleted, err)
	}
	_, err = s.Subscriptions().Get(ctx, "s2")
	notFound(t, "reading a deleted subscription", err)
}

func resourceGroups(t *testing.T, s storage.Store) {
	ctx := context.Background()
	group := func(id, name string, children ...openadr.ResourceGroupChild) openadr.ResourceGroup {
		return openadr.ResourceGroup{Metadata: meta(id), Name: name, Children: children}
	}
	res := func(id string) openadr.ResourceGroupChild {
		return openadr.ResourceGroupChild{Type: openadr.ChildResource, ID: id}
	}
	grp := func(id string) openadr.ResourceGroupChild {
		return openadr.ResourceGroupChild{Type: openadr.ChildResourceGroup, ID: id}
	}
	children := func(id string) []openadr.ResourceGroupChild {
		t.Helper()
		g, err := s.ResourceGroups().Get(ctx, id)
		if err != nil {
			t.Fatalf("group %s: %v", id, err)
		}
		return g.Children
	}
	groups := s.ResourceGroups()

	must(t, "ven", s.VENs().Create(ctx, openadr.VEN{Metadata: meta("v1"), Name: "site", ClientID: "ven"}))
	must(t, "resource", s.Resources().Create(ctx, openadr.Resource{Metadata: meta("r1"), VENID: "v1", Name: "one"}))
	must(t, "resource", s.Resources().Create(ctx, openadr.Resource{Metadata: meta("r2"), VENID: "v1", Name: "two"}))
	// a holds r1 and b; b holds r2 and itself; c holds a and b.
	must(t, "group a", groups.Create(ctx, group("a", "A", res("r1"))))
	must(t, "a group that holds itself", groups.Create(ctx, group("b", "B", res("r2"), grp("b"))))
	must(t, "a group that comes to hold a later one", groups.Update(ctx, group("a", "A", res("r1"), grp("b"))))
	must(t, "group c", groups.Create(ctx, group("c", "C", grp("a"), grp("b"))))

	for what, g := range map[string]openadr.ResourceGroup{
		"a resource that is not there":           group("x", "X", res("nope")),
		"a group that is not there":              group("x", "X", grp("nope")),
		"a group given as a resource":            group("x", "X", res("a")),
		"a resource given as a group":            group("x", "X", grp("r1")),
		"a child of a kind that is not one":      group("x", "X", openadr.ResourceGroupChild{Type: "device", ID: "r1"}),
		"an ID in use":                           group("a", "again"),
		"an ID in use, by a group that held one": group("b", "again"),
	} {
		conflict(t, "a group with "+what, groups.Create(ctx, g))
	}
	conflict(t, "updating a group to hold what is not there", groups.Update(ctx, group("a", "A", res("nope"))))
	notFound(t, "updating a group that is not there", groups.Update(ctx, group("nope", "X")))
	// Names are not unique.
	must(t, "a second group of the same name", groups.Create(ctx, group("a2", "A")))
	if list, err := groups.List(ctx, storage.ResourceGroupFilter{Name: "A"}); err != nil || len(list) != 2 || list[0].ID != "a" {
		t.Errorf("groups named A: %+v, %v", list, err)
	}
	if list, _ := groups.List(ctx, storage.ResourceGroupFilter{}); len(list) != 4 {
		t.Errorf("all groups: %d", len(list))
	}

	// What is deleted leaves the groups that held it; a value read before
	// is not changed under its holder.
	held := children("a")
	if _, err := s.Resources().Delete(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := children("a"); !reflect.DeepEqual(got, []openadr.ResourceGroupChild{grp("b")}) {
		t.Errorf("a, after its resource was deleted, holds %+v", got)
	}
	if !reflect.DeepEqual(held, []openadr.ResourceGroupChild{res("r1"), grp("b")}) {
		t.Errorf("a value that was read earlier was changed: %+v", held)
	}
	if _, err := groups.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if got := children("a"); len(got) != 0 {
		t.Errorf("a, after b was deleted, holds %+v", got)
	}
	if got := children("c"); !reflect.DeepEqual(got, []openadr.ResourceGroupChild{grp("a")}) {
		t.Errorf("c, after b was deleted, holds %+v", got)
	}
	_, err := groups.Delete(ctx, "b")
	notFound(t, "deleting a group twice", err)
	// The resource of a deleted group is still there, and still keeps its
	// ven from being deleted.
	if _, err := s.Resources().Get(ctx, "r2"); err != nil {
		t.Errorf("the resource of a deleted group: %v", err)
	}
	_, err = s.VENs().Delete(ctx, "v1")
	conflict(t, "deleting the ven of a resource", err)
}

func order(t *testing.T, s storage.Store) {
	ctx := context.Background()
	for _, id := range []string{"c", "a", "b"} {
		must(t, "program "+id, s.Programs().Create(ctx, openadr.Program{Metadata: meta(id), Name: id}))
	}
	// An update keeps the place of an object, a delete and a new create
	// puts it last.
	must(t, "update", s.Programs().Update(ctx, openadr.Program{Metadata: meta("c"), Name: "c", Targets: []string{"t"}}))
	if _, err := s.Programs().Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	must(t, "program a again", s.Programs().Create(ctx, openadr.Program{Metadata: meta("a"), Name: "a"}))
	got, err := s.Programs().List(ctx, storage.ProgramFilter{})
	if err != nil || len(got) != 3 || got[0].ID != "c" || got[1].ID != "b" || got[2].ID != "a" {
		t.Errorf("programs are listed as %+v, %v; want them in the order they were created: c, b, a", got, err)
	}
	// Many more than a page, and more than nine: the order is that of
	// creation, not of the IDs as text.
	for i := range 120 {
		must(t, "event", s.Events().Create(ctx, openadr.Event{Metadata: meta(fmt.Sprintf("e%d", i)), ProgramID: "c"}))
	}
	events, err := s.Events().List(ctx, storage.EventFilter{ProgramID: "c"})
	if err != nil || len(events) != 120 {
		t.Fatalf("%d events, %v", len(events), err)
	}
	for i, e := range events {
		if e.ID != fmt.Sprintf("e%d", i) {
			t.Fatalf("event %d of the list is %s", i, e.ID)
		}
	}
}

func concurrency(t *testing.T, s storage.Store) {
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("p%02d", i)
			if err := s.Programs().Create(ctx, openadr.Program{Metadata: meta(id), Name: id}); err != nil {
				t.Errorf("creating %s: %v", id, err)
			}
			if _, err := s.Programs().List(ctx, storage.ProgramFilter{}); err != nil {
				t.Errorf("listing: %v", err)
			}
			if _, err := s.Programs().Get(ctx, id); err != nil {
				t.Errorf("reading %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if got, _ := s.Programs().List(ctx, storage.ProgramFilter{}); len(got) != 40 {
		t.Errorf("%d programs of 40", len(got))
	}

	// One name, many writers: exactly one wins, and the others are told
	// of a conflict, not of a failure.
	var (
		mu      sync.Mutex
		created int
	)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.VENs().Create(ctx, openadr.VEN{Metadata: meta(fmt.Sprintf("v%d", i)), Name: "contested"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case !errors.Is(err, openadr.ErrConflict):
				t.Errorf("a writer that lost the name was told: %v", err)
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Errorf("%d vens were created with the same name", created)
	}

	// A program is deleted while events are made for it: afterwards
	// either it is gone and has none, or it is there.
	must(t, "program", s.Programs().Create(ctx, openadr.Program{Metadata: meta("contested"), Name: "contested"}))
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 10 {
				_, _ = s.Programs().Delete(ctx, "contested")
				return
			}
			_ = s.Events().Create(ctx, openadr.Event{Metadata: meta(fmt.Sprintf("ce%d", i)), ProgramID: "contested"})
		}()
	}
	wg.Wait()
	events, _ := s.Events().List(ctx, storage.EventFilter{ProgramID: "contested"})
	if _, err := s.Programs().Get(ctx, "contested"); err != nil && len(events) > 0 {
		t.Errorf("a program was deleted and left %d events behind", len(events))
	}
}

// fidelity checks that a store returns what it was given, with every
// property of every kind of object, now and after a restart.
func fidelity(t *testing.T, cfg Config) {
	ctx := context.Background()
	s := cfg.New(t)
	// Times as a store can keep them: UTC, without a monotonic reading.
	at := time.Date(2030, 1, 2, 3, 4, 5, 678000000, time.UTC)
	later := at.Add(90 * time.Minute)
	md := func(id string) openadr.Metadata { return openadr.Metadata{ID: id, Created: at, Modified: later} }
	accuracy, confidence, priority := 0.5, 95, 3

	program := openadr.Program{
		Metadata: md("p1"), Name: "Tariff — ünïcode 'quotes' \"and\" \\slashes\\",
		IntervalPeriod: &openadr.IntervalPeriod{Start: &at, Duration: &openadr.Duration{Years: 1, Months: 2, Days: 3, Hours: 4, Minutes: 5, Seconds: 6.5}},
		Descriptions:   []openadr.ProgramDescription{{URL: "https://example.com/a?b=c&d=e"}},
		PayloadDescriptors: []openadr.PayloadDescriptor{
			openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"},
			openadr.ReportPayloadDescriptor{PayloadType: "USAGE", ReadingType: "DIRECT_READ", Units: "KWH", Accuracy: &accuracy, Confidence: &confidence},
		},
		Attributes: []openadr.ValuesMap{{Type: "LOCATION", Values: []openadr.Value{openadr.PointValue(52.1, -4.3)}}},
		Targets:    []string{"group-a", "group-b"},
	}
	descriptor := openadr.NewReportDescriptor("USAGE")
	descriptor.Targets, descriptor.Aggregate, descriptor.ReportIntervals = []string{"group-a"}, true, openadr.ReportIntervalsSubIntervals
	event := openadr.Event{
		Metadata: md("e1"), ProgramID: "p1", Name: "prices", Duration: &openadr.Infinite, Priority: &priority,
		Targets: []string{"group-a"}, ReportDescriptors: []openadr.ReportDescriptor{descriptor},
		PayloadDescriptors: []openadr.EventPayloadDescriptor{{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"}},
		IntervalPeriod:     &openadr.IntervalPeriod{Start: &time.Time{}, Duration: &openadr.Duration{Hours: 1}, RandomizeStart: &openadr.Duration{Minutes: 5}},
		Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.17), openadr.Number(-3), openadr.Number(1e21)}}}},
			{ID: -7, IntervalPeriod: &openadr.IntervalPeriod{Start: &later, Duration: &openadr.Duration{Negative: true, Weeks: 2}},
				Payloads: []openadr.ValuesMap{{Type: "X-MIXED", Values: []openadr.Value{openadr.String(""), openadr.String("text"), openadr.Bool(false), openadr.Bool(true), openadr.PointValue(0, 0)}}}},
			{ID: 2147483647, Payloads: []openadr.ValuesMap{}},
		},
	}
	report := openadr.Report{
		Metadata: md("r1"), ClientID: "ven-1", EventID: "e1", ClientName: "meter", Name: "usage",
		PayloadDescriptors: []openadr.ReportPayloadDescriptor{{PayloadType: "USAGE", Units: "KWH"}},
		Resources: []openadr.ReportResource{
			{ResourceName: openadr.AggregatedReport, IntervalPeriod: &openadr.IntervalPeriod{Start: &at, Duration: &openadr.Duration{Hours: 1}},
				Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(4.2)}}}}}},
			{ResourceName: "battery", Intervals: []openadr.Interval{}},
		},
	}
	ven := openadr.VEN{Metadata: md("v1"), ClientID: "ven-1", Name: "site-1", Targets: []string{"group-a"},
		Attributes: []openadr.ValuesMap{{Type: "X-NOTE", Values: []openadr.Value{openadr.String("north")}}}}
	resource := openadr.Resource{Metadata: md("x1"), ClientID: "ven-1", VENID: "v1", Name: "battery", Targets: []string{"group-b"},
		Attributes: []openadr.ValuesMap{{Type: "CAPACITY", Values: []openadr.Value{openadr.Number(13.5)}}}}
	group := openadr.ResourceGroup{Metadata: md("g1"), Name: "street", Targets: []string{"feeder-7"},
		Attributes: []openadr.ValuesMap{{Type: "X-NOTE", Values: []openadr.Value{openadr.Bool(true)}}},
		Children:   []openadr.ResourceGroupChild{{Type: openadr.ChildResource, ID: "x1"}, {Type: openadr.ChildResourceGroup, ID: "g1"}}}
	subscription := storage.SubscriptionRecord{ReadsAll: true, Subscription: openadr.Subscription{
		Metadata: md("s1"), ClientID: "bl", ClientName: "ops", ProgramID: "p1", Targets: []string{"group-a"},
		ObjectOperations: []openadr.ObjectOperation{
			{Objects: []openadr.ObjectType{openadr.ObjectEvent, openadr.ObjectReport}, Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationDelete},
				CallbackURL: "https://example.com/hook?token=1", BearerToken: "secret"},
			{Objects: []openadr.ObjectType{openadr.ObjectProgram}, Operations: []openadr.Operation{openadr.OperationUpdate}, Mechanism: openadr.MechanismWebSocket},
		},
	}}
	// A second of each, with nothing but what is required.
	bare := openadr.Program{Metadata: md("p2"), Name: "bare"}
	bareEvent := openadr.Event{Metadata: md("e2"), ProgramID: "p2"}
	bareReport := openadr.Report{Metadata: md("r2"), EventID: "e2", ClientName: "c", Resources: []openadr.ReportResource{}}

	must(t, "program", s.Programs().Create(ctx, program))
	must(t, "bare program", s.Programs().Create(ctx, bare))
	must(t, "event", s.Events().Create(ctx, event))
	must(t, "bare event", s.Events().Create(ctx, bareEvent))
	must(t, "report", s.Reports().Create(ctx, report))
	must(t, "bare report", s.Reports().Create(ctx, bareReport))
	must(t, "ven", s.VENs().Create(ctx, ven))
	must(t, "resource", s.Resources().Create(ctx, resource))
	must(t, "resource group", s.ResourceGroups().Create(ctx, group))
	must(t, "subscription", s.Subscriptions().Create(ctx, subscription))

	check := func(when string, s storage.Store, bareEventGone bool) {
		t.Helper()
		same(t, when, "program", program)(s.Programs().Get(ctx, "p1"))
		same(t, when, "bare program", bare)(s.Programs().Get(ctx, "p2"))
		same(t, when, "event", event)(s.Events().Get(ctx, "e1"))
		if !bareEventGone {
			same(t, when, "bare event", bareEvent)(s.Events().Get(ctx, "e2"))
		}
		same(t, when, "report", report)(s.Reports().Get(ctx, "r1"))
		same(t, when, "bare report", bareReport)(s.Reports().Get(ctx, "r2"))
		same(t, when, "ven", ven)(s.VENs().Get(ctx, "v1"))
		same(t, when, "resource", resource)(s.Resources().Get(ctx, "x1"))
		same(t, when, "resource group", group)(s.ResourceGroups().Get(ctx, "g1"))
		same(t, when, "subscription", subscription)(s.Subscriptions().Get(ctx, "s1"))
		// The same through a search.
		if list, err := s.Programs().List(ctx, storage.ProgramFilter{}); err != nil || len(list) != 2 {
			t.Errorf("%s: %d programs, %v", when, len(list), err)
		} else {
			same(t, when, "program in a list", program)(list[0], nil)
		}
	}
	check("as stored", s, false)
	if cfg.Reopen == nil {
		return
	}
	// A report whose event is gone has to survive a restart too.
	if _, err := s.Events().Delete(ctx, "e2"); err != nil {
		t.Fatal(err)
	}
	s = cfg.Reopen(t, s)
	check("after a restart", s, true)
	_, err := s.Events().Get(ctx, "e2")
	notFound(t, "after a restart: an event that was deleted", err)
	// And the rules hold on what was restored.
	conflict(t, "after a restart: a program with a name in use", s.Programs().Create(ctx, openadr.Program{Metadata: meta("p9"), Name: "bare"}))
	_, err = s.Programs().Delete(ctx, "p1")
	conflict(t, "after a restart: deleting a program that has events", err)
	must(t, "after a restart: a new event", s.Events().Create(ctx, openadr.Event{Metadata: meta("e3"), ProgramID: "p2"}))
	if list, _ := s.Events().List(ctx, storage.EventFilter{}); len(list) != 2 || list[1].ID != "e3" {
		t.Errorf("after a restart: a new event is not listed last: %+v", list)
	}
}

// same returns a function that compares what a store returned with what it
// was given.
func same[T any](t *testing.T, when, what string, want T) func(T, error) {
	t.Helper()
	return func(got T, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("%s: %s: %v", when, what, err)
			return
		}
		if !reflect.DeepEqual(normal(got), normal(want)) {
			t.Errorf("%s: the %s came back changed:\n got %+v\nwant %+v", when, what, got, want)
		}
	}
}

// normal makes two values comparable that differ only in what a store need
// not keep: an empty list and no list, and the location of a time.
func normal(v any) any {
	return normalize(reflect.ValueOf(v)).Interface()
}

var timeType = reflect.TypeOf(time.Time{})

func normalize(v reflect.Value) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == timeType {
			out.Set(reflect.ValueOf(v.Interface().(time.Time).UTC().Round(0)))
			return out
		}
		if v.NumField() > 0 && !v.Type().Field(0).IsExported() {
			// A value with fields of its own keeping, such as openadr.Value.
			out.Set(v)
			return out
		}
		for i := range v.NumField() {
			out.Field(i).Set(normalize(v.Field(i)))
		}
	case reflect.Slice:
		if v.Len() == 0 {
			return out // nil, whether it was empty or nil
		}
		out.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
		for i := range v.Len() {
			out.Index(i).Set(normalize(v.Index(i)))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			out.Set(reflect.New(v.Type().Elem()))
			out.Elem().Set(normalize(v.Elem()))
		}
	case reflect.Interface:
		if !v.IsNil() {
			out.Set(normalize(v.Elem()))
		}
	default:
		out.Set(v)
	}
	return out
}
