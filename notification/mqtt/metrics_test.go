// SPDX-License-Identifier: MIT

package mqtt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/mqtttest"
)

// tally is what a Publisher or Subscriber reported.
type tally struct{ connected, disconnected, published, dropped, received int }

type counts struct {
	mu sync.Mutex
	t  tally
}

func (c *counts) inc(n *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*n++
}

func (c *counts) OnConnected()         { c.inc(&c.t.connected) }
func (c *counts) OnDisconnected(error) { c.inc(&c.t.disconnected) }
func (c *counts) OnPublished()         { c.inc(&c.t.published) }
func (c *counts) OnDropped()           { c.inc(&c.t.dropped) }
func (c *counts) OnReceived()          { c.inc(&c.t.received) }

func (c *counts) get() tally {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("did not happen: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	broker, err := mqtttest.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	login := Broker{URL: broker.URL(), Backoff: 5 * time.Millisecond}

	subscriber, publisher := &counts{}, &counts{}
	sub, err := NewSubscriber(SubscriberConfig{Broker: login, Topics: []string{"events/+"}, Metrics: subscriber,
		Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	pub, err := NewPublisher(PublisherConfig{Broker: login, Metrics: publisher})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pub.Close(ctx) }()
	for range 3 {
		pub.Publish("events/create", []byte(eventNotification))
	}
	// Not a notification: published, and not received as one.
	pub.Publish("events/create", []byte("noise"))
	eventually(t, "three notifications were received", func() bool { return subscriber.get().received == 3 })
	eventually(t, "four messages were published", func() bool { return publisher.get().published == 4 })
	if got := publisher.get(); got.connected != 1 || got.dropped != 0 || got.received != 0 {
		t.Errorf("the publisher reported %+v", got)
	}
	if got := subscriber.get(); got.connected != 1 || got.disconnected != 0 || got.published != 0 {
		t.Errorf("the subscriber reported %+v", got)
	}

	// The broker drops everyone: both say so, and both come back.
	broker.DropClients()
	eventually(t, "the subscriber was disconnected and came back", func() bool {
		got := subscriber.get()
		return got.disconnected == 1 && got.connected == 2
	})
	pub.Publish("events/create", []byte(eventNotification))
	eventually(t, "the publisher came back and published", func() bool {
		got := publisher.get()
		return got.connected == 2 && got.disconnected == 1 && got.published == 5
	})

	// Without a broker, what does not fit the queue is dropped.
	nowhere := &counts{}
	lost, err := NewPublisher(PublisherConfig{Broker: Broker{URL: "mqtt://127.0.0.1:1", Backoff: time.Hour}, Queue: 2, Metrics: nowhere})
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		lost.Publish("t", []byte("x"))
	}
	if got := nowhere.get(); got.dropped < 3 || got.published != 0 {
		t.Errorf("without a broker: %+v", got)
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	_ = lost.Close(short)
}

func TestSentinels(t *testing.T) {
	if _, err := NewPublisher(PublisherConfig{Broker: Broker{URL: "http://x"}}); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("a publisher for a URL that is not MQTT: %v", err)
	}
	if _, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: "mqtt://x"}}); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("a subscriber without topics: %v", err)
	}
	sub, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: "mqtt://127.0.0.1:1", Backoff: time.Hour}, Topics: []string{"#"},
		Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	_ = sub.Close()
	if err := sub.Ready(context.Background()); !errors.Is(err, openadr.ErrClosed) {
		t.Errorf("Ready on a closed subscriber: %v", err)
	}
}
