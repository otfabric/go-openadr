//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// goVTN is this module's VTN, serving a fixture of openadr-interop on the
// host, for a reference client in a container.
type goVTN struct {
	store *storage.Memory
	// ids maps "program:<name>", "event:<name>" and "ven:<name>" to object IDs.
	ids    map[string]string
	client clientRunner
}

// startGoVTN runs the VTN with the clients of the fixture and loads the
// fixture's objects through the API, as its Business Logic client: the same
// way the reference servers of openadr-interop are loaded.
func startGoVTN(t *testing.T, clientAdapter, fixtureName string) *goVTN {
	t.Helper()
	img := needImage(t, clientAdapter)
	fx := readFixture(t, img, fixtureName)

	var clients []auth.Client
	for _, c := range fx.Clients {
		scopes := auth.VENScopes()
		if c.Role == "bl" {
			scopes = auth.BusinessLogicScopes()
		}
		clients = append(clients, auth.Client{ID: c.ClientID, Secret: c.ClientSecret, Scopes: scopes})
	}
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}

	host, network := hostAccess()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// The URL the container uses is also the one the VTN announces its
	// token endpoint under.
	public := fmt.Sprintf("http://%s:%d", host, port)
	local := fmt.Sprintf("http://127.0.0.1:%d", port)

	store := storage.NewMemory()
	vtn, err := server.New(server.Config{Store: store, Validator: issuer, TokenURL: public + "/auth/token", TokenHandler: issuer})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: vtn, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	v := &goVTN{store: store, ids: map[string]string{}, client: clientRunner{img: img, fixture: fixtureName, url: public, network: network}}
	v.load(t, fx, local)
	return v
}

func (v *goVTN) load(t *testing.T, fx fixture, base string) {
	t.Helper()
	blID, blSecret := fx.credentials(t, "bl")
	resp, err := http.PostForm(base+"/auth/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {blID}, "client_secret": {blSecret}})
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&token)
	_ = resp.Body.Close()
	if err != nil || token.AccessToken == "" {
		t.Fatalf("no token for the fixture's Business Logic client: %v", err)
	}
	post := func(path string, body any) string {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		var created struct {
			ID string `json:"id"`
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &created) != nil || created.ID == "" {
			t.Fatalf("loading the fixture: POST %s %s: %d %s", path, data, resp.StatusCode, raw)
		}
		return created.ID
	}

	venClient := map[string]string{}
	for _, ven := range fx.VENs {
		clientID, _ := fx.credentials(t, ven.Client)
		venClient[ven.VENName] = clientID
		v.ids["ven:"+ven.VENName] = post("/vens", map[string]any{
			"objectType": "BL_VEN_REQUEST", "venName": ven.VENName, "clientID": clientID, "targets": ven.Targets})
	}
	for _, res := range fx.Resources {
		post("/resources", map[string]any{
			"objectType": "BL_RESOURCE_REQUEST", "resourceName": res.ResourceName,
			"venID": v.ids["ven:"+res.VEN], "clientID": venClient[res.VEN]})
	}
	for _, raw := range fx.Programs {
		var program map[string]any
		_ = json.Unmarshal(raw, &program)
		v.ids["program:"+program["programName"].(string)] = post("/programs", program)
	}
	for _, raw := range fx.Events {
		var event map[string]any
		_ = json.Unmarshal(raw, &event)
		event["programID"] = v.ids["program:"+event["program"].(string)]
		delete(event, "program")
		v.ids["event:"+event["eventName"].(string)] = post("/events", event)
	}
}

// expand replaces @kind:name@ by the ID the VTN gave that fixture object.
func (v *goVTN) expand(t *testing.T, arg string) string {
	t.Helper()
	for key, id := range v.ids {
		arg = strings.ReplaceAll(arg, "@"+key+"@", id)
	}
	if strings.Contains(arg, "@") {
		t.Fatalf("unresolved fixture reference in %q", arg)
	}
	return arg
}

func (v *goVTN) run(t *testing.T, args ...string) result {
	t.Helper()
	for i := range args {
		args[i] = v.expand(t, args[i])
	}
	return v.client.run(t, args...)
}

// The ElaadNL openadr3-client, as VEN and as Business Logic, reading what
// this module's VTN serves.
func TestElaadNLReadsGoVTN(t *testing.T) {
	vtn := startGoVTN(t, "elaadnl", "baseline")

	cases := []struct {
		op string
		// what the client must report: exit code, error code, HTTP status
		// of the VTN's answer, number of objects returned.
		exit   int
		err    string
		status int
		n      int
	}{
		{"list-programs --as bl", 0, "", 200, 2},
		{"list-programs --as ven-2", 0, "", 200, 1},
		// This VTN shows a targeted program only to a search that names the target.
		{"list-programs --as ven-1", 0, "", 200, 1},
		{"list-programs --as ven-1 --targets group-a", 0, "", 200, 1},
		{"list-programs --as ven-2 --targets group-a", 0, "", 200, 0},
		{"list-programs --as bl --targets group-a", 0, "", 200, 1},
		{"list-programs --as bl --skip 0 --limit 1", 0, "", 200, 1},
		{"list-programs --as bl --skip 1 --limit 50", 0, "", 200, 1},
		{"list-programs --as bl --skip 2 --limit 50", 0, "", 200, 0},
		{"list-programs --as bl --skip 0 --limit 51", 1, "http-error", 400, 0},
		{"get-program --as bl --id @program:group-a-flex@", 0, "", 200, 1},
		{"get-program --as ven-2 --id @program:day-ahead@", 0, "", 200, 1},
		{"get-program --as ven-1 --id @program:group-a-flex@", 0, "", 200, 1},
		{"get-program --as ven-2 --id @program:group-a-flex@", 1, "http-error", 404, 0},
		{"get-program --as bl --id no-such-program", 1, "http-error", 404, 0},
		{"list-events --as bl", 0, "", 200, 2},
		{"list-events --as bl --program-id @program:day-ahead@", 0, "", 200, 1},
		{"list-events --as ven-2", 0, "", 200, 1},
		{"list-events --as ven-1 --targets group-a", 0, "", 200, 1},
		{"list-events --as ven-2 --targets group-a", 0, "", 200, 0},
		{"get-event --as ven-1 --id @event:day-ahead-prices@", 0, "", 200, 1},
		{"get-event --as ven-1 --id no-such-event", 1, "http-error", 404, 0},
		{"list-vens --as bl", 0, "", 200, 2},
		{"list-vens --as bl --ven-name site-2", 0, "", 200, 1},
		{"list-vens --as ven-1", 0, "", 200, 1},
		{"list-vens --as ven-1 --ven-name site-2", 0, "", 200, 0},
		{"list-vens --as ven-3", 0, "", 200, 0},
		{"get-ven --as ven-1 --id @ven:site-1@", 0, "", 200, 1},
		{"get-ven --as ven-2 --id @ven:site-1@", 1, "http-error", 404, 0},
		{"list-resources --as bl", 0, "", 200, 1},
		{"list-resources --as ven-1 --ven-id @ven:site-1@", 0, "", 200, 1},
		{"list-resources --as ven-2", 0, "", 200, 0},
		{"list-reports --as bl", 0, "", 200, 0},
		{"get-report --as ven-1 --id no-such-report", 1, "http-error", 404, 0},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			r := vtn.run(t, strings.Fields(tc.op)...)
			n := 0
			switch {
			case len(r.Response) == 0 || string(r.Response) == "null":
			case r.Response[0] == '[':
				n = len(r.objects(t))
			default:
				n = 1
			}
			if r.exit != tc.exit || r.errorCode() != tc.err || r.status() != tc.status || n != tc.n {
				t.Errorf("exit %d, error %q, status %d, %d objects; want exit %d, error %q, status %d, %d objects (%+v)",
					r.exit, r.errorCode(), r.status(), n, tc.exit, tc.err, tc.status, tc.n, r.Error)
			}
			if tc.exit == 0 && !r.Authenticated {
				t.Error("the client did not obtain a token through GET /auth/server and the token endpoint")
			}
			// An error of this VTN is a problem object the client can read.
			if tc.err == "http-error" && (r.Problem == nil || r.Problem["status"] != float64(tc.status)) {
				t.Errorf("problem = %v, want one with status %d", r.Problem, tc.status)
			}
		})
	}

	// What the library made of an event: every part of it arrived.
	r := vtn.run(t, "get-event", "--as", "ven-1", "--id", "@event:day-ahead-prices@")
	event := r.object(t)
	period, _ := event["intervalPeriod"].(map[string]any)
	intervals, _ := event["intervals"].([]any)
	if event["eventName"] != "day-ahead-prices" || event["priority"] != float64(1) || event["programID"] != vtn.ids["program:day-ahead"] ||
		period["duration"] != "PT1H" || len(intervals) != 3 {
		t.Errorf("the event as the library read it: %s", r.Response)
	}
	// Target hiding: the VEN is shown the target it was granted.
	r = vtn.run(t, "list-programs", "--as", "ven-1", "--targets", "group-a")
	if list := r.objects(t); len(list) != 1 || fmt.Sprint(list[0]["targets"]) != "[group-a]" {
		t.Errorf("targeted program as ven-1 reads it: %s", r.Response)
	}
}

// The basic workflow with the ElaadNL client on both sides of this module's
// VTN, checked in the VTN's own store.
func TestElaadNLWorkflowOnGoVTN(t *testing.T) {
	vtn := startGoVTN(t, "elaadnl", "baseline")
	ctx := context.Background()
	must := func(what string, r result, status int) result {
		t.Helper()
		if !r.OK || r.status() != status {
			t.Fatalf("%s: exit %d, status %d, error %+v, problem %v", what, r.exit, r.status(), r.Error, r.Problem)
		}
		return r
	}

	program := must("create-program", vtn.run(t, "create-program", "--as", "bl", "--body",
		`{"programName":"workflow","targets":["group-a"],"payloadDescriptors":[{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE","units":"KWH","currency":"EUR"}]}`), 201).object(t)
	programID := program["id"].(string)
	stored, err := vtn.store.Programs().Get(ctx, programID)
	if err != nil || stored.Name != "workflow" || fmt.Sprint(stored.Targets) != "[group-a]" || len(stored.PayloadDescriptors) != 1 {
		t.Fatalf("the program in the VTN's store: %+v, %v", stored, err)
	}
	if d, ok := stored.PayloadDescriptors[0].(openadr.EventPayloadDescriptor); !ok || d.PayloadType != "PRICE" || d.Currency != "EUR" {
		t.Errorf("payload descriptor in the store: %+v", stored.PayloadDescriptors[0])
	}

	eventBody := fmt.Sprintf(`{"programID":%q,"eventName":"workflow-prices","priority":3,"targets":["group-a"],`+
		`"intervalPeriod":{"start":"2031-06-01T10:00:00Z","duration":"PT15M"},"reportDescriptors":[{"payloadType":"USAGE"}],`+
		`"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.31]}]},{"id":1,"payloads":[{"type":"PRICE","values":[0.24]}]}]}`, programID)
	event := must("create-event", vtn.run(t, "create-event", "--as", "bl", "--body", eventBody), 201).object(t)
	eventID := event["id"].(string)
	storedEvent, err := vtn.store.Events().Get(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2031, 6, 1, 10, 0, 0, 0, time.UTC)
	if storedEvent.Priority == nil || *storedEvent.Priority != 3 || storedEvent.IntervalPeriod == nil ||
		!storedEvent.IntervalPeriod.Start.Equal(wantStart) || *storedEvent.IntervalPeriod.Duration != (openadr.Duration{Minutes: 15}) ||
		len(storedEvent.Intervals) != 2 || len(storedEvent.ReportDescriptors) != 1 {
		t.Errorf("the event in the VTN's store: %+v", storedEvent)
	}
	if price, _ := storedEvent.Intervals[1].Payloads[0].Values[0].Number(); price != 0.24 {
		t.Errorf("price of interval 1 in the store = %v", price)
	}

	// ven-1 was granted group-a by the fixture; ven-2 was not.
	if list := must("list-events as ven-1", vtn.run(t, "list-events", "--as", "ven-1", "--targets", "group-a", "--program-id", programID), 200).objects(t); len(list) != 1 || list[0]["id"] != eventID {
		t.Fatalf("ven-1 does not find the event: %v", list)
	}
	if list := must("list-events as ven-2", vtn.run(t, "list-events", "--as", "ven-2", "--targets", "group-a", "--program-id", programID), 200).objects(t); len(list) != 0 {
		t.Errorf("ven-2 sees an event targeted at group-a: %v", list)
	}

	reportBody := fmt.Sprintf(`{"eventID":%q,"clientName":"site-1","reportName":"usage","resources":[{"resourceName":"battery",`+
		`"intervals":[{"id":0,"payloads":[{"type":"USAGE","values":[1.8]}]}]}]}`, eventID)
	report := must("create-report", vtn.run(t, "create-report", "--as", "ven-1", "--body", reportBody), 201).object(t)
	reportID := report["id"].(string)
	storedReport, err := vtn.store.Reports().Get(ctx, reportID)
	if err != nil || storedReport.ClientID != "interop-ven-1" || storedReport.EventID != eventID || storedReport.Name != "usage" {
		t.Fatalf("the report in the VTN's store: %+v, %v", storedReport, err)
	}
	if kwh, _ := storedReport.Resources[0].Intervals[0].Payloads[0].Values[0].Number(); kwh != 1.8 {
		t.Errorf("reported usage in the store = %v", kwh)
	}

	if list := must("list-reports as bl", vtn.run(t, "list-reports", "--as", "bl", "--program-id", programID), 200).objects(t); len(list) != 1 || list[0]["id"] != reportID || list[0]["clientID"] != "interop-ven-1" {
		t.Errorf("Business Logic reads reports %v", list)
	}
	if list := must("list-reports as ven-2", vtn.run(t, "list-reports", "--as", "ven-2"), 200).objects(t); len(list) != 0 {
		t.Errorf("ven-2 sees the report of ven-1: %v", list)
	}
	must("update-report", vtn.run(t, "update-report", "--as", "ven-1", "--id", reportID, "--body", strings.Replace(reportBody, `"usage"`, `"usage-corrected"`, 1)), 200)
	if storedReport, _ = vtn.store.Reports().Get(ctx, reportID); storedReport.Name != "usage-corrected" || storedReport.ClientID != "interop-ven-1" {
		t.Errorf("the corrected report in the store: %+v", storedReport)
	}

	// A VEN client registers itself and a resource; Business Logic grants it a target.
	ven := must("create-ven", vtn.run(t, "create-ven", "--as", "ven-3", "--body", `{"venName":"site-3"}`), 201).object(t)
	venID := ven["id"].(string)
	if ven["clientID"] != "interop-ven-3" {
		t.Errorf("ven as created: %v", ven)
	}
	if r := vtn.run(t, "create-ven", "--as", "ven-3", "--body", `{"venName":"site-1"}`); r.errorCode() != "http-error" || r.status() != 409 {
		t.Errorf("a second ven named site-1: %+v, status %d", r.Error, r.status())
	}
	must("create-resource", vtn.run(t, "create-resource", "--as", "ven-3", "--body", fmt.Sprintf(`{"resourceName":"charger","venID":%q}`, venID)), 201)
	must("update-ven", vtn.run(t, "update-ven", "--as", "bl", "--id", venID, "--body", `{"venName":"site-3","clientID":"interop-ven-3","targets":["group-a"]}`), 200)
	if storedVEN, err := vtn.store.VENs().Get(ctx, venID); err != nil || fmt.Sprint(storedVEN.Targets) != "[group-a]" || storedVEN.ClientID != "interop-ven-3" {
		t.Errorf("the ven in the store after the grant: %+v, %v", storedVEN, err)
	}
	if list := must("list-events as ven-3", vtn.run(t, "list-events", "--as", "ven-3", "--targets", "group-a", "--program-id", programID), 200).objects(t); len(list) != 1 {
		t.Errorf("ven-3 does not see the event after the grant: %v", list)
	}

	must("delete-report", vtn.run(t, "delete-report", "--as", "ven-1", "--id", reportID), 200)
	must("delete-event", vtn.run(t, "delete-event", "--as", "bl", "--id", eventID), 200)
	must("delete-program", vtn.run(t, "delete-program", "--as", "bl", "--id", programID), 200)
	if _, err := vtn.store.Programs().Get(ctx, programID); err == nil {
		t.Error("the program is still in the store after it was deleted")
	}
}
