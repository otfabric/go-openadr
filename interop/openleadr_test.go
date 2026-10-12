//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
)

type peer struct {
	bl        *client.BL
	ven, ven2 *client.VEN
	// unregistered is a VEN client that has no ven object yet. OpenLEADR
	// allows one ven object per client.
	unregistered *client.VEN
	// venAsBL sends Business Logic requests with the VEN's credentials.
	venAsBL *client.BL
	// url is the VTN's API; venID and unregisteredID are OAuth2 client IDs.
	url, venID, unregisteredID string
	wrongSecret                client.Config
	// nonce makes the names a test creates distinct from the fixture's.
	nonce string
}

// openleadr starts the OpenLEADR reference VTN with the baseline fixture
// and returns clients for the fixture's OAuth2 clients.
func openleadr(t *testing.T) peer {
	t.Helper()
	url := startServer(t, "openleadr", "baseline")
	fx := readFixture(t, image("openleadr"), "baseline")
	cfg := func(name string) client.Config {
		id, secret := fx.credentials(t, name)
		return client.Config{BaseURL: url, Credentials: &client.Credentials{ClientID: id, ClientSecret: secret}}
	}
	p := peer{url: url, nonce: fmt.Sprintf("%x", time.Now().UnixNano())}
	p.venID, _ = fx.credentials(t, "ven-1")
	p.unregisteredID, _ = fx.credentials(t, "ven-3")
	p.wrongSecret = client.Config{BaseURL: url, Credentials: &client.Credentials{ClientID: p.venID, ClientSecret: "not-the-secret"}}
	var err error
	if p.bl, err = client.NewBL(cfg("bl")); err != nil {
		t.Fatal(err)
	}
	if p.ven, err = client.NewVEN(cfg("ven-1")); err != nil {
		t.Fatal(err)
	}
	if p.ven2, err = client.NewVEN(cfg("ven-2")); err != nil {
		t.Fatal(err)
	}
	if p.unregistered, err = client.NewVEN(cfg("ven-3")); err != nil {
		t.Fatal(err)
	}
	if p.venAsBL, err = client.NewBL(cfg("ven-1")); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p peer) name(base string) string { return base + "-" + p.nonce }

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// program creates a program that is deleted when the test ends.
func (p peer) program(t *testing.T, prog openadr.Program) openadr.Program {
	t.Helper()
	out, err := p.bl.CreateProgram(ctx(t), prog)
	if err != nil {
		t.Fatalf("CreateProgram: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteProgram(context.Background(), out.ID) })
	return out
}

func (p peer) event(t *testing.T, e openadr.Event) openadr.Event {
	t.Helper()
	out, err := p.bl.CreateEvent(ctx(t), e)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteEvent(context.Background(), out.ID) })
	return out
}

// The first end-to-end workflow of the plan, with OpenLEADR as the VTN.
func TestOpenLEADRWorkflow(t *testing.T) {
	p := openleadr(t)
	c := ctx(t)
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	hour := openadr.Duration{Hours: 1}

	program := p.program(t, openadr.Program{
		Name: p.name("workflow"),
		PayloadDescriptors: []openadr.PayloadDescriptor{
			openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"},
		},
	})
	if program.ID == "" || program.Created.IsZero() || program.Name != p.name("workflow") || len(program.PayloadDescriptors) != 1 {
		t.Fatalf("program as created = %+v", program)
	}
	descriptor := openadr.NewReportDescriptor("USAGE")
	event := p.event(t, openadr.Event{
		ProgramID:         program.ID,
		Name:              "prices",
		Priority:          new(int),
		IntervalPeriod:    &openadr.IntervalPeriod{Start: &start, Duration: &hour},
		ReportDescriptors: []openadr.ReportDescriptor{descriptor},
		Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.17)}}}},
			{ID: 1, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.21), openadr.String("peak"), openadr.Bool(true), openadr.PointValue(1, 2)}}}},
		},
	})

	// The VEN discovers the program among everything the VTN holds.
	var found bool
	for prog, err := range client.Each(c, func(c context.Context, pg client.Page) ([]openadr.Program, error) {
		return p.ven.Programs(c, client.ProgramQuery{Page: pg})
	}) {
		if err != nil {
			t.Fatalf("VEN Programs: %v", err)
		}
		found = found || prog.ID == program.ID
	}
	if !found {
		t.Fatalf("the VEN does not find program %s", program.ID)
	}

	events, err := p.ven.Events(c, client.EventQuery{ProgramID: program.ID})
	if err != nil || len(events) != 1 {
		t.Fatalf("VEN Events = %+v, %v", events, err)
	}
	got := events[0]
	if got.ID != event.ID || got.Name != "prices" || got.Priority == nil || *got.Priority != 0 {
		t.Errorf("event = %+v", got)
	}
	if got.IntervalPeriod == nil || got.IntervalPeriod.Start == nil || !got.IntervalPeriod.Start.Equal(start) ||
		got.IntervalPeriod.Duration == nil || *got.IntervalPeriod.Duration != hour {
		t.Errorf("event intervalPeriod = %+v", got.IntervalPeriod)
	}
	if len(got.Intervals) != 2 || got.Intervals[1].ID != 1 ||
		!slices.Equal(got.Intervals[1].Payloads[0].Values, event.Intervals[1].Payloads[0].Values) {
		t.Errorf("event intervals = %+v", got.Intervals)
	}
	if n, _ := got.Intervals[0].Payloads[0].Values[0].Number(); n != 0.17 {
		t.Errorf("price of interval 0 = %v", n)
	}
	if len(got.ReportDescriptors) != 1 || got.ReportDescriptors[0].PayloadType != "USAGE" || got.ReportDescriptors[0].Repeat != descriptor.Repeat {
		t.Errorf("report descriptors = %+v", got.ReportDescriptors)
	}

	report, err := p.ven.CreateReport(c, openadr.Report{
		EventID:    event.ID,
		ClientName: p.name("ven"),
		Name:       "usage",
		Resources: []openadr.ReportResource{{
			ResourceName: "meter-1",
			Intervals:    []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(3.5)}}}}},
		}},
	})
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	t.Cleanup(func() {
		if _, err := p.ven.DeleteReport(context.Background(), report.ID); err != nil {
			t.Errorf("the VEN could not delete its report: %v", err)
		}
	})
	if report.ClientID != p.venID {
		t.Errorf("report.ClientID = %q, want %q", report.ClientID, p.venID)
	}

	reports, err := p.bl.Reports(c, client.ReportQuery{ProgramID: program.ID})
	if err != nil || len(reports) != 1 || reports[0].ID != report.ID {
		t.Fatalf("BL Reports = %+v, %v", reports, err)
	}
	if n, _ := reports[0].Resources[0].Intervals[0].Payloads[0].Values[0].Number(); n != 3.5 || reports[0].Name != "usage" {
		t.Errorf("report read by Business Logic = %+v", reports[0])
	}

	// A VEN updates its own report; the other VEN cannot see it.
	report.Name = "usage-corrected"
	if updated, err := p.ven.UpdateReport(c, report); err != nil || updated.Name != "usage-corrected" {
		t.Errorf("UpdateReport = %+v, %v", updated, err)
	}
	if _, err := p.ven2.Report(c, report.ID); !errors.Is(err, openadr.ErrNotFound) && !errors.Is(err, openadr.ErrForbidden) {
		t.Errorf("the other VEN reading the report: %v", err)
	}
}

func TestOpenLEADRErrors(t *testing.T) {
	p := openleadr(t)
	c := ctx(t)
	program := p.program(t, openadr.Program{Name: p.name("errors")})

	_, err := p.bl.Program(c, "no-such-program-"+p.nonce)
	var apiErr *openadr.Error
	if !errors.Is(err, openadr.ErrNotFound) || !errors.As(err, &apiErr) || apiErr.Problem.Status != 404 {
		t.Errorf("unknown program: %v; want ErrNotFound with a decoded problem", err)
	}
	if _, err := p.bl.CreateProgram(c, openadr.Program{Name: program.Name}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("duplicate programName: %v, want ErrConflict", err)
	}
	if _, err := p.venAsBL.CreateProgram(c, openadr.Program{Name: p.name("by-ven")}); !errors.Is(err, openadr.ErrForbidden) {
		t.Errorf("a VEN creating a program: %v, want ErrForbidden", err)
	}
	renamed := program
	renamed.Name = p.name("errors-renamed")
	if updated, err := p.bl.UpdateProgram(c, renamed); err != nil || updated.Name != renamed.Name || updated.ID != program.ID {
		t.Errorf("UpdateProgram = %+v, %v", updated, err)
	}

	bad, err := client.NewVEN(p.wrongSecret)
	if err != nil {
		t.Fatal(err)
	}
	var tokenErr *client.TokenError
	if _, err := bad.Programs(c, client.ProgramQuery{}); !errors.As(err, &tokenErr) {
		t.Errorf("wrong client secret: %v, want a TokenError", err)
	}
}

func TestOpenLEADRVENsResourcesAndTargets(t *testing.T) {
	p := openleadr(t)
	c := ctx(t)

	// The fixture's objects, as each client may see them.
	if list, err := p.bl.VENs(c, client.VENQuery{}); err != nil || len(list) != 2 {
		t.Errorf("BL VENs = %+v, %v; want the two vens of the fixture", list, err)
	}
	if list, err := p.ven.VENs(c, client.VENQuery{}); err != nil || len(list) != 1 || list[0].Name != "site-1" ||
		!slices.Equal(list[0].Targets, []string{"group-a"}) {
		t.Errorf("ven-1 VENs = %+v, %v; want its own ven site-1 with the target group-a", list, err)
	}
	if list, err := p.ven.Programs(c, client.ProgramQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 1 || list[0].Name != "group-a-flex" {
		t.Errorf("ven-1 programs for group-a = %+v, %v", list, err)
	}
	if list, err := p.ven2.Events(c, client.EventQuery{}); err != nil || len(list) != 1 || list[0].Name != "day-ahead-prices" {
		t.Errorf("ven-2 events = %+v, %v; want only the untargeted event", list, err)
	}

	// A VEN client registers itself and a resource.
	own, err := p.unregistered.CreateVEN(c, openadr.VEN{Name: p.name("site")})
	if err != nil {
		t.Fatalf("VEN CreateVEN: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteVEN(context.Background(), own.ID) })
	if own.ID == "" || own.ClientID != p.unregisteredID || own.Name != p.name("site") {
		t.Errorf("ven = %+v", own)
	}
	res, err := p.unregistered.CreateResource(c, openadr.Resource{VENID: own.ID, Name: "battery"})
	if err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteResource(context.Background(), res.ID) })
	if res.VENID != own.ID || res.Name != "battery" {
		t.Errorf("resource = %+v", res)
	}
	if list, err := p.unregistered.Resources(c, client.ResourceQuery{VENID: own.ID}); err != nil || len(list) != 1 || list[0].ID != res.ID {
		t.Errorf("VEN Resources = %+v, %v", list, err)
	}
	if list, err := p.ven2.VENs(c, client.VENQuery{}); err != nil || slices.ContainsFunc(list, func(v openadr.VEN) bool { return v.ID == own.ID }) {
		t.Errorf("the other VEN's VENs = %+v, %v", list, err)
	}

	// Business Logic grants the VEN a target through its ven object.
	target := p.name("group")
	own.Targets = []string{target}
	granted, err := p.bl.UpdateVEN(c, own)
	if err != nil || !slices.Equal(granted.Targets, []string{target}) {
		t.Fatalf("BL UpdateVEN = %+v, %v", granted, err)
	}
	program := p.program(t, openadr.Program{Name: p.name("targeted"), Targets: []string{target}})

	has := func(list []openadr.Program) bool {
		return slices.ContainsFunc(list, func(pr openadr.Program) bool { return pr.ID == program.ID })
	}
	if list, err := p.unregistered.Programs(c, client.ProgramQuery{Targets: []string{target}}); err != nil || !has(list) {
		t.Errorf("the granted VEN naming its target does not see the program: %v", err)
	}
	if list, err := p.ven2.Programs(c, client.ProgramQuery{Targets: []string{target}}); err != nil || has(list) {
		t.Errorf("a VEN without the grant sees the targeted program (err %v)", err)
	}
}
