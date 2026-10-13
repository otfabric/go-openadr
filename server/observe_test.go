// SPDX-License-Identifier: MIT

package server_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/ws"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// recorder is a Metrics with every extension.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func (r *recorder) OnRequest(op string) { r.add("request %s", op) }
func (r *recorder) OnResponse(op string, status int, d time.Duration) {
	if d < 0 {
		r.add("a negative duration")
	}
	r.add("response %s %d", op, status)
}
func (r *recorder) OnNotification(mechanism string, object openadr.ObjectType, operation openadr.Operation) {
	r.add("notification %s %s %s", mechanism, operation, object)
}
func (r *recorder) OnSocketOpened()              { r.add("socket opened") }
func (r *recorder) OnSocketClosed(reason string) { r.add("socket %s", reason) }

type countingPublisher struct{ topics []string }

func (p *countingPublisher) Publish(topic string, _ []byte) { p.topics = append(p.topics, topic) }

func TestMetricsAndRequestLog(t *testing.T) {
	m := &recorder{}
	var log bytes.Buffer
	publisher := &countingPublisher{}
	vtn, err := server.New(server.Config{
		Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token",
		Webhooks: fakeWebhooks{}, MQTT: &server.MQTTConfig{Publisher: publisher, URIs: []string{"mqtt://b"}},
		WebSocket: &server.WebSocketConfig{}, Metrics: m,
		Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vtn.Close() }()

	// Operations are named by their route, whatever the IDs; what is not
	// an operation is "other".
	created := call(vtn, "POST", "/programs", "bl", `{"programName":"tariff"}`)
	id, _ := created.json["id"].(string)
	call(vtn, "GET", "/programs/"+id, "ven", "")
	call(vtn, "GET", "/programs/nope", "ven", "")
	call(vtn, "GET", "/programs", "", "")
	call(vtn, "PATCH", "/programs", "bl", "")
	call(vtn, "GET", "/no/such/path", "bl", "")
	call(vtn, "POST", "/programs", "ven", `{"programName":"x"}`)
	want := []string{
		"request POST /programs", "notification MQTT CREATE PROGRAM", "response POST /programs 201",
		"request GET /programs/{id}", "response GET /programs/{id} 200",
		"request GET /programs/{id}", "response GET /programs/{id} 404",
		"request GET /programs", "response GET /programs 401",
		"request other", "response other 405",
		"request other", "response other 404",
		"request POST /programs", "response POST /programs 403",
	}
	if got := m.take(); !slices.Equal(got, want) {
		t.Errorf("reported\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, line := range []string{
		`level=DEBUG msg="openadr: request" op="POST /programs" status=201 duration=`,
		`op="GET /programs/{id}" status=404`, `op=other status=405`,
	} {
		if !strings.Contains(log.String(), line) {
			t.Errorf("the log lacks %q:\n%s", line, log.String())
		}
	}
	if strings.Contains(log.String(), id) {
		t.Errorf("the ID of an object is in the request log:\n%s", log.String())
	}

	// Notifications, per mechanism. A webhook and a WebSocket subscription
	// of Business Logic to events, and the broker.
	sub := `{"clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"https://example.com/hook"},{"objects":["EVENT"],"operations":["CREATE"],"mechanism":"WEBSOCKET"}]}`
	if r := call(vtn, "POST", "/subscriptions", "bl", sub); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	m.take()

	// A WebSocket is answered when it opens, not when it closes.
	ts := httptest.NewServer(vtn)
	defer ts.Close()
	conn, err := ws.Dial(context.Background(), nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer bl"}})
	if err != nil {
		t.Fatal(err)
	}
	// All of it has been reported by the time the client knows that its
	// WebSocket is open, and in this order.
	wantOpen := []string{"request GET /notifiers/ws", "response GET /notifiers/ws 101", "socket opened"}
	if got := m.take(); !slices.Equal(got, wantOpen) {
		t.Errorf("opening a WebSocket reported %v, want %v", got, wantOpen)
	}

	call(vtn, "POST", "/events", "bl", `{"programID":"`+id+`"}`)
	events := m.take()
	for _, want := range []string{"notification WEBHOOK CREATE EVENT", "notification WEBSOCKET CREATE EVENT", "notification MQTT CREATE EVENT"} {
		if !slices.Contains(events, want) {
			t.Errorf("an event was created, and %q was not reported: %v", want, events)
		}
	}
	if n := len(publisher.topics); n != 4 {
		t.Errorf("%d messages for the broker: %v", n, publisher.topics)
	}

	_ = conn.Close(ws.CloseNormal, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := m.take(); slices.Contains(got, "socket closed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the end of the WebSocket was not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Shut down with one open: the reason says so.
	conn, err = ws.Dial(context.Background(), nil, ts.URL+"/notifiers/ws", http.Header{"Authorization": {"Bearer bl"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ws.CloseNormal, "") }()
	_ = vtn.Close()
	if got := m.take(); !slices.Contains(got, "socket shutdown") {
		t.Errorf("shutting down reported %v", got)
	}

	// Metrics that are only Metrics get the requests and nothing else,
	// and a VTN without any serves as before.
	plain := &onlyRequests{}
	quiet, err := server.New(server.Config{Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token", Metrics: plain})
	if err != nil {
		t.Fatal(err)
	}
	call(quiet, "POST", "/programs", "bl", `{"programName":"tariff"}`)
	if plain.requests != 1 || plain.responses != 1 {
		t.Errorf("%d requests, %d responses", plain.requests, plain.responses)
	}
}

type onlyRequests struct{ requests, responses int }

func (o *onlyRequests) OnRequest(string)                      { o.requests++ }
func (o *onlyRequests) OnResponse(string, int, time.Duration) { o.responses++ }

func TestConfigurationErrors(t *testing.T) {
	_, err := server.New(server.Config{})
	if !errors.Is(err, openadr.ErrConfiguration) || !strings.Contains(err.Error(), "Config.Store") {
		t.Errorf("got %v", err)
	}
}
