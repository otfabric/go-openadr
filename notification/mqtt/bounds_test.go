// SPDX-License-Identifier: MIT

package mqtt

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	openadr "github.com/otfabric/go-openadr"
	imqtt "github.com/otfabric/go-openadr/internal/mqtt"
	"github.com/otfabric/go-openadr/internal/mqtttest"
)

// L15: a broker that takes a PUBLISH, answers pings and never acknowledges
// it. Close with a context that ends gives up on that PUBLISH and returns
// with the context, without waiting for the PUBLISH to time out.
func TestPublisherCloseGivesUpAnUnacknowledgedPublish(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	published := make(chan struct{}, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(time.Minute))
		if p, err := imqtt.ReadPacket(conn); err != nil || p.Type != imqtt.TypeConnect {
			return
		}
		_, _ = conn.Write([]byte{imqtt.TypeConnack << 4, 2, 0, 0})
		for {
			p, err := imqtt.ReadPacket(conn)
			if err != nil || p.Type == imqtt.TypeDisconnect {
				return
			}
			switch p.Type {
			case imqtt.TypePingreq:
				_, _ = conn.Write([]byte{imqtt.TypePingresp << 4, 0})
			case imqtt.TypePublish:
				published <- struct{}{}
			}
		}
	}()

	pub, err := NewPublisher(PublisherConfig{Broker: Broker{URL: "mqtt://" + ln.Addr().String(), Backoff: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	pub.Publish("events/create", []byte(eventNotification))
	within(t, "the PUBLISH", published)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		if err := pub.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Close with an unacknowledged PUBLISH: %v", err)
		}
	}()
	within(t, "Close with an unacknowledged PUBLISH", closed)
}

// A notification MQTT cannot carry fails on every connection: it is
// dropped, reported, and the ones after it are published.
func TestPublisherDropsWhatMQTTCannotCarry(t *testing.T) {
	broker, err := mqtttest.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	var logged bytes.Buffer
	var mu sync.Mutex
	m := &counts{}
	pub, err := NewPublisher(PublisherConfig{Broker: Broker{URL: broker.URL(), Backoff: time.Hour}, Metrics: m,
		Logger: slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &logged}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{"events/#", "events/+", "a\x00b", "", strings.Repeat("t", 70000)} {
		pub.Publish(topic, []byte(eventNotification))
	}
	pub.Publish("events/create", []byte(eventNotification))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pub.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := broker.Published(); len(got) != 1 || got[0].Topic != "events/create" {
		t.Errorf("published %+v", got)
	}
	if got := m.get(); got.dropped != 5 || got.published != 1 || got.connected != 1 || got.disconnected != 0 {
		t.Errorf("metrics %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	log := logged.String()
	if strings.Count(log, "cannot be published") != 5 || strings.Contains(log, "reconnecting") {
		t.Errorf("log:\n%s", log)
	}
	for line := range strings.SplitSeq(log, "\n") {
		if len(line) > 2*maxLogged+500 {
			t.Errorf("a log line of %d bytes", len(line))
		}
	}
}

// A message from the broker that is not a notification is logged with its
// topic and the decoding error cut short: another publisher on the topic
// does not choose how long a log entry is.
func TestSubscriberLogsUndecodableMessagesBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	broker, err := mqtttest.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	var logged bytes.Buffer
	var mu sync.Mutex
	got := make(chan string, 4)
	sub, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: broker.URL(), Backoff: 10 * time.Millisecond}, Topics: []string{"#"},
		Logger: slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &logged}, nil)),
		Notify: func(_ context.Context, topic string, _ openadr.Notification) { got <- topic }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := imqtt.Dial(ctx, broker.URL(), imqtt.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	long := strings.Repeat("x", 60000)
	if err := conn.Publish(ctx, "events/"+long, []byte(`{"objectType":"`+strings.Repeat(long, 2)+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Publish(ctx, "events/create", []byte(eventNotification)); err != nil {
		t.Fatal(err)
	}
	select {
	case topic := <-got:
		if topic != "events/create" {
			t.Errorf("notified for %q", topic)
		}
	case <-ctx.Done():
		t.Fatal("the notification did not arrive")
	}
	mu.Lock()
	defer mu.Unlock()
	log := logged.String()
	if !strings.Contains(log, "not a notification") || !strings.Contains(log, "bytes)") {
		t.Errorf("log:\n%.2000s", log)
	}
	for line := range strings.SplitSeq(log, "\n") {
		if len(line) > 2*maxLogged+500 {
			t.Errorf("a log line of %d bytes", len(line))
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("short"); got != "short" {
		t.Errorf("clip(short) = %q", got)
	}
	exact := strings.Repeat("a", maxLogged)
	if got := clip(exact); got != exact {
		t.Errorf("clip of maxLogged bytes changed it")
	}
	// A character of three bytes across the cut is not split.
	s := strings.Repeat("a", maxLogged-1) + strings.Repeat("€", 100)
	got := clip(s)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, strings.Repeat("a", maxLogged-1)+"...") ||
		!strings.HasSuffix(got, "(555 bytes)") {
		t.Errorf("clip = %q", got)
	}
}

// lockedWriter lets a test read what a Logger wrote while it may still write.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// isComparable compiles only for a comparable type.
func isComparable[T comparable]() {}

// Publisher stays comparable, as it was before Close could give up a
// publish in flight.
func TestPublisherComparable(t *testing.T) {
	isComparable[Publisher]()
}
