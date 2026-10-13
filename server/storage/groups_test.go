// SPDX-License-Identifier: MIT

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

func group(id, name string, children ...openadr.ResourceGroupChild) openadr.ResourceGroup {
	return openadr.ResourceGroup{Metadata: meta(id), Name: name, Children: children}
}

func resourceChild(id string) openadr.ResourceGroupChild {
	return openadr.ResourceGroupChild{Type: openadr.ChildResource, ID: id}
}

func groupChild(id string) openadr.ResourceGroupChild {
	return openadr.ResourceGroupChild{Type: openadr.ChildResourceGroup, ID: id}
}

// seedGroups fills a store with a ven, two resources and three groups: a
// holds r1 and b, b holds r2 and itself, c holds a and b.
func seedGroups(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.VENs().Create(ctx, openadr.VEN{Metadata: meta("v1"), Name: "site", ClientID: "ven"}))
	must(s.Resources().Create(ctx, openadr.Resource{Metadata: meta("r1"), VENID: "v1", Name: "one"}))
	must(s.Resources().Create(ctx, openadr.Resource{Metadata: meta("r2"), VENID: "v1", Name: "two"}))
	must(s.ResourceGroups().Create(ctx, group("a", "A", resourceChild("r1"))))
	must(s.ResourceGroups().Create(ctx, group("b", "B", resourceChild("r2"), groupChild("b"))))
	must(s.ResourceGroups().Update(ctx, group("a", "A", resourceChild("r1"), groupChild("b"))))
	must(s.ResourceGroups().Create(ctx, group("c", "C", groupChild("a"), groupChild("b"))))
}

func children(t *testing.T, s Store, id string) []openadr.ResourceGroupChild {
	t.Helper()
	g, err := s.ResourceGroups().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return g.Children
}

func testGroupRules(t *testing.T, s Store) {
	ctx := context.Background()
	seedGroups(t, s)
	groups := s.ResourceGroups()

	// Children exist, and are of a kind.
	for what, g := range map[string]openadr.ResourceGroup{
		"a resource that does not exist": group("x", "X", resourceChild("nope")),
		"a group that does not exist":    group("x", "X", groupChild("nope")),
		"a group as a resource":          group("x", "X", resourceChild("a")),
		"a resource as a group":          group("x", "X", groupChild("r1")),
		"an unknown kind":                group("x", "X", openadr.ResourceGroupChild{Type: "device", ID: "r1"}),
		"an ID in use":                   group("a", "again"),
	} {
		if err := groups.Create(ctx, g); !errors.Is(err, openadr.ErrConflict) {
			t.Errorf("%s: %v, want a conflict", what, err)
		}
	}
	if err := groups.Update(ctx, group("a", "A", resourceChild("nope"))); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("an update with a missing child: %v", err)
	}
	if err := groups.Update(ctx, group("nope", "X")); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("an update of nothing: %v", err)
	}
	// Names are not unique, as in OpenLEADR.
	if err := groups.Create(ctx, group("a2", "A")); err != nil {
		t.Errorf("a second group of the same name: %v", err)
	}
	if list, err := groups.List(ctx, ResourceGroupFilter{Name: "A"}); err != nil || len(list) != 2 || list[0].ID != "a" {
		t.Errorf("by name: %+v, %v", list, err)
	}
	if list, _ := groups.List(ctx, ResourceGroupFilter{}); len(list) != 4 {
		t.Errorf("all: %d groups", len(list))
	}

	// A value handed out before a change is not changed under its holder.
	held := children(t, s, "a")
	// What is deleted leaves the groups it was in.
	if _, err := s.Resources().Delete(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := children(t, s, "a"); !reflect.DeepEqual(got, []openadr.ResourceGroupChild{groupChild("b")}) {
		t.Errorf("a after its resource was deleted: %+v", got)
	}
	if !reflect.DeepEqual(held, []openadr.ResourceGroupChild{resourceChild("r1"), groupChild("b")}) {
		t.Errorf("a value read earlier was changed: %+v", held)
	}
	if _, err := groups.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if got := children(t, s, "a"); len(got) != 0 {
		t.Errorf("a after b was deleted: %+v", got)
	}
	if got := children(t, s, "c"); !reflect.DeepEqual(got, []openadr.ResourceGroupChild{groupChild("a")}) {
		t.Errorf("c after b was deleted: %+v", got)
	}
	if _, err := groups.Delete(ctx, "b"); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	// The resource a deleted group held is still there, and its ven can
	// still not be deleted for it.
	if _, err := s.VENs().Delete(ctx, "v1"); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("deleting the ven of a resource: %v", err)
	}
}

func TestMemoryResourceGroups(t *testing.T) {
	testGroupRules(t, NewMemory())
}

func TestFileResourceGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	testGroupRules(t, openFile(t, path))

	// A journal and its snapshot both restore groups that hold each other
	// and themselves, and what was dropped from them stays dropped.
	path = filepath.Join(t.TempDir(), "journal")
	first := openFile(t, path)
	seedGroups(t, first)
	if _, err := first.Resources().Delete(context.Background(), "r2"); err != nil {
		t.Fatal(err)
	}
	want, _ := first.ResourceGroups().List(context.Background(), ResourceGroupFilter{})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		again := openFile(t, path)
		got, err := again.ResourceGroups().List(context.Background(), ResourceGroupFilter{})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d:\n got %+v, %v\nwant %+v", round, got, err, want)
		}
		if err := again.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
