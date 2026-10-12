// SPDX-License-Identifier: MIT

package openadr_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/internal/vtntest"
)

func ptr[T any](v T) *T { return &v }

func mustProgram(t *testing.T, bl *client.BL, p openadr.Program) openadr.Program {
	t.Helper()
	out, err := bl.CreateProgram(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateProgram(%q): %v", p.Name, err)
	}
	return out
}

func mustEvent(t *testing.T, bl *client.BL, e openadr.Event) openadr.Event {
	t.Helper()
	out, err := bl.CreateEvent(context.Background(), e)
	if err != nil {
		t.Fatalf("CreateEvent(%q): %v", e.Name, err)
	}
	return out
}

// The first end-to-end workflow of the plan: Business Logic creates a
// program and an event, a VEN discovers both and reports, Business Logic
// reads the report.
func TestProgramEventReportWorkflow(t *testing.T) {
	ctx := context.Background()
	vtn := vtntest.Start(t)

	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	program := mustProgram(t, vtn.BL, openadr.Program{
		Name: "ResTOU",
		PayloadDescriptors: []openadr.PayloadDescriptor{
			openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"},
			openadr.ReportPayloadDescriptor{PayloadType: "USAGE", Units: "KWH", ReadingType: "DIRECT_READ"},
		},
	})
	if program.ID == "" || program.Created.IsZero() || !program.Created.Equal(program.Modified) {
		t.Fatalf("program metadata not set by the VTN: %+v", program.Metadata)
	}

	event := mustEvent(t, vtn.BL, openadr.Event{
		ProgramID:         program.ID,
		Name:              "prices",
		Priority:          ptr(1),
		ReportDescriptors: []openadr.ReportDescriptor{openadr.NewReportDescriptor("USAGE")},
		IntervalPeriod:    &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Hours: 1}},
		Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.17)}}}},
			{ID: 1, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.21)}}}},
		},
	})

	programs, err := vtn.VEN1.Programs(ctx, client.ProgramQuery{})
	if err != nil || len(programs) != 1 || programs[0].ID != program.ID {
		t.Fatalf("VEN Programs = %+v, %v", programs, err)
	}
	if !reflect.DeepEqual(programs[0], program) {
		t.Errorf("program read by the VEN differs from the one created:\n got %+v\nwant %+v", programs[0], program)
	}
	events, err := vtn.VEN1.Events(ctx, client.EventQuery{ProgramID: program.ID})
	if err != nil || len(events) != 1 {
		t.Fatalf("VEN Events = %+v, %v", events, err)
	}
	if !reflect.DeepEqual(events[0], event) {
		t.Errorf("event read by the VEN differs from the one created:\n got %+v\nwant %+v", events[0], event)
	}

	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{
		EventID:    event.ID,
		ClientName: "ven-one",
		Name:       "usage",
		Resources: []openadr.ReportResource{{
			ResourceName:   "meter-1",
			IntervalPeriod: &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Hours: 1}},
			Intervals: []openadr.Interval{
				{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(3.5)}}}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	if report.ClientID != "ven-1" {
		t.Errorf("report.ClientID = %q, want the client ID of the token", report.ClientID)
	}

	got, err := vtn.BL.Reports(ctx, client.ReportQuery{ProgramID: program.ID})
	if err != nil || len(got) != 1 {
		t.Fatalf("BL Reports = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(got[0], report) {
		t.Errorf("report read by Business Logic differs:\n got %+v\nwant %+v", got[0], report)
	}
	if v, _ := got[0].Resources[0].Intervals[0].Payloads[0].Values[0].Number(); v != 3.5 {
		t.Errorf("reported usage = %v, want 3.5", v)
	}
}

func TestRolesAndOwnership(t *testing.T) {
	ctx := context.Background()
	vtn := vtntest.Start(t)
	program := mustProgram(t, vtn.BL, openadr.Program{Name: "p"})
	event := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID})

	t.Run("a VEN cannot write programs or events", func(t *testing.T) {
		// The VEN client has no such methods; use the BL API with VEN credentials.
		asVEN, err := client.NewBL(client.Config{
			BaseURL:     vtn.URL,
			Credentials: &client.Credentials{ClientID: "ven-1", ClientSecret: "ven-1-secret"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := asVEN.CreateProgram(ctx, openadr.Program{Name: "x"}); !errors.Is(err, openadr.ErrForbidden) {
			t.Errorf("CreateProgram as VEN: %v, want ErrForbidden", err)
		}
		if _, err := asVEN.DeleteEvent(ctx, event.ID); !errors.Is(err, openadr.ErrForbidden) {
			t.Errorf("DeleteEvent as VEN: %v, want ErrForbidden", err)
		}
		// A BL_VEN_REQUEST from a VEN would let it choose its own targets.
		if _, err := asVEN.CreateVEN(ctx, openadr.VEN{Name: "v", ClientID: "ven-2", Targets: []string{"a"}}); !errors.Is(err, openadr.ErrForbidden) {
			t.Errorf("BL_VEN_REQUEST as VEN: %v, want ErrForbidden", err)
		}
	})

	t.Run("wrong credentials", func(t *testing.T) {
		bad, err := client.NewVEN(client.Config{
			BaseURL:     vtn.URL,
			Credentials: &client.Credentials{ClientID: "ven-1", ClientSecret: "nope"},
		})
		if err != nil {
			t.Fatal(err)
		}
		var tokenErr *client.TokenError
		if _, err := bad.Programs(ctx, client.ProgramQuery{}); !errors.As(err, &tokenErr) || tokenErr.Code != "invalid_client" {
			t.Errorf("Programs with a wrong secret: %v, want a TokenError invalid_client", err)
		}
		for name, cfg := range map[string]client.Config{
			"no token":      {BaseURL: vtn.URL},
			"unknown token": {BaseURL: vtn.URL, Tokens: client.StaticToken("made-up")},
		} {
			anon, err := client.NewVEN(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := anon.Programs(ctx, client.ProgramQuery{}); !errors.Is(err, openadr.ErrUnauthorized) {
				t.Errorf("%s: %v, want ErrUnauthorized", name, err)
			}
		}
	})

	t.Run("reports belong to the VEN that wrote them", func(t *testing.T) {
		rep, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "one", Resources: []openadr.ReportResource{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := vtn.VEN2.Report(ctx, rep.ID); !errors.Is(err, openadr.ErrNotFound) {
			t.Errorf("other VEN reading the report: %v, want ErrNotFound", err)
		}
		rep.Name = "stolen"
		if _, err := vtn.VEN2.UpdateReport(ctx, rep); !errors.Is(err, openadr.ErrNotFound) {
			t.Errorf("other VEN updating the report: %v, want ErrNotFound", err)
		}
		if _, err := vtn.VEN2.DeleteReport(ctx, rep.ID); !errors.Is(err, openadr.ErrNotFound) {
			t.Errorf("other VEN deleting the report: %v, want ErrNotFound", err)
		}
		if list, err := vtn.VEN2.Reports(ctx, client.ReportQuery{}); err != nil || len(list) != 0 {
			t.Errorf("other VEN listing reports = %v, %v; want none", list, err)
		}
		rep.Name = "renamed"
		updated, err := vtn.VEN1.UpdateReport(ctx, rep)
		if err != nil || updated.Name != "renamed" || updated.ClientID != "ven-1" || !updated.Created.Equal(rep.Created) {
			t.Errorf("owner updating the report = %+v, %v", updated, err)
		}
		if _, err := vtn.BL.Report(ctx, rep.ID); err != nil {
			t.Errorf("Business Logic reading the report: %v", err)
		}
		if _, err := vtn.VEN1.DeleteReport(ctx, rep.ID); err != nil {
			t.Errorf("owner deleting the report: %v", err)
		}
	})

	t.Run("a report needs an existing event", func(t *testing.T) {
		_, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: "no-such-event", ClientName: "one"})
		if !errors.Is(err, openadr.ErrConflict) {
			t.Errorf("CreateReport for a missing event: %v, want ErrConflict", err)
		}
	})
}

func TestVENsAndResources(t *testing.T) {
	ctx := context.Background()
	vtn := vtntest.Start(t)

	own, err := vtn.VEN1.CreateVEN(ctx, openadr.VEN{Name: "site-1", Targets: []string{"self-granted"}})
	if err != nil {
		t.Fatal(err)
	}
	if own.ClientID != "ven-1" || own.Targets != nil {
		t.Errorf("VEN-created ven = %+v; want clientID ven-1 and no targets", own)
	}
	if _, err := vtn.VEN2.CreateVEN(ctx, openadr.VEN{Name: "site-1"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("duplicate venName: %v, want ErrConflict", err)
	}
	if _, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-2"}); err == nil {
		t.Error("BL CreateVEN without clientID succeeded")
	}

	// Business Logic grants a target; a later update by the VEN keeps it.
	own.Targets = []string{"group-a"}
	if own, err = vtn.BL.UpdateVEN(ctx, own); err != nil || !slices.Equal(own.Targets, []string{"group-a"}) {
		t.Fatalf("BL UpdateVEN = %+v, %v", own, err)
	}
	own.Name, own.Targets = "site-one", nil
	if own, err = vtn.VEN1.UpdateVEN(ctx, own); err != nil || own.Name != "site-one" || !slices.Equal(own.Targets, []string{"group-a"}) {
		t.Fatalf("VEN UpdateVEN = %+v, %v; want the granted targets kept", own, err)
	}

	res, err := vtn.VEN1.CreateResource(ctx, openadr.Resource{VENID: own.ID, Name: "battery"})
	if err != nil || res.ClientID != "ven-1" {
		t.Fatalf("CreateResource = %+v, %v", res, err)
	}
	if _, err := vtn.VEN1.CreateResource(ctx, openadr.Resource{VENID: own.ID, Name: "battery"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("duplicate resourceName: %v, want ErrConflict", err)
	}
	if _, err := vtn.VEN2.CreateResource(ctx, openadr.Resource{VENID: own.ID, Name: "intruder"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("resource on another client's ven: %v, want ErrConflict", err)
	}
	if list, err := vtn.VEN2.VENs(ctx, client.VENQuery{}); err != nil || len(list) != 0 {
		t.Errorf("VEN2 sees vens %v, %v; want none", list, err)
	}
	if list, err := vtn.BL.Resources(ctx, client.ResourceQuery{VENID: own.ID}); err != nil || len(list) != 1 {
		t.Errorf("BL Resources = %v, %v; want one", list, err)
	}
	if _, err := vtn.VEN1.DeleteVEN(ctx, own.ID); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("deleting a ven that has resources: %v, want ErrConflict", err)
	}
	if _, err := vtn.VEN1.DeleteResource(ctx, res.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.VEN1.DeleteVEN(ctx, own.ID); err != nil {
		t.Errorf("DeleteVEN: %v", err)
	}
}

func TestTargeting(t *testing.T) {
	ctx := context.Background()
	vtn := vtntest.Start(t)

	open := mustProgram(t, vtn.BL, openadr.Program{Name: "open"})
	groupA := mustProgram(t, vtn.BL, openadr.Program{Name: "a", Targets: []string{"group-a", "group-b"}})
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: groupA.ID, Name: "for-a", Targets: []string{"group-a"}})
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: open.ID, Name: "for-all"})

	names := func(ps []openadr.Program) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}

	// Without a grant, naming the target does not help.
	ps, err := vtn.VEN1.Programs(ctx, client.ProgramQuery{Targets: []string{"group-a"}})
	if err != nil || len(ps) != 0 {
		t.Errorf("ungranted VEN asking for group-a sees %v, %v", names(ps), err)
	}
	if _, err := vtn.VEN1.Program(ctx, groupA.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("ungranted VEN reading the targeted program: %v, want ErrNotFound", err)
	}

	// Business Logic grants group-a to ven-1 through its ven object.
	if _, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}

	ps, err = vtn.VEN1.Programs(ctx, client.ProgramQuery{})
	if err != nil || !slices.Equal(names(ps), []string{"open"}) {
		t.Errorf("granted VEN naming no targets sees %v, %v; want only the untargeted program", names(ps), err)
	}
	ps, err = vtn.VEN1.Programs(ctx, client.ProgramQuery{Targets: []string{"group-a", "group-b"}})
	if err != nil || !slices.Equal(names(ps), []string{"a"}) {
		t.Fatalf("granted VEN asking for its target sees %v, %v", names(ps), err)
	}
	if !slices.Equal(ps[0].Targets, []string{"group-a"}) {
		t.Errorf("targets shown = %v; group-b was not granted and must stay hidden", ps[0].Targets)
	}
	byID, err := vtn.VEN1.Program(ctx, groupA.ID)
	if err != nil || !slices.Equal(byID.Targets, []string{"group-a"}) {
		t.Errorf("granted VEN reading by ID = %+v, %v", byID.Targets, err)
	}
	es, err := vtn.VEN1.Events(ctx, client.EventQuery{Targets: []string{"group-a"}})
	if err != nil || len(es) != 1 || es[0].Name != "for-a" {
		t.Errorf("granted VEN events for group-a = %+v, %v", es, err)
	}
	// The other VEN was granted nothing.
	es, err = vtn.VEN2.Events(ctx, client.EventQuery{Targets: []string{"group-a"}})
	if err != nil || len(es) != 0 {
		t.Errorf("ungranted VEN events for group-a = %+v, %v", es, err)
	}
	// Business Logic sees everything, and all targets.
	ps, err = vtn.BL.Programs(ctx, client.ProgramQuery{Targets: []string{"group-b"}})
	if err != nil || len(ps) != 1 || len(ps[0].Targets) != 2 {
		t.Errorf("BL programs for group-b = %+v, %v", ps, err)
	}
}

func TestProgramLifecycleAndPaging(t *testing.T) {
	ctx := context.Background()
	vtn := vtntest.Start(t)

	const n = 120
	for i := range n {
		mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("program-%03d", i)})
	}
	if _, err := vtn.BL.CreateProgram(ctx, openadr.Program{Name: "program-000"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("duplicate programName: %v, want ErrConflict", err)
	}
	if _, err := vtn.BL.CreateProgram(ctx, openadr.Program{}); err == nil {
		t.Error("CreateProgram without a name succeeded")
	}

	first, err := vtn.BL.Programs(ctx, client.ProgramQuery{})
	if err != nil || len(first) != 50 {
		t.Fatalf("default page has %d programs, %v; want 50", len(first), err)
	}
	if _, err := vtn.BL.Programs(ctx, client.ProgramQuery{Page: client.Page{Limit: 51}}); !errors.Is(err, openadr.ErrBadRequest) {
		t.Errorf("limit 51: %v, want ErrBadRequest", err)
	}
	var seen []string
	for p, err := range client.Each(ctx, func(ctx context.Context, pg client.Page) ([]openadr.Program, error) {
		return vtn.BL.Programs(ctx, client.ProgramQuery{Page: pg})
	}) {
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, p.Name)
	}
	if len(seen) != n || seen[0] != "program-000" || seen[n-1] != fmt.Sprintf("program-%03d", n-1) {
		t.Errorf("Each returned %d programs (%v ... %v); want all %d in creation order", len(seen), seen[0], seen[len(seen)-1], n)
	}

	p := first[0]
	event := mustEvent(t, vtn.BL, openadr.Event{ProgramID: p.ID})
	if _, err := vtn.BL.DeleteProgram(ctx, p.ID); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("deleting a program that has events: %v, want ErrConflict", err)
	}
	if _, err := vtn.BL.DeleteEvent(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	p.Name = "renamed"
	updated, err := vtn.BL.UpdateProgram(ctx, p)
	if err != nil || updated.Name != "renamed" || !updated.Created.Equal(p.Created) || updated.Modified.Before(p.Modified) {
		t.Errorf("UpdateProgram = %+v, %v", updated, err)
	}
	deleted, err := vtn.BL.DeleteProgram(ctx, p.ID)
	if err != nil || deleted.Name != "renamed" {
		t.Errorf("DeleteProgram = %+v, %v", deleted, err)
	}
	_, err = vtn.BL.Program(ctx, p.ID)
	var apiErr *openadr.Error
	if !errors.Is(err, openadr.ErrNotFound) || !errors.As(err, &apiErr) || apiErr.Problem.Status != 404 {
		t.Errorf("reading a deleted program: %v (problem %+v), want a 404 problem", err, apiErr)
	}
	if _, err := vtn.BL.CreateEvent(ctx, openadr.Event{ProgramID: p.ID}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("event for a deleted program: %v, want ErrConflict", err)
	}
}

func TestActiveEvents(t *testing.T) {
	ctx := context.Background()
	vtn := vtntest.Start(t)
	p := mustProgram(t, vtn.BL, openadr.Program{Name: "p"})
	past := time.Now().Add(-48 * time.Hour)
	future := time.Now().Add(48 * time.Hour)
	hour := &openadr.Duration{Hours: 1}
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: p.ID, Name: "over", IntervalPeriod: &openadr.IntervalPeriod{Start: &past, Duration: hour}})
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: p.ID, Name: "ahead", IntervalPeriod: &openadr.IntervalPeriod{Start: &future, Duration: hour}})
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: p.ID, Name: "forever", IntervalPeriod: &openadr.IntervalPeriod{Start: &past, Duration: &openadr.Infinite}})
	mustEvent(t, vtn.BL, openadr.Event{ProgramID: p.ID, Name: "untimed"})

	es, err := vtn.VEN1.Events(ctx, client.EventQuery{ActiveOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"ahead", "forever", "untimed"}) {
		t.Errorf("active events = %v", names)
	}
}
