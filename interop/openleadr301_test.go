//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
)

// The clients of this module as clients of OpenADR 3.0.1, against
// OpenLEADR v0.1.4, the last release of OpenLEADR for 3.0.

type peer301 struct {
	bl             *client.BL
	ven, ven2      *client.VEN
	url, nonce     string
	blID, blSecret string
}

func openleadr301(t *testing.T) peer301 {
	t.Helper()
	url := startServer(t, "openleadr301", "baseline-301")
	fx := readFixture(t, image("openleadr301"), "baseline-301")
	cfg := func(name string) client.Config {
		id, secret := fx.credentials(t, name)
		// No TokenURL: a 3.0.1 client finds the token endpoint at /auth/token.
		return client.Config{BaseURL: url, Version: openadr.Version301, Credentials: &client.Credentials{ClientID: id, ClientSecret: secret}}
	}
	p := peer301{url: url, nonce: fmt.Sprintf("%x", time.Now().UnixNano())}
	p.blID, p.blSecret = fx.credentials(t, "bl")
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
	return p
}

func (p peer301) name(base string) string { return base + "-" + p.nonce }

// deleteAsBL deletes an object as Business Logic, over plain HTTP. OpenLEADR
// v0.1.4 deletes a report only for a business user, where the specification
// gives that to VENs, and this module's BL client has no DeleteReport.
func (p peer301) deleteAsBL(ctx context.Context, path string) error {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {p.blID}, "client_secret": {p.blSecret}}
	resp, err := http.PostForm(p.url+"/auth/token", form)
	if err != nil {
		return err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.url+"/"+strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DELETE %s: %s", path, resp.Status)
	}
	return nil
}

var site1 = []openadr.ValuesMap{{Type: "VEN_NAME", Values: []openadr.Value{openadr.String("site-1")}}}

// The fixture as a 3.0.1 VTN holds it, and its access rules.
func TestOpenLEADR301Fixture(t *testing.T) {
	p := openleadr301(t)
	c := ctx(t)

	programs, err := p.bl.Programs(c, client.ProgramQuery{})
	if err != nil {
		t.Fatalf("BL Programs: %v", err)
	}
	byName := map[string]openadr.Program{}
	for _, prog := range programs {
		byName[prog.Name] = prog
	}
	dayAhead, flex := byName["day-ahead"], byName["group-a-flex"]
	if dayAhead.ID == "" || flex.ID == "" {
		t.Fatalf("programs = %+v", programs)
	}
	if len(dayAhead.PayloadDescriptors) != 1 || len(dayAhead.TypedTargets) != 0 {
		t.Errorf("day-ahead = %+v", dayAhead)
	}
	if len(flex.TypedTargets) != 1 || flex.TypedTargets[0].Type != "VEN_NAME" || flex.Targets != nil {
		t.Errorf("group-a-flex targets = %+v, %v", flex.TypedTargets, flex.Targets)
	}

	// The VEN named in the targets sees the program and its event; the
	// other does not.
	if _, err := p.ven.Program(c, flex.ID); err != nil {
		t.Errorf("ven-1 Program(group-a-flex): %v", err)
	}
	if _, err := p.ven2.Program(c, flex.ID); err == nil {
		t.Error("ven-2 reads the program targeted at site-1")
	}
	events, err := p.ven.Events(c, client.EventQuery{ProgramID: flex.ID})
	if err != nil || len(events) != 1 || events[0].Name != "group-a-limit" {
		t.Errorf("ven-1 Events(group-a-flex) = %+v, %v", events, err)
	}
	if events, err := p.ven2.Events(c, client.EventQuery{ProgramID: flex.ID}); err != nil || len(events) != 0 {
		t.Errorf("ven-2 Events(group-a-flex) = %+v, %v", events, err)
	}

	// Searches by typed targets. OpenLEADR v0.1.4 keeps a program's
	// VEN_NAME targets apart from its other targets (they say which VENs
	// see it) and searches only the others: a search by VEN_NAME finds
	// nothing, though the program shows the target.
	found, err := p.bl.Programs(c, client.ProgramQuery{TargetType: "VEN_NAME", Targets: []string{"site-1"}})
	if err != nil || len(found) != 0 {
		t.Errorf("Programs targeted VEN_NAME site-1 = %+v, %v; OpenLEADR v0.1.4 finds none", found, err)
	}
	byGroup, err := p.bl.VENs(c, client.VENQuery{TargetType: "GROUP", Targets: []string{"group-a"}})
	if err != nil || len(byGroup) != 1 || byGroup[0].Name != "site-1" {
		t.Errorf("VENs targeted GROUP group-a = %+v, %v", byGroup, err)
	}
	if none, err := p.bl.VENs(c, client.VENQuery{TargetType: "GROUP", Targets: []string{"group-b"}}); err != nil || len(none) != 0 {
		t.Errorf("VENs targeted GROUP group-b = %+v, %v", none, err)
	}

	// The VENs of the fixture, with targets of a type.
	vens, err := p.bl.VENs(c, client.VENQuery{Name: "site-1"})
	if err != nil || len(vens) != 1 || len(vens[0].TypedTargets) != 1 || vens[0].TypedTargets[0].Type != "GROUP" {
		t.Fatalf("VENs(site-1) = %+v, %v", vens, err)
	}
	resources, err := p.bl.Resources(c, client.ResourceQuery{VENID: vens[0].ID})
	if err != nil || len(resources) != 1 || resources[0].Name != "battery" {
		t.Errorf("Resources of site-1 = %+v, %v", resources, err)
	}
}

// The first end-to-end workflow of the plan, in OpenADR 3.0.1: Business
// Logic creates a program and an event, a VEN retrieves the event and
// reports on it, and Business Logic retrieves the report.
func TestOpenLEADR301Workflow(t *testing.T) {
	p := openleadr301(t)
	c := ctx(t)
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	hour := openadr.Duration{Hours: 1}

	// The program properties of 3.0.1 that 3.1.0 made attributes.
	attributes := []openadr.ValuesMap{
		{Type: "PROGRAM_LONG_NAME", Values: []openadr.Value{openadr.String("Workflow of the interop test")}},
		{Type: "RETAILER_NAME", Values: []openadr.Value{openadr.String("ACME")}},
		{Type: "COUNTRY", Values: []openadr.Value{openadr.String("NL")}},
		{Type: "LOCAL", Values: []openadr.Value{openadr.Bool(false)}},
	}
	program, err := p.bl.CreateProgram(c, openadr.Program{
		Name:               p.name("workflow"),
		Attributes:         attributes,
		TypedTargets:       site1,
		PayloadDescriptors: []openadr.PayloadDescriptor{openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"}},
	})
	if err != nil {
		t.Fatalf("CreateProgram: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteProgram(context.Background(), program.ID) })
	if program.ID == "" || program.Name != p.name("workflow") || len(program.Attributes) != len(attributes) {
		t.Errorf("program as created = %+v", program)
	}
	for _, want := range attributes {
		if !slices.ContainsFunc(program.Attributes, func(a openadr.ValuesMap) bool { return a.Type == want.Type && len(a.Values) == 1 }) {
			t.Errorf("attribute %s did not come back: %+v", want.Type, program.Attributes)
		}
	}

	descriptor := openadr.NewReportDescriptor("USAGE")
	event, err := p.bl.CreateEvent(c, openadr.Event{
		ProgramID:         program.ID,
		Name:              "prices",
		TypedTargets:      site1,
		IntervalPeriod:    &openadr.IntervalPeriod{Start: &start, Duration: &hour},
		ReportDescriptors: []openadr.ReportDescriptor{descriptor},
		Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.17)}}}},
			{ID: 1, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.21)}}}},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteEvent(context.Background(), event.ID) })

	got, err := p.ven.Event(c, event.ID)
	if err != nil || got.ProgramID != program.ID || len(got.Intervals) != 2 || len(got.ReportDescriptors) != 1 {
		t.Fatalf("VEN Event = %+v, %v", got, err)
	}
	if _, err := p.ven2.Event(c, event.ID); err == nil {
		t.Error("ven-2 reads the event targeted at site-1")
	}

	// The report names its program, which 3.0.1 requires: the client reads
	// it from the event.
	report, err := p.ven.CreateReport(c, openadr.Report{
		EventID: event.ID, ClientName: "site-1",
		Resources: []openadr.ReportResource{{ResourceName: "battery", Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(1.5)}}}},
		}}},
	})
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	t.Cleanup(func() { _ = p.deleteAsBL(context.Background(), "reports/"+report.ID) })
	if report.ProgramID != program.ID || report.EventID != event.ID {
		t.Errorf("report as created = %+v", report)
	}
	reports, err := p.bl.Reports(c, client.ReportQuery{EventID: event.ID})
	if err != nil || len(reports) != 1 || reports[0].ID != report.ID || len(reports[0].Resources) != 1 {
		t.Errorf("BL Reports = %+v, %v", reports, err)
	}

	// Update and delete, in the order the VTN allows: the report first.
	report.Name = "updated"
	if updated, err := p.ven.UpdateReport(c, report); err != nil || updated.Name != "updated" {
		t.Errorf("UpdateReport = %+v, %v", updated, err)
	}
	// OpenLEADR v0.1.4 does not let a VEN delete its report (403), against
	// the specification; Business Logic can.
	if _, err := p.ven.DeleteReport(c, report.ID); !errors.Is(err, openadr.ErrForbidden) {
		t.Errorf("the VEN's DeleteReport: %v, want 403 from OpenLEADR v0.1.4", err)
	}
	if err := p.deleteAsBL(c, "reports/"+report.ID); err != nil {
		t.Errorf("deleting the report as Business Logic: %v", err)
	}
	event.Name = "prices, revised"
	if updated, err := p.bl.UpdateEvent(c, event); err != nil || updated.Name != "prices, revised" {
		t.Errorf("UpdateEvent = %+v, %v", updated, err)
	}
	if _, err := p.bl.DeleteEvent(c, event.ID); err != nil {
		t.Errorf("DeleteEvent: %v", err)
	}
	if _, err := p.bl.DeleteProgram(c, program.ID); err != nil {
		t.Errorf("DeleteProgram: %v", err)
	}
}

// VENs and their resources, which 3.0.1 reaches through the VEN.
func TestOpenLEADR301VENsAndResources(t *testing.T) {
	p := openleadr301(t)
	c := ctx(t)
	group := []openadr.ValuesMap{{Type: "GROUP", Values: []openadr.Value{openadr.String(p.name("group"))}}}

	ven, err := p.bl.CreateVEN(c, openadr.VEN{Name: p.name("ven"), TypedTargets: group})
	if err != nil {
		t.Fatalf("CreateVEN: %v", err)
	}
	t.Cleanup(func() { _, _ = p.bl.DeleteVEN(context.Background(), ven.ID) })
	if len(ven.TypedTargets) != 1 || ven.TypedTargets[0].Type != "GROUP" {
		t.Errorf("VEN as created = %+v", ven)
	}

	res, err := p.bl.CreateResource(c, openadr.Resource{VENID: ven.ID, Name: "meter"})
	if err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if res.VENID != ven.ID {
		t.Errorf("resource as created = %+v", res)
	}
	if got, err := p.bl.ResourceOf(c, ven.ID, res.ID); err != nil || got.Name != "meter" {
		t.Errorf("ResourceOf = %+v, %v", got, err)
	}
	res.Name = "meter-2"
	if got, err := p.bl.UpdateResource(c, res); err != nil || got.Name != "meter-2" {
		t.Errorf("UpdateResource = %+v, %v", got, err)
	}
	if list, err := p.bl.Resources(c, client.ResourceQuery{VENID: ven.ID}); err != nil || len(list) != 1 {
		t.Errorf("Resources = %+v, %v", list, err)
	}
	if _, err := p.bl.DeleteResourceOf(c, ven.ID, res.ID); err != nil {
		t.Errorf("DeleteResourceOf: %v", err)
	}

	ven.TypedTargets = append(ven.TypedTargets, site1...)
	if got, err := p.bl.UpdateVEN(c, ven); err != nil || len(got.TypedTargets) != 2 {
		t.Errorf("UpdateVEN = %+v, %v", got, err)
	}
	if _, err := p.bl.DeleteVEN(c, ven.ID); err != nil {
		t.Errorf("DeleteVEN: %v", err)
	}
}

// What the client refuses before it sends anything, and what OpenLEADR
// v0.1.4 does not have.
func TestOpenLEADR301Limits(t *testing.T) {
	p := openleadr301(t)
	c := ctx(t)
	if _, err := p.bl.CreateProgram(c, openadr.Program{Name: p.name("strings"), Targets: []string{"group-a"}}); !errors.Is(err, openadr.ErrInvalid) {
		t.Errorf("a program with 3.1.0 targets: %v, want ErrInvalid", err)
	}
	if _, err := p.bl.Notifiers(c); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Notifiers: %v, want errors.ErrUnsupported", err)
	}
	// Subscriptions are of 3.0.1, and OpenLEADR v0.1.4 does not serve them.
	if _, err := p.ven.Subscriptions(c, client.SubscriptionQuery{}); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("Subscriptions: %v, want the 404 of a VTN without them", err)
	}
}
