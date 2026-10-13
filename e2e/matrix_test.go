// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/server"
)

// fixture is a VTN with one object of every kind, the VEN-side ones made by
// ven-1.
type fixture struct {
	*stack
	program, idle, event, report, ven, resource, subscription, group string
	hook                                                             string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{stack: start(t, options{mqtt: true, groups: true, websocket: &server.WebSocketConfig{}})}
	f.program = mustProgram(t, f.BL, openadr.Program{Name: "tariff"}).ID
	f.idle = mustProgram(t, f.BL, openadr.Program{Name: "without-events"}).ID
	f.event = mustEvent(t, f.BL, openadr.Event{ProgramID: f.program}).ID
	report, err := f.VEN1.CreateReport(ctx, openadr.Report{EventID: f.event, ClientName: "one", Resources: []openadr.ReportResource{}})
	if err != nil {
		t.Fatal(err)
	}
	f.report = report.ID
	ven, err := f.VEN1.CreateVEN(ctx, openadr.VEN{Name: "site-1"})
	if err != nil {
		t.Fatal(err)
	}
	f.ven = ven.ID
	resource, err := f.VEN1.CreateResource(ctx, openadr.Resource{VENID: f.ven, Name: "battery"})
	if err != nil {
		t.Fatal(err)
	}
	f.resource = resource.ID
	hook := newHook(t, "")
	f.hook = hook.URL
	f.subscription = subscribe(t, f.VEN1.CreateSubscription, hook, "", openadr.Subscription{ObjectOperations: all(openadr.ObjectVEN)}).ID
	group, err := f.BL.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "feeder-7",
		Children: []openadr.ResourceGroupChild{{Type: openadr.ChildResource, ID: f.resource}}})
	if err != nil {
		t.Fatal(err)
	}
	f.group = group.ID
	return f
}

// Every operation of the API, as each kind of client: nobody, a client
// with a token the VTN did not issue, the VEN the objects belong to,
// another VEN, and Business Logic. One fresh VTN per request, so that no
// request sees what another did.
//
// This is the VTN's access control in one table. A change to it shows here
// as a changed number.
func TestAuthorizationMatrix(t *testing.T) {
	const (
		subscriptionBody = `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"%s"}]}`
		reportBody       = `{"eventID":"%s","clientName":"c","resources":[]}`
	)
	type request func(f fixture) (method, path, body string)
	get := func(path string, args ...func(fixture) string) request {
		return func(f fixture) (string, string, string) { return "GET", expand(path, f, args), "" }
	}
	send := func(method, path, body string, args ...func(fixture) string) request {
		return func(f fixture) (string, string, string) {
			return method, expand(path, f, args), expand(body, f, args[strings.Count(path, "%s"):])
		}
	}
	program := func(f fixture) string { return f.program }
	idle := func(f fixture) string { return f.idle }
	event := func(f fixture) string { return f.event }
	report := func(f fixture) string { return f.report }
	ven := func(f fixture) string { return f.ven }
	resource := func(f fixture) string { return f.resource }
	subscription := func(f fixture) string { return f.subscription }
	hook := func(f fixture) string { return f.hook }
	group := func(f fixture) string { return f.group }
	const groupBody = `{"resourceGroupName":"renamed","children":[{"type":"ven_resource","id":"%s"}]}`

	rows := []struct {
		name string
		req  request
		// Expected status for: no token, forged token, the owning VEN,
		// another VEN, Business Logic.
		want [5]int
	}{
		{"find the token endpoint", get("/auth/server"), [5]int{200, 200, 200, 200, 200}},

		{"search programs", get("/programs"), [5]int{401, 401, 200, 200, 200}},
		{"read a program", get("/programs/%s", program), [5]int{401, 401, 200, 200, 200}},
		{"create a program", send("POST", "/programs", `{"programName":"new"}`), [5]int{401, 401, 403, 403, 201}},
		{"update a program", send("PUT", "/programs/%s", `{"programName":"renamed"}`, program), [5]int{401, 401, 403, 403, 200}},
		{"delete a program", send("DELETE", "/programs/%s", "", idle), [5]int{401, 401, 403, 403, 200}},
		{"delete a program that has events", send("DELETE", "/programs/%s", "", program), [5]int{401, 401, 403, 403, 409}},

		{"search events", get("/events"), [5]int{401, 401, 200, 200, 200}},
		{"read an event", get("/events/%s", event), [5]int{401, 401, 200, 200, 200}},
		{"create an event", send("POST", "/events", `{"programID":"%s"}`, program), [5]int{401, 401, 403, 403, 201}},
		{"update an event", send("PUT", "/events/%s", `{"programID":"%s"}`, event, program), [5]int{401, 401, 403, 403, 200}},
		{"delete an event", send("DELETE", "/events/%s", "", event), [5]int{401, 401, 403, 403, 200}},

		// Reports are written by VENs, each its own; Business Logic reads.
		{"search reports", get("/reports"), [5]int{401, 401, 200, 200, 200}},
		{"read a report", get("/reports/%s", report), [5]int{401, 401, 200, 404, 200}},
		{"create a report", send("POST", "/reports", reportBody, event), [5]int{401, 401, 201, 201, 403}},
		{"update a report", send("PUT", "/reports/%s", reportBody, report, event), [5]int{401, 401, 200, 404, 403}},
		{"delete a report", send("DELETE", "/reports/%s", "", report), [5]int{401, 401, 200, 404, 403}},

		{"search vens", get("/vens"), [5]int{401, 401, 200, 200, 200}},
		{"read a ven", get("/vens/%s", ven), [5]int{401, 401, 200, 404, 200}},
		{"register as a ven", send("POST", "/vens", `{"objectType":"VEN_VEN_REQUEST","venName":"new"}`), [5]int{401, 401, 201, 201, 400}},
		{"create a ven with a client ID and targets", send("POST", "/vens", `{"objectType":"BL_VEN_REQUEST","venName":"new","clientID":"ven-2","targets":["a"]}`), [5]int{401, 401, 403, 403, 201}},
		{"update a ven", send("PUT", "/vens/%s", `{"objectType":"VEN_VEN_REQUEST","venName":"renamed"}`, ven), [5]int{401, 401, 200, 404, 400}},
		{"grant a ven targets", send("PUT", "/vens/%s", `{"objectType":"BL_VEN_REQUEST","venName":"site-1","clientID":"ven-1","targets":["a"]}`, ven), [5]int{401, 401, 403, 404, 200}},
		{"delete a ven that has resources", send("DELETE", "/vens/%s", "", ven), [5]int{401, 401, 409, 404, 409}},

		{"search resources", get("/resources"), [5]int{401, 401, 200, 200, 200}},
		{"read a resource", get("/resources/%s", resource), [5]int{401, 401, 200, 404, 200}},
		// Another VEN naming this ven is told it does not exist.
		{"add a resource to a ven", send("POST", "/resources", `{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"charger","venID":"%s"}`, ven), [5]int{401, 401, 201, 409, 400}},
		{"update a resource", send("PUT", "/resources/%s", `{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"renamed","venID":"%s"}`, resource, ven), [5]int{401, 401, 200, 404, 400}},
		{"delete a resource", send("DELETE", "/resources/%s", "", resource), [5]int{401, 401, 200, 404, 200}},

		{"search subscriptions", get("/subscriptions"), [5]int{401, 401, 200, 200, 200}},
		{"read a subscription", get("/subscriptions/%s", subscription), [5]int{401, 401, 200, 404, 200}},
		{"subscribe", send("POST", "/subscriptions", subscriptionBody, hook), [5]int{401, 401, 201, 201, 201}},
		{"update a subscription", send("PUT", "/subscriptions/%s", subscriptionBody, subscription, hook), [5]int{401, 401, 200, 404, 200}},
		{"delete a subscription", send("DELETE", "/subscriptions/%s", "", subscription), [5]int{401, 401, 200, 404, 200}},

		{"ask how the VTN notifies", get("/notifiers"), [5]int{401, 401, 200, 200, 200}},
		// Topics that carry every object are for Business Logic; those of a
		// ven for its VEN.
		{"topics of all programs", get("/notifiers/mqtt/topics/programs"), [5]int{401, 401, 403, 403, 200}},
		{"topics of one program", get("/notifiers/mqtt/topics/programs/%s", program), [5]int{401, 401, 403, 403, 200}},
		{"topics of a program's events", get("/notifiers/mqtt/topics/programs/%s/events", program), [5]int{401, 401, 403, 403, 200}},
		{"topics of all events", get("/notifiers/mqtt/topics/events"), [5]int{401, 401, 403, 403, 200}},
		{"topics of all reports", get("/notifiers/mqtt/topics/reports"), [5]int{401, 401, 403, 403, 200}},
		{"topics of all subscriptions", get("/notifiers/mqtt/topics/subscriptions"), [5]int{401, 401, 403, 403, 200}},
		{"topics of all vens", get("/notifiers/mqtt/topics/vens"), [5]int{401, 401, 403, 403, 200}},
		{"topics of all resources", get("/notifiers/mqtt/topics/resources"), [5]int{401, 401, 403, 403, 200}},
		{"topics of one ven", get("/notifiers/mqtt/topics/vens/%s", ven), [5]int{401, 401, 200, 404, 200}},
		{"topics of a ven's events", get("/notifiers/mqtt/topics/vens/%s/events", ven), [5]int{401, 401, 200, 404, 200}},
		{"topics of a ven's programs", get("/notifiers/mqtt/topics/vens/%s/programs", ven), [5]int{401, 401, 200, 404, 200}},
		{"topics of a ven's resources", get("/notifiers/mqtt/topics/vens/%s/resources", ven), [5]int{401, 401, 200, 404, 200}},

		// What is not of OpenADR 3.1.0. Resource groups are Business
		// Logic's to write; a VEN reads those that hold a resource of its.
		{"search resource groups", get("/resource_groups"), [5]int{401, 401, 200, 200, 200}},
		{"read a resource group", get("/resource_groups/%s", group), [5]int{401, 401, 200, 404, 200}},
		{"create a resource group", send("POST", "/resource_groups", groupBody, resource), [5]int{401, 401, 403, 403, 201}},
		{"update a resource group", send("PUT", "/resource_groups/%s", groupBody, group, resource), [5]int{401, 401, 403, 403, 200}},
		{"delete a resource group", send("DELETE", "/resource_groups/%s", "", group), [5]int{401, 401, 403, 403, 200}},
		{"subscribe to a WebSocket", send("POST", "/subscriptions", `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET"}]}`), [5]int{401, 401, 201, 201, 201}},
		// An ordinary request to the address of the WebSocket.
		{"ask for the WebSocket without opening one", get("/notifiers/ws"), [5]int{401, 401, 426, 426, 426}},
	}
	// The 45 operations of OpenADR 3.1.0, less POST /auth/token, which is
	// the token service's; plus the rows that show a second face of one.
	if len(rows) < 44 {
		t.Fatalf("the matrix has %d rows; every operation needs one", len(rows))
	}

	clients := []string{"no token", "forged token", "owning VEN", "another VEN", "Business Logic"}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for i, who := range clients {
				f := newFixture(t)
				token := map[int]string{1: "not-a-token-of-this-vtn"}[i]
				if id := map[int]string{2: "ven-1", 3: "ven-2", 4: "bl"}[i]; id != "" {
					token = f.token(t, id)
				}
				method, path, body := row.req(f)
				req, _ := http.NewRequest(method, f.URL+path, strings.NewReader(body))
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, err := f.HTTP.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				answer, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != row.want[i] {
					t.Errorf("%s %s as %s: %d, want %d (%s)", method, path, who, resp.StatusCode, row.want[i], strings.TrimSpace(string(answer)))
				}
				// Whoever is refused is told so in a problem object.
				if resp.StatusCode >= 400 && !strings.Contains(string(answer), fmt.Sprintf(`"status":%d`, resp.StatusCode)) {
					t.Errorf("%s %s as %s: a %d without a problem object: %s", method, path, who, resp.StatusCode, answer)
				}
			}
		})
	}
}

// expand fills the %s of a template with values of the fixture.
func expand(template string, f fixture, args []func(fixture) string) string {
	n := strings.Count(template, "%s")
	values := make([]any, n)
	for i := range n {
		values[i] = args[i](f)
	}
	return fmt.Sprintf(template, values...)
}
