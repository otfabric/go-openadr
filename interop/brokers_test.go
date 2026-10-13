//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	imqtt "github.com/otfabric/go-openadr/internal/mqtt"
	"github.com/otfabric/go-openadr/notification/mqtt"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// The MQTT client of this module is its own. These tests put both of its
// uses, the VTN's publisher and a client's subscriber, on brokers other
// people wrote: the VTN of this module publishes its notifications to the
// broker in a container, and this module's subscriber receives them from
// it. Each broker runs as its maker ships it, apart from what is needed to
// let a client in without a password.

// brokers are the brokers to test on, and how each is started.
var brokers = []struct {
	name string
	// args go after the image, for a broker that needs telling.
	args []string
}{
	// Mosquitto 2 listens on loopback only unless given a configuration;
	// the image ships this one, which opens port 1883 to everyone.
	{name: "mosquitto", args: []string{"mosquitto", "-c", "/mosquitto-no-auth.conf"}},
	{name: "nanomq"},
	{name: "hivemq"},
	{name: "emqx"},
}

// startBroker runs a broker on a loopback port of the host, waits until it
// takes MQTT connections, and returns its URL and the container.
func startBroker(t *testing.T, name string, args []string) (url, container string) {
	t.Helper()
	img := needImage(t, name)
	port := freePort(t)
	url = fmt.Sprintf("mqtt://127.0.0.1:%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := append([]string{"run", "-d", "-p", fmt.Sprintf("127.0.0.1:%d:1883", port), img}, args...)
	out, exit, err := docker(ctx, run...)
	if err != nil || exit != 0 {
		t.Fatalf("starting %s: exit %d, %v", name, exit, err)
	}
	container = strings.TrimSpace(string(out))
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "40", container).CombinedOutput()
			t.Logf("%s log:\n%s", name, logs)
		}
		_ = exec.Command("docker", "rm", "-f", container).Run()
	})
	waitForBroker(t, ctx, name, url)
	return url, container
}

// waitForBroker waits until a broker answers an MQTT CONNECT. An open port
// is not that: the port of a container is open before the broker is up.
func waitForBroker(t *testing.T, ctx context.Context, name, url string) {
	t.Helper()
	var last error
	for ctx.Err() == nil {
		dial, cancel := context.WithTimeout(ctx, 3*time.Second)
		conn, err := imqtt.Dial(dial, url, imqtt.Options{ClientID: "go-openadr-probe"})
		cancel()
		if err == nil {
			_ = conn.Close()
			return
		}
		last = err
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s took no MQTT connection in time: %v", name, last)
}

// brokerStack is this module's VTN publishing to a broker, with a Business
// Logic client and a VEN that has a ven object.
type brokerStack struct {
	bl    *client.BL
	ven   *client.VEN
	venID string
}

func startVTNOn(t *testing.T, brokerURL string) brokerStack {
	t.Helper()
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
		{ID: "bl", Secret: "bl-secret", Scopes: auth.BusinessLogicScopes()},
		{ID: "ven-1", Secret: "ven-1-secret", Scopes: auth.VENScopes()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := mqtt.NewPublisher(mqtt.PublisherConfig{Broker: mqtt.Broker{URL: brokerURL, ClientID: "go-openadr-vtn", Backoff: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = publisher.Close(ctx)
	})
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	vtn, err := server.New(server.Config{
		Store: storage.NewMemory(), Validator: issuer, TokenHandler: issuer, TokenURL: ts.URL + "/auth/token",
		MQTT: &server.MQTTConfig{Publisher: publisher, URIs: []string{brokerURL}, TopicPrefix: "openadr/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", vtn)

	var s brokerStack
	credentials := func(id string) client.Config {
		return client.Config{BaseURL: ts.URL, Credentials: &client.Credentials{ClientID: id, ClientSecret: id + "-secret"}}
	}
	if s.bl, err = client.NewBL(credentials("bl")); err != nil {
		t.Fatal(err)
	}
	if s.ven, err = client.NewVEN(credentials("ven-1")); err != nil {
		t.Fatal(err)
	}
	ven, err := s.bl.CreateVEN(ctx(t), openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}})
	if err != nil {
		t.Fatal(err)
	}
	s.venID = ven.ID
	return s
}

// subscribeOn subscribes to topics on a broker, with room for a burst.
func subscribeOn(t *testing.T, url, clientID string, topics ...string) (*mqtt.Subscriber, chan openadr.Notification) {
	t.Helper()
	received := make(chan openadr.Notification, 4096)
	sub, err := mqtt.NewSubscriber(mqtt.SubscriberConfig{
		Broker: mqtt.Broker{URL: url, ClientID: clientID, Backoff: 100 * time.Millisecond}, Topics: topics,
		Notify: func(_ context.Context, _ string, n openadr.Notification) { received <- n },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	ready, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sub.Ready(ready); err != nil {
		t.Fatalf("subscribing to %v as %s: %v", topics, clientID, err)
	}
	return sub, received
}

func TestMQTTBrokers(t *testing.T) {
	for _, b := range brokers {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			url, container := startBroker(t, b.name, b.args)
			vtn := startVTNOn(t, url)
			c := ctx(t)

			// The clients ask the VTN where to listen, as they would.
			notifiers, err := vtn.ven.Notifiers(c)
			if err != nil || notifiers.MQTT == nil || notifiers.MQTT.URIs[0] != url {
				t.Fatalf("notifiers: %+v, %v", notifiers, err)
			}
			mine, err := vtn.ven.MQTTTopics(c, client.EventsForVEN(vtn.venID))
			if err != nil {
				t.Fatal(err)
			}
			all, err := vtn.bl.MQTTTopics(c, client.AllEvents())
			if err != nil {
				t.Fatal(err)
			}
			venSub, forVEN := subscribeOn(t, notifiers.MQTT.URIs[0], "go-openadr-ven-1", mine.All)
			_, forBL := subscribeOn(t, notifiers.MQTT.URIs[0], "go-openadr-bl", all.Create, all.Update, all.Delete)

			next := func(who string, ch chan openadr.Notification) (openadr.Notification, openadr.Event) {
				t.Helper()
				select {
				case n := <-ch:
					e, ok := n.Object.(openadr.Event)
					if !ok {
						t.Fatalf("%s: a notification of a %s", who, n.ObjectType)
					}
					return n, e
				case <-time.After(20 * time.Second):
					t.Fatalf("%s: no notification arrived", who)
					return openadr.Notification{}, openadr.Event{}
				}
			}

			// Created, changed, withdrawn: each arrives, in that order, at
			// Business Logic with every target and at the VEN with its own.
			program, err := vtn.bl.CreateProgram(c, openadr.Program{Name: "flex", Targets: []string{"group-a", "group-b"}})
			if err != nil {
				t.Fatal(err)
			}
			event, err := vtn.bl.CreateEvent(c, openadr.Event{ProgramID: program.ID, Name: "first", Targets: []string{"group-a", "group-b"}})
			if err != nil {
				t.Fatal(err)
			}
			event.Name = "first, revised"
			if _, err := vtn.bl.UpdateEvent(c, event); err != nil {
				t.Fatal(err)
			}
			if _, err := vtn.bl.DeleteEvent(c, event.ID); err != nil {
				t.Fatal(err)
			}
			for _, want := range []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete} {
				if n, e := next("Business Logic", forBL); n.Operation != want || e.ID != event.ID || len(e.Targets) != 2 {
					t.Errorf("Business Logic: %s of %s with targets %v, want %s", n.Operation, e.Name, e.Targets, want)
				}
				if n, e := next("the VEN", forVEN); n.Operation != want || e.ID != event.ID || len(e.Targets) != 1 || e.Targets[0] != "group-a" {
					t.Errorf("the VEN: %s of %s with targets %v, want %s with group-a", n.Operation, e.Name, e.Targets, want)
				}
			}
			// An event for another group does not reach the VEN's topic.
			if _, err := vtn.bl.CreateEvent(c, openadr.Event{ProgramID: program.ID, Name: "not for ven-1", Targets: []string{"group-b"}}); err != nil {
				t.Fatal(err)
			}
			if _, e := next("Business Logic", forBL); e.Name != "not for ven-1" {
				t.Errorf("Business Logic was told of %q", e.Name)
			}
			select {
			case n := <-forVEN:
				t.Errorf("the VEN was told of an event of another group: %+v", n.Object)
			case <-time.After(500 * time.Millisecond):
			}

			// A burst: none lost, none out of order.
			const burst = 200
			for i := range burst {
				if _, err := vtn.bl.CreateEvent(c, openadr.Event{ProgramID: program.ID, Name: fmt.Sprintf("burst-%03d", i)}); err != nil {
					t.Fatal(err)
				}
			}
			for who, ch := range map[string]chan openadr.Notification{"Business Logic": forBL, "the VEN": forVEN} {
				for i := range burst {
					if _, e := next(who, ch); e.Name != fmt.Sprintf("burst-%03d", i) {
						t.Fatalf("%s: notification %d of the burst is of %q", who, i, e.Name)
					}
				}
			}

			// A large one: an event of a thousand intervals, some 60 kB.
			big := openadr.Event{ProgramID: program.ID, Name: "big", IntervalPeriod: &openadr.IntervalPeriod{Duration: &openadr.Duration{Minutes: 15}}}
			at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			big.IntervalPeriod.Start = &at
			for i := range 1000 {
				big.Intervals = append(big.Intervals, openadr.Interval{ID: int32(i), Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(float64(i) / 1000)}}}})
			}
			if _, err := vtn.bl.CreateEvent(c, big); err != nil {
				t.Fatal(err)
			}
			for who, ch := range map[string]chan openadr.Notification{"Business Logic": forBL, "the VEN": forVEN} {
				_, e := next(who, ch)
				if last, _ := e.Intervals[len(e.Intervals)-1].Payloads[0].Values[0].Number(); e.Name != "big" || len(e.Intervals) != 1000 || last != 0.999 {
					t.Errorf("%s: the large event arrived as %q with %d intervals", who, e.Name, len(e.Intervals))
				}
			}

			// The broker goes away and comes back: so do the publisher and
			// the subscriber, and what is published after arrives.
			sessions := venSub.Sessions()
			restart, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, exit, err := docker(restart, "restart", container); err != nil || exit != 0 {
				t.Fatalf("restarting %s: exit %d, %v", b.name, exit, err)
			}
			waitForBroker(t, restart, b.name, url)
			deadline := time.Now().Add(time.Minute)
			for venSub.Sessions() == sessions {
				if time.Now().After(deadline) {
					t.Fatal("the subscriber did not connect again after the restart")
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err := venSub.Ready(restart); err != nil {
				t.Fatal(err)
			}
			// The publisher finds out that its connection is gone when it
			// next uses it, and then sends again what it could not: so
			// events are made until one arrives.
			for len(forVEN) > 0 {
				<-forVEN
			}
			arrived := false
			for i := 0; i < 60 && !arrived; i++ {
				if _, err := vtn.bl.CreateEvent(c, openadr.Event{ProgramID: program.ID, Name: fmt.Sprintf("after-restart-%d", i)}); err != nil {
					t.Fatal(err)
				}
				select {
				case n := <-forVEN:
					arrived = strings.HasPrefix(n.Object.(openadr.Event).Name, "after-restart-")
				case <-time.After(500 * time.Millisecond):
				}
			}
			if !arrived {
				t.Error("nothing published after the restart arrived")
			}
		})
	}
}
