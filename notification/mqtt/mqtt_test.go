// SPDX-License-Identifier: MIT

package mqtt

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/mqtttest"
)

const eventNotification = `{"objectType":"EVENT","operation":"CREATE","object":{"id":"e1","objectType":"EVENT",
	"createdDateTime":"2026-01-01T00:00:00Z","modificationDateTime":"2026-01-01T00:00:00Z","programID":"p1","eventName":"prices"}}`

func TestConfig(t *testing.T) {
	notify := func(context.Context, string, openadr.Notification) {}
	for name, cfg := range map[string]SubscriberConfig{
		"no URL":          {Topics: []string{"a"}, Notify: notify},
		"not a broker":    {Broker: Broker{URL: "https://example.com"}, Topics: []string{"a"}, Notify: notify},
		"no topics":       {Broker: Broker{URL: "mqtt://example.com"}, Notify: notify},
		"an empty topic":  {Broker: Broker{URL: "mqtt://example.com"}, Topics: []string{"a", ""}, Notify: notify},
		"nothing to call": {Broker: Broker{URL: "mqtt://example.com"}, Topics: []string{"a"}},
	} {
		if _, err := NewSubscriber(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewPublisher(PublisherConfig{Broker: Broker{URL: "broker.example.com"}}); err == nil {
		t.Error("a Publisher without a scheme in its URL was accepted")
	}
}

func TestPublisherAndSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	broker, err := mqtttest.Start(map[string]string{"u": "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	login := Broker{URL: broker.URL(), Username: "u", Password: "p", Backoff: 10 * time.Millisecond}

	var logged bytes.Buffer
	got := make(chan string, 16)
	sub, err := NewSubscriber(SubscriberConfig{Broker: login, Topics: []string{"events/+"}, Logger: slog.New(slog.NewTextHandler(&logged, nil)),
		Notify: func(_ context.Context, topic string, n openadr.Notification) {
			got <- topic + " " + string(n.Operation) + " " + n.Object.(openadr.Event).Name
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	pub, err := NewPublisher(PublisherConfig{Broker: login})
	if err != nil {
		t.Fatal(err)
	}
	// A message that is not a notification is skipped and logged; the next
	// one still arrives.
	pub.Publish("events/create", []byte("not a notification"))
	pub.Publish("events/create", []byte(eventNotification))
	select {
	case m := <-got:
		if m != "events/create CREATE prices" {
			t.Errorf("received %q", m)
		}
	case <-ctx.Done():
		t.Fatal("the notification did not arrive")
	}
	if !strings.Contains(logged.String(), "not a notification") {
		t.Errorf("the undecodable message was not logged: %s", logged.String())
	}

	if err := pub.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
	pub.Publish("events/create", []byte(eventNotification)) // after Close: nothing, and no panic
	if err := sub.Close(); err != nil {
		t.Error(err)
	}
	if err := sub.Ready(ctx); err == nil {
		t.Error("Ready after Close succeeded")
	}
	select {
	case m := <-got:
		t.Errorf("a notification after Close: %q", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// While the broker cannot be reached, notifications wait, the excess is
// dropped, and Close does not wait for ever.
func TestPublisherWithoutBroker(t *testing.T) {
	var logged bytes.Buffer
	pub, err := NewPublisher(PublisherConfig{
		Broker: Broker{URL: "mqtt://127.0.0.1:1", Backoff: 10 * time.Millisecond}, Queue: 3,
		Logger: slog.New(slog.NewTextHandler(&logged, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for range 50 {
			pub.Publish("events/create", []byte(eventNotification))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked while the broker was away")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := pub.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close with undelivered notifications: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Close took %v", time.Since(start))
	}
	log := logged.String()
	if !strings.Contains(log, "queue full") || strings.Count(log, "cannot connect") != 1 {
		t.Errorf("log (dropped notifications, and one line for the broker being away):\n%s", log)
	}

	// A subscriber whose broker is away is not ready, and can be closed.
	sub, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: "mqtt://127.0.0.1:1", Backoff: 10 * time.Millisecond},
		Topics: []string{"a"}, Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	if err := sub.Ready(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Ready without a broker: %v", err)
	}
	if sub.Sessions() != 0 {
		t.Errorf("Sessions = %d", sub.Sessions())
	}
	closed := make(chan struct{})
	go func() { _ = sub.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close of a subscriber without a broker hung")
	}
}
