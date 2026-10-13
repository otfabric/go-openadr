// SPDX-License-Identifier: MIT

// Package vtntest runs the VTN of go-openadr on a store and drives it with
// the module's clients, for the tests of the example stores: what a store
// passes on its own it also has to pass with a VTN on top of it.
package vtntest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// stack is a VTN on a store, with a Business Logic client and two VENs.
type stack struct {
	bl         *client.BL
	ven1, ven2 *client.VEN
}

func start(t *testing.T, store storage.Store) stack {
	t.Helper()
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
		{ID: "bl", Secret: "bl-secret", Scopes: auth.BusinessLogicScopes()},
		{ID: "ven-1", Secret: "ven-1-secret", Scopes: auth.VENScopes()},
		{ID: "ven-2", Secret: "ven-2-secret", Scopes: auth.VENScopes()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	vtn, err := server.New(server.Config{Store: store, Validator: issuer, TokenHandler: issuer, TokenURL: ts.URL + "/auth/token", ResourceGroups: true})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", vtn)
	credentials := func(id string) client.Config {
		return client.Config{BaseURL: ts.URL, Credentials: &client.Credentials{ClientID: id, ClientSecret: id + "-secret"}}
	}
	var s stack
	if s.bl, err = client.NewBL(credentials("bl")); err != nil {
		t.Fatal(err)
	}
	if s.ven1, err = client.NewVEN(credentials("ven-1")); err != nil {
		t.Fatal(err)
	}
	if s.ven2, err = client.NewVEN(credentials("ven-2")); err != nil {
		t.Fatal(err)
	}
	return s
}

// Workflow runs the VTN on a store through what its clients do, stops it,
// starts another on what reopen returns, and checks that the clients find
// everything as they left it, with the same rules in force.
func Workflow(t *testing.T, store storage.Store, reopen func(t *testing.T, s storage.Store) storage.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	vtn := start(t, store)

	// Business Logic: a public tariff, and a program for one group.
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tariff, err := vtn.bl.CreateProgram(ctx, openadr.Program{Name: "tariff",
		PayloadDescriptors: []openadr.PayloadDescriptor{openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"}}})
	if err != nil {
		t.Fatal(err)
	}
	flex, err := vtn.bl.CreateProgram(ctx, openadr.Program{Name: "flex", Targets: []string{"group-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.bl.CreateProgram(ctx, openadr.Program{Name: "tariff"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("a second program of the same name: %v, want 409", err)
	}
	var intervals []openadr.Interval
	for hour := range 24 {
		intervals = append(intervals, openadr.Interval{ID: int32(hour), Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.10 + float64(hour)/100)}}}})
	}
	prices, err := vtn.bl.CreateEvent(ctx, openadr.Event{ProgramID: tariff.ID, Name: "prices",
		IntervalPeriod: &openadr.IntervalPeriod{Start: &at, Duration: &openadr.Duration{Hours: 1}}, Intervals: intervals})
	if err != nil {
		t.Fatal(err)
	}
	limit, err := vtn.bl.CreateEvent(ctx, openadr.Event{ProgramID: flex.ID, Name: "limit", Targets: []string{"group-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.bl.CreateEvent(ctx, openadr.Event{ProgramID: "nope"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("an event of no program: %v, want 409", err)
	}
	if _, err := vtn.bl.DeleteProgram(ctx, tariff.ID); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("deleting a program with events: %v, want 409", err)
	}

	// The VENs: each registers, one is granted the group, both report.
	site1, err := vtn.ven1.CreateVEN(ctx, openadr.VEN{Name: "site-1"})
	if err != nil {
		t.Fatal(err)
	}
	battery, err := vtn.ven1.CreateResource(ctx, openadr.Resource{VENID: site1.ID, Name: "battery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.ven2.CreateVEN(ctx, openadr.VEN{Name: "site-2"}); err != nil {
		t.Fatal(err)
	}
	site1.Targets = []string{"group-a"}
	if _, err := vtn.bl.UpdateVEN(ctx, site1); err != nil {
		t.Fatal(err)
	}
	group, err := vtn.bl.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "street", Children: []openadr.ResourceGroupChild{{Type: openadr.ChildResource, ID: battery.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := vtn.ven1.CreateReport(ctx, openadr.Report{EventID: prices.ID, ClientName: "site-1",
		Resources: []openadr.ReportResource{{ResourceName: "battery", Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(1.5)}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.ven2.CreateReport(ctx, openadr.Report{EventID: prices.ID, ClientName: "site-2", Resources: []openadr.ReportResource{}}); err != nil {
		t.Fatal(err)
	}
	// More events than a page holds.
	for range 60 {
		if _, err := vtn.bl.CreateEvent(ctx, openadr.Event{ProgramID: tariff.ID}); err != nil {
			t.Fatal(err)
		}
	}

	check := func(when string, vtn stack) {
		t.Helper()
		got, err := vtn.ven2.Event(ctx, prices.ID)
		if err != nil || len(got.Intervals) != 24 || !got.IntervalPeriod.Start.Equal(at) || got.Created != prices.Created {
			t.Errorf("%s: the event with the prices: %d intervals, %v", when, len(got.Intervals), err)
		} else if last, _ := got.Intervals[23].Payloads[0].Values[0].Number(); last != 0.33 {
			t.Errorf("%s: the last price is %v", when, last)
		}
		// Targeting: the granted VEN sees the event of its group, the
		// other does not, by search or by ID.
		if list, err := vtn.ven1.Events(ctx, client.EventQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 1 || list[0].ID != limit.ID {
			t.Errorf("%s: the events of group-a for ven-1: %+v, %v", when, list, err)
		}
		if list, err := vtn.ven2.Events(ctx, client.EventQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 0 {
			t.Errorf("%s: the events of group-a for ven-2: %+v, %v", when, list, err)
		}
		if _, err := vtn.ven2.Event(ctx, limit.ID); !errors.Is(err, openadr.ErrNotFound) {
			t.Errorf("%s: ven-2 reads the event of another group: %v", when, err)
		}
		// Ownership: each VEN its own report, Business Logic both.
		if list, err := vtn.ven1.Reports(ctx, client.ReportQuery{}); err != nil || len(list) != 1 || list[0].ID != report.ID {
			t.Errorf("%s: the reports of ven-1: %+v, %v", when, list, err)
		}
		if list, err := vtn.bl.Reports(ctx, client.ReportQuery{ProgramID: tariff.ID}); err != nil || len(list) != 2 {
			t.Errorf("%s: the reports on the tariff: %d, %v", when, len(list), err)
		}
		if _, err := vtn.ven2.Report(ctx, report.ID); !errors.Is(err, openadr.ErrNotFound) {
			t.Errorf("%s: ven-2 reads the report of ven-1: %v", when, err)
		}
		if list, err := vtn.ven1.ResourceGroups(ctx, client.ResourceGroupQuery{}); err != nil || len(list) != 1 || list[0].ID != group.ID {
			t.Errorf("%s: the resource groups of ven-1: %+v, %v", when, list, err)
		}
		// Paging, in the order of creation.
		count, first := 0, ""
		for e, err := range client.Each(ctx, func(ctx context.Context, p client.Page) ([]openadr.Event, error) {
			return vtn.bl.Events(ctx, client.EventQuery{ProgramID: tariff.ID, Page: p})
		}) {
			if err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			if count == 0 {
				first = e.ID
			}
			count++
		}
		if count != 61 || first != prices.ID {
			t.Errorf("%s: %d events of the tariff, the first %s", when, count, first)
		}
	}
	check("before the restart", vtn)

	if reopen == nil {
		return
	}
	vtn = start(t, reopen(t, store))
	check("after the restart", vtn)
	// And it goes on where it stopped.
	if _, err := vtn.ven1.DeleteResource(ctx, battery.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := vtn.bl.ResourceGroup(ctx, group.ID); err != nil || len(got.Children) != 0 {
		t.Errorf("the group after its resource was deleted: %+v, %v", got, err)
	}
	if _, err := vtn.bl.CreateProgram(ctx, openadr.Program{Name: "flex"}); !errors.Is(err, openadr.ErrConflict) {
		t.Errorf("after the restart, a program with a name in use: %v, want 409", err)
	}
}
