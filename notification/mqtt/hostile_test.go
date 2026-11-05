// SPDX-License-Identifier: MIT

package mqtt

import (
	"context"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	imqtt "github.com/otfabric/go-openadr/internal/mqtt"
)

// fickleBroker accepts every connection, answers CONNACK and, for a
// SUBSCRIBE, a SUBACK with the given return code, and hangs up at the first
// PUBLISH, or right after the SUBACK. With silent it never answers the
// SUBSCRIBE. It counts the connections.
type fickleBroker struct {
	ln    net.Listener
	conns atomic.Int64
	wg    sync.WaitGroup
}

func newFickleBroker(t *testing.T) *fickleBroker { return newBroker(t, 1, false) }

func newBroker(t *testing.T, code byte, silent bool) *fickleBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fickleBroker{ln: ln}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b.conns.Add(1)
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if p, err := imqtt.ReadPacket(conn); err != nil || p.Type != imqtt.TypeConnect {
					return
				}
				_, _ = conn.Write([]byte{imqtt.TypeConnack << 4, 2, 0, 0})
				p, err := imqtt.ReadPacket(conn)
				if err != nil || p.Type != imqtt.TypeSubscribe || len(p.Body) < 2 {
					return
				}
				if silent {
					// Until the client hangs up.
					_, _ = imqtt.ReadPacket(conn)
					return
				}
				_, _ = conn.Write([]byte{imqtt.TypeSuback << 4, 3, p.Body[0], p.Body[1], code})
				// Long enough for the SUBACK to be taken before the hang-up.
				time.Sleep(10 * time.Millisecond)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		b.wg.Wait()
	})
	return b
}

func (b *fickleBroker) url() string { return "mqtt://" + b.ln.Addr().String() }

// countFor returns the connections made in d, from the first one on.
func (b *fickleBroker) countFor(t *testing.T, d time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.conns.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(d)
	return b.conns.Load()
}

// A broker that drops the Publisher at every PUBLISH is not dialled again
// at once and for ever: the Publisher backs off.
func TestPublisherBacksOffABrokerThatDropsEveryPublish(t *testing.T) {
	b := newFickleBroker(t)
	p, err := NewPublisher(PublisherConfig{Broker: Broker{URL: b.url(), Backoff: 50 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	p.Publish("events/e1", []byte(eventNotification))
	// 50, 100, 200, 400 ms: five connections in 600 ms, and a hot loop
	// thousands.
	if n := b.countFor(t, 600*time.Millisecond); n > 8 {
		t.Errorf("%d connections in 600 ms", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = p.Close(ctx)
}

// A broker that takes the subscription and hangs up at once is not
// dialled again at once and for ever either.
func TestSubscriberBacksOffABrokerThatDropsEverySession(t *testing.T) {
	b := newFickleBroker(t)
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: b.url(), Backoff: 50 * time.Millisecond}, Topics: []string{"events/+"},
		Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n := b.countFor(t, 600*time.Millisecond); n > 8 {
		t.Errorf("%d connections in 600 ms", n)
	}
	if s.Sessions() == 0 {
		t.Error("never subscribed")
	}
}

// A broker that refuses every subscription is not dialled again at once
// and for ever either.
func TestSubscriberBacksOffABrokerThatRefusesSubscriptions(t *testing.T) {
	b := newBroker(t, 0x80, false)
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: b.url(), Backoff: 50 * time.Millisecond}, Topics: []string{"events/+"},
		Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n := b.countFor(t, 600*time.Millisecond); n > 8 {
		t.Errorf("%d connections in 600 ms", n)
	}
	if s.Sessions() != 0 {
		t.Errorf("%d sessions on refused subscriptions", s.Sessions())
	}
}

// Close ends a Subscriber that waits for an answer to its SUBSCRIBE.
func TestSubscriberCloseWhileSubscribing(t *testing.T) {
	b := newBroker(t, 0, true)
	s, err := NewSubscriber(SubscriberConfig{Broker: Broker{URL: b.url(), Backoff: 50 * time.Millisecond}, Topics: []string{"events/+"},
		Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	b.countFor(t, 50*time.Millisecond)
	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	if n := b.conns.Load(); n != 1 {
		t.Errorf("%d connections", n)
	}
}

// A Backoff as long as a Duration holds does not overflow into a negative
// wait, which would dial at once and for ever, and Close still ends it.
func TestBackoffNearMaxDuration(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			// Refused: CONNACK with "not authorized".
			_, _ = conn.Write([]byte{imqtt.TypeConnack << 4, 2, 0, 5})
			_ = conn.Close()
		}
	})
	defer func() {
		_ = ln.Close()
		wg.Wait()
	}()
	p, err := NewPublisher(PublisherConfig{Broker: Broker{URL: "mqtt://" + ln.Addr().String(), Backoff: math.MaxInt64}})
	if err != nil {
		t.Fatal(err)
	}
	p.Publish("events/e1", []byte(eventNotification))
	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := dials.Load(); n != 1 {
		t.Errorf("%d attempts to connect, want 1 and then a very long wait", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = p.Close(ctx)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Close took %v", took)
	}
}
