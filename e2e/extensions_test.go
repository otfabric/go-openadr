// SPDX-License-Identifier: MIT

package e2e_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server/storage"
	"github.com/otfabric/go-openadr/timeline"
)

// do sends a request as the test writes it and returns the status, the
// headers and the body.
func (s *stack) do(t *testing.T, method, path, token, body string, header ...string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, answer
}

// --- a VTN that does not require authentication

// A VTN with public information lets clients in without a token. They see
// what has no targets and nothing that belongs to anyone; everything else
// is as it was.
func TestAnonymousAccess(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{anonymous: true, mqtt: true})
	public := mustProgram(t, vtn.BL, openadr.Program{Name: "public-tariff"})
	private := mustProgram(t, vtn.BL, openadr.Program{Name: "group-a-flex", Targets: []string{"group-a"}})
	prices := mustEvent(t, vtn.BL, openadr.Event{ProgramID: public.ID, Name: "prices"})
	limit := mustEvent(t, vtn.BL, openadr.Event{ProgramID: private.ID, Name: "limit", Targets: []string{"group-a"}})
	ven, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}})
	if err != nil {
		t.Fatal(err)
	}

	// A client configured with the URL and nothing else, as the
	// specification has it.
	anyone, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP})
	if err != nil {
		t.Fatal(err)
	}
	programs, err := anyone.Programs(ctx, client.ProgramQuery{})
	if err != nil || len(programs) != 1 || programs[0].ID != public.ID {
		t.Errorf("programs without a token: %+v, %v", programs, err)
	}
	if got, err := anyone.Program(ctx, public.ID); err != nil || got.Name != "public-tariff" {
		t.Errorf("a public program: %+v, %v", got, err)
	}
	events, err := anyone.Events(ctx, client.EventQuery{})
	if err != nil || len(events) != 1 || events[0].ID != prices.ID {
		t.Errorf("events without a token: %+v, %v", events, err)
	}
	// What is targeted does not exist for it, whatever it names.
	if _, err := anyone.Program(ctx, private.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("a targeted program: %v", err)
	}
	if _, err := anyone.Event(ctx, limit.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("a targeted event: %v", err)
	}
	if list, err := anyone.Programs(ctx, client.ProgramQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 0 {
		t.Errorf("naming a target without a token: %+v, %v", list, err)
	}
	if n, err := anyone.Notifiers(ctx); err != nil || !n.Webhook || n.MQTT == nil {
		t.Errorf("notifiers: %+v, %v", n, err)
	}

	// Everything that belongs to a client, and every write, needs one.
	refused := map[string]error{}
	_, refused["reports"] = anyone.Reports(ctx, client.ReportQuery{})
	_, refused["vens"] = anyone.VENs(ctx, client.VENQuery{})
	_, refused["a ven"] = anyone.VEN(ctx, ven.ID)
	_, refused["resources"] = anyone.Resources(ctx, client.ResourceQuery{})
	_, refused["subscriptions"] = anyone.Subscriptions(ctx, client.SubscriptionQuery{})
	_, refused["a report"] = anyone.CreateReport(ctx, openadr.Report{EventID: prices.ID, ClientName: "x", Resources: []openadr.ReportResource{}})
	_, refused["registering"] = anyone.CreateVEN(ctx, openadr.VEN{Name: "squatter"})
	_, refused["topics of a ven"] = anyone.MQTTTopics(ctx, client.EventsForVEN(ven.ID))
	for what, err := range refused {
		if !errors.Is(err, openadr.ErrUnauthorized) {
			t.Errorf("%s without a token: %v, want 401", what, err)
		}
	}
	for _, write := range [][2]string{{"POST", "/programs"}, {"PUT", "/programs/" + public.ID}, {"DELETE", "/events/" + prices.ID}, {"GET", "/notifiers/mqtt/topics/programs"}} {
		status, header, _ := vtn.do(t, write[0], write[1], "", `{"programName":"x"}`)
		if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s without a token: %d", write[0], write[1], status)
		}
	}

	// A token is still checked when one is sent, and still decides.
	if status, _, _ := vtn.do(t, "GET", "/programs", "not-a-token", ""); status != http.StatusUnauthorized {
		t.Errorf("a forged token on a VTN that lets anyone in: %d", status)
	}
	if list, err := vtn.VEN1.Programs(ctx, client.ProgramQuery{Targets: []string{"group-a"}}); err != nil || len(list) != 1 {
		t.Errorf("the granted VEN: %+v, %v", list, err)
	}
	if list, err := vtn.BL.Programs(ctx, client.ProgramQuery{}); err != nil || len(list) != 2 {
		t.Errorf("Business Logic: %+v, %v", list, err)
	}
}

// --- compression

func TestResponseCompression(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})
	for i := range 40 {
		mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("program-%02d", i),
			Descriptions: []openadr.ProgramDescription{{URL: "https://example.com/programs/a-description-that-repeats"}}})
	}
	token := vtn.token(t, "bl")

	status, header, plain := vtn.do(t, "GET", "/programs", token, "", "Accept-Encoding", "identity")
	if status != 200 || header.Get("Content-Encoding") != "" {
		t.Fatalf("identity: %d, Content-Encoding %q", status, header.Get("Content-Encoding"))
	}
	if !strings.Contains(header.Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary is %q: a cache would serve one client's encoding to another", header.Get("Vary"))
	}

	// Setting the header by hand turns off the transparent handling of
	// the Go transport, so this is what is on the wire.
	status, header, packed := vtn.do(t, "GET", "/programs", token, "", "Accept-Encoding", "br, gzip;q=0.8")
	if status != 200 || header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %d, Content-Encoding %q", status, header.Get("Content-Encoding"))
	}
	if len(packed)*3 > len(plain) {
		t.Errorf("%d bytes became %d: not much of a compression", len(plain), len(packed))
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	unpacked, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(unpacked, plain) {
		t.Errorf("the compressed answer is not the plain one: %v", err)
	}

	// The other coding the standard library can write, for who prefers it.
	status, header, packed = vtn.do(t, "GET", "/programs", token, "", "Accept-Encoding", "deflate, gzip;q=0.5")
	if status != 200 || header.Get("Content-Encoding") != "deflate" {
		t.Fatalf("deflate: %d, Content-Encoding %q", status, header.Get("Content-Encoding"))
	}
	if zl, err := zlib.NewReader(bytes.NewReader(packed)); err != nil {
		t.Errorf("deflate is not the zlib format: %v", err)
	} else if unpacked, err := io.ReadAll(zl); err != nil || !bytes.Equal(unpacked, plain) {
		t.Errorf("the deflated answer is not the plain one: %v", err)
	}

	// Not for who refuses it or wants what the VTN has not, and not for
	// what is small.
	for _, accept := range []string{"gzip;q=0", "br, zstd", ""} {
		if _, header, _ := vtn.do(t, "GET", "/programs", token, "", "Accept-Encoding", accept); header.Get("Content-Encoding") != "" {
			t.Errorf("Accept-Encoding %q was answered with %q", accept, header.Get("Content-Encoding"))
		}
	}
	if _, header, _ := vtn.do(t, "GET", "/programs?limit=1", token, "", "Accept-Encoding", "gzip"); header.Get("Content-Encoding") != "" {
		t.Error("a small answer was compressed")
	}
	if status, header, body := vtn.do(t, "GET", "/programs/nope", token, "", "Accept-Encoding", "gzip"); status != 404 || header.Get("Content-Encoding") != "" || !bytes.Contains(body, []byte(`"status":404`)) {
		t.Errorf("a problem: %d %q %s", status, header.Get("Content-Encoding"), body)
	}

	// The clients ask for it and read it without being told to.
	list, err := vtn.BL.Programs(ctx, client.ProgramQuery{})
	if err != nil || len(list) != 40 {
		t.Errorf("the client read %d programs, %v", len(list), err)
	}

	off := start(t, options{noCompression: true})
	for i := range 40 {
		mustProgram(t, off.BL, openadr.Program{Name: fmt.Sprintf("program-%02d", i)})
	}
	if _, header, _ := off.do(t, "GET", "/programs", off.token(t, "bl"), "", "Accept-Encoding", "gzip"); header.Get("Content-Encoding") != "" {
		t.Error("a VTN with compression turned off compressed")
	}
}

// --- intervals as tables

// An event goes to the VTN, comes back to a VEN, and is laid out as a
// table there; a table becomes the intervals of an event.
func TestIntervalTables(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{})
	program := mustProgram(t, vtn.BL, openadr.Program{Name: "tariff"})
	midnight := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	// A tariff as a spreadsheet has it.
	sheet := "start,end,type,values\n" +
		"2030-01-01T00:00:00Z,2030-01-01T07:00:00Z,PRICE,0.11\n" +
		"2030-01-01T07:00:00Z,2030-01-01T23:00:00Z,PRICE,0.29\n" +
		"2030-01-01T23:00:00Z,2030-01-02T00:00:00Z,PRICE,0.11\n"
	table, err := timeline.ReadCSV(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	created := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "day-and-night", Intervals: table.Intervals(),
		Duration: &openadr.Duration{Days: 2}})

	got, err := vtn.VEN1.Event(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := timeline.OfEvent(got, timeline.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Two days of three periods each: the duration of the event repeats them.
	if len(rows) != 6 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	for when, want := range map[time.Duration]float64{3 * time.Hour: 0.11, 12 * time.Hour: 0.29, 23*time.Hour + 30*time.Minute: 0.11, 36 * time.Hour: 0.29} {
		now := rows.At(midnight.Add(when))
		if len(now) != 1 || now[0].Values[0] != openadr.Number(want) {
			t.Errorf("at +%v: %+v, want %v", when, now, want)
		}
	}
	if start, end := rows.Span(); !start.Equal(midnight) || !end.Equal(midnight.Add(48*time.Hour)) {
		t.Errorf("span %v to %v", start, end)
	}
	var out bytes.Buffer
	if err := rows[:3].WriteCSV(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1,0,2030-01-01T07:00:00Z,2030-01-01T23:00:00Z,PRICE,0.29,") {
		t.Errorf("as CSV:\n%s", out.String())
	}

	// The compact form, as Business Logic of another make may write it:
	// the start the User Guide spells without a zone, values packed into
	// one interval. The VTN takes it, and a VEN lays it out from now.
	body := fmt.Sprintf(`{"programID":%q,"eventName":"from-now",
		"intervalPeriod":{"start":"0001-01-01T00:00:00","duration":"PT1H"},
		"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[1,2,3,4]}]},
		             {"id":1,"intervalPeriod":{"start":"0001-01-01","duration":"P9999Y"},"payloads":[{"type":"PRICE","values":[9]}]}]}`, program.ID)
	status, _, answer := vtn.do(t, "POST", "/events", vtn.token(t, "bl"), body, "Content-Type", "application/json")
	if status != http.StatusCreated {
		t.Fatalf("the compact event: %d %s", status, answer)
	}
	list, err := vtn.VEN1.Events(ctx, client.EventQuery{ProgramID: program.ID})
	if err != nil || len(list) != 2 {
		t.Fatalf("events: %d, %v", len(list), err)
	}
	now := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	rows, err = timeline.OfEvent(list[1], timeline.Options{Now: now, SubIntervals: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || !rows[0].Start.Equal(now) || !rows[3].End.Equal(now.Add(time.Hour)) || !rows[4].Start.Equal(now.Add(time.Hour)) || !rows[4].End.IsZero() {
		t.Errorf("laid out from now: %+v", rows)
	}
	if at := rows.At(now.Add(40 * time.Minute)); len(at) != 1 || at[0].Values[0] != openadr.Number(3) {
		t.Errorf("40 minutes in: %+v", at)
	}
	// An event that is still on, because it has no end, is active.
	active, err := vtn.VEN1.Events(ctx, client.EventQuery{ProgramID: program.ID, ActiveOnly: true})
	if err != nil || len(active) != 2 {
		t.Errorf("active events: %d, %v", len(active), err)
	}

	// And a report follows the intervals of its event.
	reportRows := timeline.Table{
		{IntervalID: 0, Start: midnight, End: midnight.Add(7 * time.Hour), Type: "USAGE", Values: []openadr.Value{openadr.Number(4.2)}},
		{IntervalID: 1, Start: midnight.Add(7 * time.Hour), End: midnight.Add(23 * time.Hour), Type: "USAGE", Values: []openadr.Value{openadr.Number(17)}},
	}
	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: created.ID, ClientName: "meter",
		Resources: []openadr.ReportResource{{ResourceName: "site", Intervals: reportRows.Intervals()}}})
	if err != nil {
		t.Fatal(err)
	}
	read, err := vtn.BL.Report(ctx, report.ID)
	if err != nil {
		t.Fatal(err)
	}
	back, err := timeline.Of(read.Resources[0].IntervalPeriod, read.Resources[0].Intervals, timeline.Options{})
	if err != nil || !reflect.DeepEqual(back, reportRows) {
		t.Errorf("the report as a table:\n got %+v, %v\nwant %+v", back, err, reportRows)
	}
}

// --- resource groups

func child(kind openadr.ResourceGroupChildType, id string) openadr.ResourceGroupChild {
	return openadr.ResourceGroupChild{Type: kind, ID: id}
}

// site registers a ven with one resource for a VEN client and returns the
// resource.
func site(t *testing.T, ven *client.VEN, name string) openadr.Resource {
	t.Helper()
	v, err := ven.CreateVEN(context.Background(), openadr.VEN{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ven.CreateResource(context.Background(), openadr.Resource{VENID: v.ID, Name: "battery"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestResourceGroups(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{groups: true})
	battery1, battery2 := site(t, vtn.VEN1, "site-1"), site(t, vtn.VEN2, "site-2")

	// Business Logic arranges the resources: two streets, and a district
	// that holds them.
	street1, err := vtn.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "street-1", Targets: []string{"feeder-7"},
		Attributes: []openadr.ValuesMap{{Type: "LOCATION", Values: []openadr.Value{openadr.PointValue(52.1, 4.3)}}},
		Children:   []openadr.ResourceGroupChild{child(openadr.ChildResource, battery1.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if street1.ID == "" || street1.Created.IsZero() || street1.Name != "street-1" || len(street1.Children) != 1 || len(street1.Attributes) != 1 {
		t.Errorf("created %+v", street1)
	}
	street2, err := vtn.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "street-2",
		Children: []openadr.ResourceGroupChild{child(openadr.ChildResource, battery2.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	district, err := vtn.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "district",
		Children: []openadr.ResourceGroupChild{child(openadr.ChildResourceGroup, street1.ID), child(openadr.ChildResourceGroup, street2.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := vtn.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}

	// Business Logic reads them all, whole, and by name and target.
	all, err := vtn.BL.ResourceGroups(ctx, client.ResourceGroupQuery{})
	if err != nil || len(all) != 4 || !reflect.DeepEqual(all[2], district) {
		t.Errorf("all groups: %+v, %v", all, err)
	}
	if got, err := vtn.BL.ResourceGroup(ctx, street1.ID); err != nil || !reflect.DeepEqual(got, street1) {
		t.Errorf("read back %+v, %v", got, err)
	}
	if list, err := vtn.BL.ResourceGroups(ctx, client.ResourceGroupQuery{Name: "street-2"}); err != nil || len(list) != 1 || list[0].ID != street2.ID {
		t.Errorf("by name: %+v, %v", list, err)
	}
	if list, err := vtn.BL.ResourceGroups(ctx, client.ResourceGroupQuery{Targets: []string{"feeder-7", "other"}}); err != nil || len(list) != 1 || list[0].ID != street1.ID {
		t.Errorf("by target: %+v, %v", list, err)
	}
	if list, err := vtn.BL.ResourceGroups(ctx, client.ResourceGroupQuery{Page: client.Page{Skip: 1, Limit: 2}}); err != nil || len(list) != 2 || list[0].ID != street2.ID {
		t.Errorf("a page: %+v, %v", list, err)
	}

	// A VEN reads the groups that lead to a resource of its own, and of
	// their children those only.
	mine, err := vtn.VEN1.ResourceGroups(ctx, client.ResourceGroupQuery{})
	if err != nil || len(mine) != 2 || mine[0].ID != street1.ID || mine[1].ID != district.ID {
		t.Fatalf("groups of ven-1: %+v, %v", mine, err)
	}
	if want := []openadr.ResourceGroupChild{child(openadr.ChildResourceGroup, street1.ID)}; !reflect.DeepEqual(mine[1].Children, want) {
		t.Errorf("ven-1 sees the district hold %+v, want %+v", mine[1].Children, want)
	}
	if got, err := vtn.VEN2.ResourceGroup(ctx, district.ID); err != nil || len(got.Children) != 1 || got.Children[0].ID != street2.ID {
		t.Errorf("ven-2 reads the district: %+v, %v", got, err)
	}
	for what, id := range map[string]string{"another VEN's street": street2.ID, "an empty group": empty.ID} {
		if _, err := vtn.VEN1.ResourceGroup(ctx, id); !errors.Is(err, openadr.ErrNotFound) {
			t.Errorf("ven-1 reads %s: %v", what, err)
		}
	}

	// Children have to exist, and be of a kind.
	for what, c := range map[string]openadr.ResourceGroupChild{
		"a resource that does not exist": child(openadr.ChildResource, "nope"),
		"a group that does not exist":    child(openadr.ChildResourceGroup, "nope"),
		// The ID of a group is not that of a resource.
		"a group given as a resource": child(openadr.ChildResource, street1.ID),
	} {
		if _, err := vtn.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "bad", Children: []openadr.ResourceGroupChild{c}}); !errors.Is(err, openadr.ErrConflict) {
			t.Errorf("%s: %v, want 409", what, err)
		}
	}
	if _, err := vtn.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "bad", Children: []openadr.ResourceGroupChild{child("device", "x")}}); err == nil {
		t.Error("a child of an unknown kind was sent")
	}
	if status, _, body := vtn.do(t, "POST", "/resource_groups", vtn.token(t, "bl"), `{"resourceGroupName":"bad","children":[{"type":"device","id":"x"}]}`); status != 400 {
		t.Errorf("a child of an unknown kind: %d %s", status, body)
	}
	if status, _, body := vtn.do(t, "POST", "/resource_groups", vtn.token(t, "bl"), `{"children":[]}`); status != 400 {
		t.Errorf("a group without a name: %d %s", status, body)
	}

	// An update replaces the children; a group may come to hold itself,
	// and groups each other, without the VTN going in circles.
	street1.Name, street1.Children = "street-1a", append(street1.Children, child(openadr.ChildResourceGroup, district.ID), child(openadr.ChildResourceGroup, street1.ID))
	updated, err := vtn.BL.UpdateResourceGroup(ctx, street1)
	if err != nil || updated.Name != "street-1a" || len(updated.Children) != 3 || !updated.Modified.After(updated.Created.Add(-time.Second)) || updated.Created != street1.Created {
		t.Fatalf("updated %+v, %v", updated, err)
	}
	// Through the circle, the district now leads ven-2 to street-1 too.
	if list, err := vtn.VEN2.ResourceGroups(ctx, client.ResourceGroupQuery{}); err != nil || len(list) != 3 {
		t.Errorf("groups of ven-2 through the circle: %+v, %v", list, err)
	}
	if got, err := vtn.VEN2.ResourceGroup(ctx, street1.ID); err != nil || len(got.Children) != 2 {
		t.Errorf("ven-2 reads street-1: %+v, %v (the battery of ven-1 is not for it)", got, err)
	}
	if _, err := vtn.BL.UpdateResourceGroup(ctx, openadr.ResourceGroup{Name: "x"}); err == nil {
		t.Error("an update without an ID was sent")
	}

	// What is deleted leaves the groups it was in.
	if _, err := vtn.VEN1.DeleteResource(ctx, battery1.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := vtn.BL.ResourceGroup(ctx, street1.ID); err != nil || len(got.Children) != 2 {
		t.Errorf("after its resource was deleted: %+v, %v", got, err)
	}
	if list, err := vtn.VEN1.ResourceGroups(ctx, client.ResourceGroupQuery{}); err != nil || len(list) != 0 {
		t.Errorf("ven-1 has no resource left, and sees %+v, %v", list, err)
	}
	deleted, err := vtn.BL.DeleteResourceGroup(ctx, street2.ID)
	if err != nil || deleted.ID != street2.ID {
		t.Fatalf("deleted %+v, %v", deleted, err)
	}
	if got, err := vtn.BL.ResourceGroup(ctx, district.ID); err != nil || len(got.Children) != 1 || got.Children[0].ID != street1.ID {
		t.Errorf("the district after a street was deleted: %+v, %v", got, err)
	}
	if _, err := vtn.BL.ResourceGroup(ctx, street2.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("a deleted group: %v", err)
	}
	// The resource of the deleted group is still there.
	if _, err := vtn.VEN2.Resource(ctx, battery2.ID); err != nil {
		t.Errorf("deleting a group took its resource along: %v", err)
	}
}

// A VTN that was not told to serve them has no resource groups.
func TestNoResourceGroups(t *testing.T) {
	vtn := start(t, options{})
	if _, err := vtn.BL.ResourceGroups(context.Background(), client.ResourceGroupQuery{}); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("got %v, want 404", err)
	}
	if _, err := vtn.VEN1.ResourceGroup(context.Background(), "x"); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("got %v, want 404", err)
	}
}

func TestResourceGroupsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vtn.journal")
	open := func() (*storage.File, *stack) {
		store, err := storage.OpenFile(path, storage.FileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return store, start(t, options{store: store, groups: true})
	}
	store, before := open()
	battery := site(t, before.VEN1, "site-1")
	// Created in an order a replay could not repeat: the first comes to
	// hold the second, and itself.
	a, err := before.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := before.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "b", Targets: []string{"t"},
		Children: []openadr.ResourceGroupChild{child(openadr.ChildResource, battery.ID), child(openadr.ChildResourceGroup, a.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	a.Children = []openadr.ResourceGroupChild{child(openadr.ChildResourceGroup, b.ID), child(openadr.ChildResourceGroup, a.ID)}
	if a, err = before.BL.UpdateResourceGroup(ctx, a); err != nil {
		t.Fatal(err)
	}
	gone, err := before.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.BL.DeleteResourceGroup(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}

	// Twice: once from the journal as it was written, once from the
	// snapshot the first reopening made of it.
	for round := 1; round <= 2; round++ {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		var after *stack
		store, after = open()
		list, err := after.BL.ResourceGroups(ctx, client.ResourceGroupQuery{})
		if err != nil || len(list) != 2 || !reflect.DeepEqual(list[0], a) || !reflect.DeepEqual(list[1], b) {
			t.Fatalf("round %d: after the restart:\n got %+v, %v\nwant %+v", round, list, err, []openadr.ResourceGroup{a, b})
		}
		if mine, err := after.VEN1.ResourceGroups(ctx, client.ResourceGroupQuery{}); err != nil || len(mine) != 2 {
			t.Errorf("round %d: ven-1 after the restart: %+v, %v", round, mine, err)
		}
	}
	_ = store.Close()
}
