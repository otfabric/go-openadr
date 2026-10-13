//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/mqtt"
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/server"
)

// What this module's VTN offers beyond reading and writing objects, used
// by another implementation's client: compressed answers, access without a
// token, subscriptions and the webhook they lead to, MQTT and resource
// groups. The client is the ElaadNL openadr3-client.
//
// Two things no reference client has, and which are therefore not here: a
// receiver of webhooks (the one below is this module's) and a WebSocket
// client.

// goBL and goVEN are this module's clients for a client of the fixture:
// they do what a test needs done quickly, or in a way the reference client
// cannot.
func (v *goVTN) goBL(t *testing.T) *client.BL {
	t.Helper()
	id, secret := v.fx.credentials(t, "bl")
	bl, err := client.NewBL(client.Config{BaseURL: v.local, HTTPClient: v.hc, Credentials: &client.Credentials{ClientID: id, ClientSecret: secret, TokenURL: v.local + "/auth/token"}})
	if err != nil {
		t.Fatal(err)
	}
	return bl
}

func (v *goVTN) goVEN(t *testing.T, name string) *client.VEN {
	t.Helper()
	id, secret := v.fx.credentials(t, name)
	ven, err := client.NewVEN(client.Config{BaseURL: v.local, HTTPClient: v.hc, Credentials: &client.Credentials{ClientID: id, ClientSecret: secret, TokenURL: v.local + "/auth/token"}})
	if err != nil {
		t.Fatal(err)
	}
	return ven
}

// ok fails the test unless the client reports success with that status.
func ok(t *testing.T, what string, r result, status int) result {
	t.Helper()
	if !r.OK || r.status() != status {
		t.Fatalf("%s: exit %d, status %d, error %+v, problem %v", what, r.exit, r.status(), r.Error, r.Problem)
	}
	return r
}

// The ElaadNL client asks for a compressed answer, as the HTTP library
// under it does by default, and reads what this VTN then sends.
func TestElaadNLCompressedResponses(t *testing.T) {
	type exchange struct{ asked, answered string }
	var mu sync.Mutex
	var seen []exchange
	vtn := startGoVTNWith(t, "elaadnl", "baseline", vtnOptions{wrap: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if r.Method == http.MethodGet && r.URL.Path == "/programs" {
				mu.Lock()
				seen = append(seen, exchange{r.Header.Get("Accept-Encoding"), w.Header().Get("Content-Encoding")})
				mu.Unlock()
			}
		})
	}})
	// Enough programs for an answer worth compressing.
	bl := vtn.goBL(t)
	for i := range 20 {
		if _, err := bl.CreateProgram(ctx(t), openadr.Program{Name: fmt.Sprintf("compressed-%02d-%s", i, strings.Repeat("x", 80))}); err != nil {
			t.Fatal(err)
		}
	}

	r := ok(t, "list-programs", vtn.run(t, "list-programs", "--as", "bl"), 200)
	if list := r.objects(t); len(list) != 22 || !strings.HasPrefix(fmt.Sprint(list[21]["programName"]), "compressed-19-") {
		t.Errorf("the client read %d programs from the compressed answer: %s", len(list), r.Response)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || !strings.Contains(seen[0].asked, "gzip") || seen[0].answered != "gzip" {
		t.Errorf("the search of the client: %+v; want one that asks for gzip and is answered with it", seen)
	}
}

// A VTN that answers without a token, and the ElaadNL client without one:
// it reads what is open to everyone and nothing else, and cannot write.
func TestElaadNLAnonymous(t *testing.T) {
	needOperation(t, "elaadnl", "ven", "get-notifiers")
	if !has(t, image("elaadnl"), "clientAnonymous") {
		t.Skipf("%s has no anonymous client", image("elaadnl"))
	}
	vtn := startGoVTNWith(t, "elaadnl", "baseline", vtnOptions{configure: func(cfg *server.Config) { cfg.AllowAnonymous = true }})

	r := ok(t, "list-programs", vtn.run(t, "list-programs", "--anonymous", "ven"), 200)
	if list := r.objects(t); r.Authenticated || len(list) != 1 || list[0]["programName"] != "day-ahead" {
		t.Errorf("programs without a token: authenticated %v, %s; want the one that is not targeted", r.Authenticated, r.Response)
	}
	// The library's Business Logic client without a token is no more than that.
	r = ok(t, "list-events", vtn.run(t, "list-events", "--anonymous", "bl"), 200)
	if list := r.objects(t); len(list) != 1 || list[0]["eventName"] != "day-ahead-prices" {
		t.Errorf("events without a token: %s", r.Response)
	}
	ok(t, "get-program", vtn.run(t, "get-program", "--anonymous", "ven", "--id", "@program:day-ahead@"), 200)
	if r := vtn.run(t, "get-program", "--anonymous", "ven", "--id", "@program:group-a-flex@"); r.status() != 404 {
		t.Errorf("a targeted program without a token: status %d, error %+v; want 404", r.status(), r.Error)
	}
	if r := vtn.run(t, "list-vens", "--anonymous", "ven"); r.status() != 401 || r.Problem == nil {
		t.Errorf("vens without a token: status %d, problem %v; want 401", r.status(), r.Problem)
	}
	if r := vtn.run(t, "create-program", "--anonymous", "bl", "--body", `{"programName":"anonymous"}`); r.errorCode() != "http-error" || r.status() != 401 {
		t.Errorf("a write without a token: status %d, error %+v; want 401", r.status(), r.Error)
	}
	// With a token the same client still is who it is.
	if r := ok(t, "list-programs as bl", vtn.run(t, "list-programs", "--as", "bl"), 200); !r.Authenticated || len(r.objects(t)) != 2 {
		t.Errorf("Business Logic with its token on the same VTN: %s", r.Response)
	}

	// A VTN that was not told to: no token, no answer.
	strict := startGoVTN(t, "elaadnl", "baseline")
	if r := strict.run(t, "list-programs", "--anonymous", "ven"); r.errorCode() != "http-error" || r.status() != 401 {
		t.Errorf("without a token on a VTN that wants one: status %d, error %+v; want 401", r.status(), r.Error)
	}
}

// The ElaadNL VEN client subscribes on this VTN, reads, changes and removes
// its subscription, and what it subscribed to arrives at the callback URL
// it gave: there, this module's webhook handler, because no reference
// client has one.
//
// The library names the operations of a subscription as OpenADR 3.0.1 did
// (GET, POST, PUT, DELETE); 3.1.0 names them CREATE, READ, UPDATE, DELETE.
// DELETE is the one name they share, and so the one subscription this
// client and a 3.1.0 VTN agree on. The rest of the test pins down where
// they do not.
func TestElaadNLSubscriptions(t *testing.T) {
	needOperation(t, "elaadnl", "ven", "create-subscription")
	sender := webhook.NewSender(webhook.SenderConfig{AllowInsecure: true, Backoff: 50 * time.Millisecond})
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sender.Close(c)
	})
	vtn := startGoVTNWith(t, "elaadnl", "baseline", vtnOptions{configure: func(cfg *server.Config) { cfg.Webhooks = sender }})

	received := make(chan openadr.Notification, 16)
	callback := httptest.NewServer(&webhook.Handler{BearerToken: "interop-callback-token",
		Notify: func(_ context.Context, n openadr.Notification) error { received <- n; return nil }})
	t.Cleanup(callback.Close)

	// This VTN has webhooks and no MQTT broker, and says so: WEBHOOK
	// without MQTT, which the specification allows. The library takes an
	// answer without MQTT for one it cannot decode.
	notifiers := vtn.run(t, "get-notifiers", "--as", "ven-1")
	if has(t, image("elaadnl"), "clientNotifiersWithoutMqtt") {
		if ok(t, "get-notifiers", notifiers, 200).object(t)["WEBHOOK"] != true {
			t.Errorf("notifiers: %s", notifiers.Response)
		}
	} else if notifiers.errorCode() != "invalid-response" || notifiers.status() != 200 {
		t.Errorf("notifiers of a VTN without MQTT: status %d, error %+v; the library is expected not to decode them", notifiers.status(), notifiers.Error)
	}

	body := func(name, operation string) string {
		return fmt.Sprintf(`{"clientName":%q,"objectOperations":[{"objects":["PROGRAM","EVENT"],"operations":[%q],"callbackUrl":%q,"bearerToken":"interop-callback-token"}]}`,
			name, operation, callback.URL+"/notifications")
	}
	created := ok(t, "create-subscription", vtn.run(t, "create-subscription", "--as", "ven-1", "--body", body("elaadnl-site-1", "DELETE")), 201).object(t)
	id := created["id"].(string)
	stored, err := vtn.store.Subscriptions().Get(ctx(t), id)
	if err != nil || stored.ClientID != "interop-ven-1" || stored.ClientName != "elaadnl-site-1" || len(stored.ObjectOperations) != 1 ||
		fmt.Sprint(stored.ObjectOperations[0].Operations) != "[DELETE]" || fmt.Sprint(stored.ObjectOperations[0].Objects) != "[PROGRAM EVENT]" ||
		stored.ObjectOperations[0].BearerToken != "interop-callback-token" {
		t.Fatalf("the subscription in the VTN's store: %+v, %v", stored, err)
	}

	// It reads its subscription back, alone and in a search; another VEN
	// does not see it, Business Logic does.
	if got := ok(t, "get-subscription", vtn.run(t, "get-subscription", "--as", "ven-1", "--id", id), 200).object(t); got["clientName"] != "elaadnl-site-1" || got["id"] != id {
		t.Errorf("the subscription as its owner reads it: %v", got)
	}
	if list := ok(t, "list-subscriptions", vtn.run(t, "list-subscriptions", "--as", "ven-1", "--client-name", "elaadnl-site-1"), 200).objects(t); len(list) != 1 || list[0]["id"] != id {
		t.Errorf("the search of the owner: %v", list)
	}
	if list := ok(t, "list-subscriptions as ven-2", vtn.run(t, "list-subscriptions", "--as", "ven-2"), 200).objects(t); len(list) != 0 {
		t.Errorf("ven-2 sees the subscription of ven-1: %v", list)
	}
	if r := vtn.run(t, "get-subscription", "--as", "ven-2", "--id", id); r.status() != 404 {
		t.Errorf("ven-2 reads the subscription of ven-1: status %d", r.status())
	}
	if list := ok(t, "list-subscriptions as bl", vtn.run(t, "list-subscriptions", "--as", "bl"), 200).objects(t); len(list) != 1 {
		t.Errorf("Business Logic sees %d subscriptions", len(list))
	}

	// Business Logic, the ElaadNL client too, removes an event the VEN may
	// see: the notification arrives where the VEN said.
	eventBody := fmt.Sprintf(`{"programID":%q,"eventName":"short-lived","intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.5]}]}]}`, vtn.ids["program:day-ahead"])
	eventID := ok(t, "create-event", vtn.run(t, "create-event", "--as", "bl", "--body", eventBody), 201).object(t)["id"].(string)
	ok(t, "delete-event", vtn.run(t, "delete-event", "--as", "bl", "--id", eventID), 200)
	select {
	case n := <-received:
		event, isEvent := n.Object.(openadr.Event)
		if n.ObjectType != openadr.ObjectEvent || n.Operation != openadr.OperationDelete || !isEvent || event.ID != eventID || event.Name != "short-lived" {
			t.Errorf("the notification at the callback URL: %+v", n)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no notification at the callback URL of the subscription")
	}
	// Its creation was not subscribed to, and did not arrive.
	select {
	case n := <-received:
		t.Errorf("a second notification: %+v", n)
	default:
	}

	// It changes the subscription, and removes it.
	ok(t, "update-subscription", vtn.run(t, "update-subscription", "--as", "ven-1", "--id", id, "--body", body("elaadnl-site-1-renamed", "DELETE")), 200)
	if stored, _ = vtn.store.Subscriptions().Get(ctx(t), id); stored.ClientName != "elaadnl-site-1-renamed" || stored.ClientID != "interop-ven-1" {
		t.Errorf("the subscription in the store after the update: %+v", stored)
	}
	ok(t, "delete-subscription", vtn.run(t, "delete-subscription", "--as", "ven-1", "--id", id), 200)
	if _, err := vtn.store.Subscriptions().Get(ctx(t), id); err == nil {
		t.Error("the subscription is still in the store after it was deleted")
	}

	if has(t, image("elaadnl"), "clientSubscriptionOperations310") {
		return
	}
	// The names of the operations. What the library takes for a creation
	// this VTN refuses, with a problem that says why ...
	if r := vtn.run(t, "create-subscription", "--as", "ven-1", "--body", body("names", "POST")); r.errorCode() != "http-error" || r.status() != 400 ||
		!strings.Contains(fmt.Sprint(r.Problem["detail"]), "CREATE") {
		t.Errorf("a subscription to POST: status %d, error %+v, problem %v; want 400 and a problem that names the operations", r.status(), r.Error, r.Problem)
	}
	// ... what 3.1.0 calls it the library refuses to send ...
	if r := vtn.run(t, "create-subscription", "--as", "ven-1", "--body", body("names", "CREATE")); r.errorCode() != "invalid-request" || r.Status != nil {
		t.Errorf("a subscription to CREATE: status %v, error %+v; want the library to refuse it", r.Status, r.Error)
	}
	// ... and a subscription made with those names the library cannot read.
	theirs, err := vtn.goVEN(t, "ven-2").CreateSubscription(ctx(t), openadr.Subscription{ClientName: "go-site-2", ObjectOperations: []openadr.ObjectOperation{{
		Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate},
		CallbackURL: callback.URL + "/notifications", BearerToken: "interop-callback-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := vtn.run(t, "get-subscription", "--as", "ven-2", "--id", theirs.ID); r.errorCode() != "invalid-response" || r.status() != 200 {
		t.Errorf("a subscription to CREATE and UPDATE as the library reads it: status %d, error %+v; want an answer it cannot decode", r.status(), r.Error)
	}
}

// mqttVTN is this module's VTN publishing to a Mosquitto broker that the
// container of the reference client reaches under the URL the VTN names.
func mqttVTN(t *testing.T) (vtn *goVTN, announced string) {
	t.Helper()
	brokerURL, _ := startBroker(t, "mosquitto", []string{"mosquitto", "-c", "/mosquitto-no-auth.conf"})
	host, _ := hostAccess()
	announced = strings.Replace(brokerURL, "127.0.0.1", host, 1)
	publisher, err := mqtt.NewPublisher(mqtt.PublisherConfig{Broker: mqtt.Broker{URL: brokerURL, ClientID: "go-openadr-vtn", Backoff: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = publisher.Close(c)
	})
	vtn = startGoVTNWith(t, "elaadnl", "baseline", vtnOptions{configure: func(cfg *server.Config) {
		cfg.MQTT = &server.MQTTConfig{Publisher: publisher, URIs: []string{announced}, TopicPrefix: "openadr/"}
	}})
	return vtn, announced
}

// listening is what listen-mqtt reports.
type listening struct {
	Broker   string `json:"broker"`
	Topic    string `json:"topic"`
	Messages []struct {
		Topic   string `json:"topic"`
		Payload struct {
			ObjectType string         `json:"objectType"`
			Operation  string         `json:"operation"`
			Object     map[string]any `json:"object"`
		} `json:"payload"`
	} `json:"messages"`
}

// listenWhile runs listen-mqtt and calls change again and again until the
// client has what it waits for: it cannot be told from outside when the
// client has subscribed, and a notification from before that is not kept.
func listenWhile(t *testing.T, vtn *goVTN, change func(i int), args ...string) listening {
	t.Helper()
	for i := range args {
		args[i] = vtn.expand(t, args[i])
	}
	type outcome struct {
		r   result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := vtn.client.try(append([]string{"listen-mqtt", "--listen-ms", "60000"}, args...)...)
		done <- outcome{r, err}
	}()
	for i := 0; ; i++ {
		select {
		case o := <-done:
			if o.err != nil {
				t.Fatal(o.err)
			}
			var l listening
			if err := json.Unmarshal(ok(t, "listen-mqtt "+strings.Join(args, " "), o.r, 200).Response, &l); err != nil {
				t.Fatalf("listen-mqtt: %v: %s", err, o.r.Response)
			}
			return l
		case <-time.After(250 * time.Millisecond):
			change(i)
		}
	}
}

// The ElaadNL client asks this VTN for its broker and its topics, and its
// MQTT client receives there what the VTN publishes: as Business Logic,
// every new program, and as a VEN, the events meant for it.
func TestElaadNLMQTT(t *testing.T) {
	needOperation(t, "elaadnl", "ven", "listen-mqtt")
	vtn, announced := mqttVTN(t)
	bl := vtn.goBL(t)
	venID := vtn.ids["ven:site-1"]

	binding, _ := ok(t, "get-notifiers", vtn.run(t, "get-notifiers", "--as", "ven-1"), 200).object(t)["MQTT"].(map[string]any)
	if uris, _ := binding["URIS"].([]any); len(uris) != 1 || uris[0] != announced || binding["serialization"] != "JSON" {
		t.Fatalf("the MQTT binding as the library reads it: %v", binding)
	}

	topics := func(r result) map[string]any {
		t.Helper()
		got, _ := r.object(t)["topics"].(map[string]any)
		return got
	}
	if got := topics(ok(t, "get-topics programs", vtn.run(t, "get-topics", "--as", "bl", "--scope", "programs"), 200)); got["CREATE"] != "openadr/programs/create" ||
		got["UPDATE"] != "openadr/programs/update" || got["DELETE"] != "openadr/programs/delete" || got["ALL"] != "openadr/programs/+" {
		t.Errorf("topics of all programs: %v", got)
	}
	for _, scope := range []string{"events", "reports", "subscriptions", "vens", "resources"} {
		if got := topics(ok(t, "get-topics "+scope, vtn.run(t, "get-topics", "--as", "bl", "--scope", scope), 200)); got["CREATE"] != "openadr/"+scope+"/create" {
			t.Errorf("topics of all %s: %v", scope, got)
		}
	}
	if got := topics(ok(t, "get-topics program-events", vtn.run(t, "get-topics", "--as", "bl", "--scope", "program-events", "--id", "@program:day-ahead@"), 200)); got["CREATE"] != "openadr/programs/"+vtn.ids["program:day-ahead"]+"/events/create" {
		t.Errorf("topics of the events of a program: %v", got)
	}
	for _, scope := range []string{"ven-events", "ven-programs", "ven-resources"} {
		want := "openadr/vens/" + venID + "/" + strings.TrimPrefix(scope, "ven-") + "/create"
		if got := topics(ok(t, "get-topics "+scope, vtn.run(t, "get-topics", "--as", "ven-1", "--scope", scope, "--id", venID), 200)); got["CREATE"] != want {
			t.Errorf("topics %s of the VEN: %v, want CREATE %s", scope, got, want)
		}
	}
	// What is Business Logic's to ask a VEN is refused, and so are the
	// topics of another VEN.
	if r := vtn.run(t, "get-topics", "--as", "ven-1", "--scope", "programs"); r.status() != 403 {
		t.Errorf("topics of all programs for a VEN: status %d, want 403", r.status())
	}
	if r := vtn.run(t, "get-topics", "--as", "ven-2", "--scope", "ven-events", "--id", venID); r.status() != 403 && r.status() != 404 {
		t.Errorf("topics of another VEN: status %d, want a refusal", r.status())
	}
	// One object has no topic for its own creation, as the specification
	// says, and this VTN names none. The library insists on one.
	one := vtn.run(t, "get-topics", "--as", "bl", "--scope", "program", "--id", "@program:day-ahead@")
	if has(t, image("elaadnl"), "clientTopicsOfOneObject") {
		if got := topics(ok(t, "get-topics program", one, 200)); got["UPDATE"] != "openadr/programs/"+vtn.ids["program:day-ahead"]+"/update" {
			t.Errorf("topics of one program: %v", got)
		}
	} else if one.errorCode() != "invalid-response" || one.status() != 200 {
		t.Errorf("topics of one program: status %d, error %+v; the library is expected not to decode them", one.status(), one.Error)
	}

	// Business Logic listens for new programs.
	l := listenWhile(t, vtn, func(i int) {
		if _, err := bl.CreateProgram(ctx(t), openadr.Program{Name: fmt.Sprintf("announced-%d", i)}); err != nil {
			t.Error(err)
		}
	}, "--as", "bl", "--scope", "programs", "--topic-operation", "create", "--messages", "2")
	if l.Broker != announced && l.Broker != announced+"/" || l.Topic != "openadr/programs/create" || len(l.Messages) != 2 {
		t.Fatalf("listening for programs: %+v", l)
	}
	for _, m := range l.Messages {
		if m.Topic != "openadr/programs/create" || m.Payload.ObjectType != "PROGRAM" || m.Payload.Operation != "CREATE" ||
			!strings.HasPrefix(fmt.Sprint(m.Payload.Object["programName"]), "announced-") || m.Payload.Object["id"] == nil {
			t.Errorf("a notification of a new program: %+v", m)
		}
	}

	// A VEN listens on its own topic: it is told of an event for a target
	// it was granted, and not of one for another target.
	n := 0
	l = listenWhile(t, vtn, func(i int) {
		for _, target := range []string{"group-b", "group-a"} {
			n++
			if _, err := bl.CreateEvent(ctx(t), openadr.Event{ProgramID: vtn.ids["program:group-a-flex"], Name: fmt.Sprintf("for-%s-%d", target, i), Targets: []string{target},
				Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.2)}}}}}}); err != nil {
				t.Error(err)
			}
		}
	}, "--as", "ven-1", "--scope", "ven-events", "--id", venID, "--messages", "2")
	if l.Topic != "openadr/vens/"+venID+"/events/+" || len(l.Messages) != 2 {
		t.Fatalf("listening for the events of the VEN: %+v", l)
	}
	for _, m := range l.Messages {
		if m.Topic != "openadr/vens/"+venID+"/events/create" || m.Payload.ObjectType != "EVENT" ||
			!strings.HasPrefix(fmt.Sprint(m.Payload.Object["eventName"]), "for-group-a-") {
			t.Errorf("a notification on the topic of the VEN: %+v", m)
		}
	}
}

// Resource groups, an extension this VTN shares with OpenLEADR, through
// the client the ElaadNL library has for them: Business Logic groups
// resources, and a VEN sees the groups that hold one of its own.
func TestElaadNLResourceGroups(t *testing.T) {
	needOperation(t, "elaadnl", "bl", "create-resource-group")
	vtn := startGoVTNWith(t, "elaadnl", "baseline", vtnOptions{configure: func(cfg *server.Config) { cfg.ResourceGroups = true }})

	resources := ok(t, "list-resources", vtn.run(t, "list-resources", "--as", "bl"), 200).objects(t)
	if len(resources) != 1 {
		t.Fatalf("resources of the fixture: %v", resources)
	}
	resourceID := resources[0]["id"].(string)

	street := ok(t, "create-resource-group", vtn.run(t, "create-resource-group", "--as", "bl", "--body",
		fmt.Sprintf(`{"resourceGroupName":"street","targets":["group-a"],"children":[{"type":"ven_resource","id":%q}]}`, resourceID)), 201).object(t)
	streetID := street["id"].(string)
	stored, err := vtn.store.ResourceGroups().Get(ctx(t), streetID)
	if err != nil || stored.Name != "street" || fmt.Sprint(stored.Targets) != "[group-a]" || len(stored.Children) != 1 ||
		stored.Children[0] != (openadr.ResourceGroupChild{Type: openadr.ChildResource, ID: resourceID}) {
		t.Fatalf("the group in the VTN's store: %+v, %v", stored, err)
	}
	district := ok(t, "create-resource-group of groups", vtn.run(t, "create-resource-group", "--as", "bl", "--body",
		fmt.Sprintf(`{"resourceGroupName":"district","children":[{"type":"resource_group","id":%q}]}`, streetID)), 201).object(t)
	districtID := district["id"].(string)

	if got := ok(t, "get-resource-group", vtn.run(t, "get-resource-group", "--as", "bl", "--id", streetID), 200).object(t); got["resourceGroupName"] != "street" || got["objectType"] != "RESOURCE_GROUP" || len(got["children"].([]any)) != 1 {
		t.Errorf("the group as Business Logic reads it: %v", got)
	}
	if list := ok(t, "list-resource-groups", vtn.run(t, "list-resource-groups", "--as", "bl"), 200).objects(t); len(list) != 2 {
		t.Errorf("Business Logic finds %d groups, want 2", len(list))
	}
	if list := ok(t, "list-resource-groups by name", vtn.run(t, "list-resource-groups", "--as", "bl", "--resource-group-name", "district"), 200).objects(t); len(list) != 1 || list[0]["id"] != districtID {
		t.Errorf("the search by name: %v", list)
	}
	if list := ok(t, "list-resource-groups by target", vtn.run(t, "list-resource-groups", "--as", "bl", "--targets", "group-a", "--skip", "0", "--limit", "5"), 200).objects(t); len(list) != 1 || list[0]["id"] != streetID {
		t.Errorf("the search by target: %v", list)
	}
	if r := vtn.run(t, "create-resource-group", "--as", "bl", "--body", `{"resourceGroupName":"broken","children":[{"type":"ven_resource","id":"no-such-resource"}]}`); r.errorCode() != "http-error" || r.status() < 400 || r.status() >= 500 {
		t.Errorf("a group of a resource that is not there: status %d, error %+v", r.status(), r.Error)
	}

	// The VEN whose resource it is sees the group, with its own resource
	// in it; another VEN sees neither.
	mine := ok(t, "list-resource-groups as ven-1", vtn.run(t, "list-resource-groups", "--as", "ven-1", "--targets", "group-a"), 200).objects(t)
	if len(mine) != 1 || mine[0]["id"] != streetID || len(mine[0]["children"].([]any)) != 1 {
		t.Errorf("the groups ven-1 sees: %v", mine)
	}
	ok(t, "get-resource-group as ven-1", vtn.run(t, "get-resource-group", "--as", "ven-1", "--id", streetID), 200)
	if list := ok(t, "list-resource-groups as ven-2", vtn.run(t, "list-resource-groups", "--as", "ven-2", "--targets", "group-a"), 200).objects(t); len(list) != 0 {
		t.Errorf("ven-2 sees groups: %v", list)
	}
	if r := vtn.run(t, "get-resource-group", "--as", "ven-2", "--id", streetID); r.status() != 404 {
		t.Errorf("ven-2 reads the group of ven-1's resource: status %d, want 404", r.status())
	}

	ok(t, "update-resource-group", vtn.run(t, "update-resource-group", "--as", "bl", "--id", streetID, "--body",
		fmt.Sprintf(`{"resourceGroupName":"street-renamed","targets":["group-a"],"children":[{"type":"ven_resource","id":%q}]}`, resourceID)), 200)
	if stored, _ = vtn.store.ResourceGroups().Get(ctx(t), streetID); stored.Name != "street-renamed" || len(stored.Children) != 1 {
		t.Errorf("the group in the store after the update: %+v", stored)
	}
	ok(t, "delete-resource-group", vtn.run(t, "delete-resource-group", "--as", "bl", "--id", districtID), 200)
	ok(t, "delete-resource-group", vtn.run(t, "delete-resource-group", "--as", "bl", "--id", streetID), 200)
	if r := vtn.run(t, "get-resource-group", "--as", "bl", "--id", streetID); r.status() != 404 {
		t.Errorf("the group after it was deleted: status %d", r.status())
	}

	// A VTN that was not told to serve them does not have the path.
	plain := startGoVTN(t, "elaadnl", "baseline")
	if r := plain.run(t, "list-resource-groups", "--as", "bl"); r.errorCode() != "http-error" || r.status() != 404 {
		t.Errorf("resource groups on a VTN without them: status %d, error %+v; want 404", r.status(), r.Error)
	}
}
