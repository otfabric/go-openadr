// SPDX-License-Identifier: MIT

package mqtt

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
	imqtt "github.com/otfabric/go-openadr/internal/mqtt"
)

// strayBroker answers every PUBLISH and SUBSCRIBE first with an
// acknowledgement of the same kind for a packet identifier that nothing
// waits for, and then with the one that answers it. It routes nothing, and
// counts the PUBLISH packets it has had.
type strayBroker struct {
	ln        net.Listener
	wg        sync.WaitGroup
	published atomic.Int64

	mu    sync.Mutex
	conns []net.Conn
}

func newStrayBroker(t *testing.T) *strayBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &strayBroker{ln: ln}
	b.wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.conns = append(b.conns, conn)
			b.mu.Unlock()
			b.wg.Go(func() { b.serve(conn) })
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			_ = c.Close()
		}
		b.mu.Unlock()
		b.wg.Wait()
	})
	return b
}

func (b *strayBroker) url() string { return "mqtt://" + b.ln.Addr().String() }

func (b *strayBroker) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if p, err := imqtt.ReadPacket(conn); err != nil || p.Type != imqtt.TypeConnect {
		return
	}
	_, _ = conn.Write([]byte{imqtt.TypeConnack << 4, 2, 0, 0})
	for {
		p, err := imqtt.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p.Type {
		case imqtt.TypePublish:
			_, _, _, id, err := imqtt.ParsePublish(p)
			if err != nil {
				return
			}
			b.published.Add(1)
			_, _ = conn.Write(append(imqtt.Ack(imqtt.TypePuback, id^0x8000).Encode(), imqtt.Ack(imqtt.TypePuback, id).Encode()...))
		case imqtt.TypeSubscribe:
			id, _, err := imqtt.Uint16(p.Body)
			if err != nil {
				return
			}
			// One return code: the tests subscribe to one filter.
			stray := imqtt.Packet{Type: imqtt.TypeSuback, Body: []byte{byte(id>>8) ^ 0x80, byte(id), 1}}
			answer := imqtt.Packet{Type: imqtt.TypeSuback, Body: []byte{byte(id >> 8), byte(id), 1}}
			_, _ = conn.Write(append(stray.Encode(), answer.Encode()...))
		case imqtt.TypePingreq:
			_, _ = conn.Write(imqtt.Packet{Type: imqtt.TypePingresp}.Encode())
		default:
			return
		}
	}
}

// lateCounts is counts that also counts late acknowledgements.
type lateCounts struct {
	counts
	late atomic.Int32
}

func (c *lateCounts) OnLateAck() { c.late.Add(1) }

// L7: an acknowledgement that nothing waits for is dropped, the connection
// is kept, and a Metrics that has OnLateAck counts it, for the Subscriber
// and for the Publisher.
func TestLateAcksAreCounted(t *testing.T) {
	b := newStrayBroker(t)
	login := Broker{URL: b.url(), Backoff: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sm := &lateCounts{}
	sub, err := NewSubscriber(SubscriberConfig{Broker: login, Topics: []string{"events/+"}, Metrics: sm,
		Notify: func(context.Context, string, openadr.Notification) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	// The connection reads in order: the stray SUBACK was read before the
	// one that completed the subscription.
	if n := sm.late.Load(); n != 1 {
		t.Errorf("the Subscriber counted %d late acknowledgements, want 1", n)
	}

	pm := &lateCounts{}
	pub, err := NewPublisher(PublisherConfig{Broker: login, Metrics: pm})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pub.Close(ctx) }()
	for range 3 {
		pub.Publish("events/create", []byte(eventNotification))
	}
	eventually(t, "three notifications were published", func() bool { return pm.get().published == 3 })
	if n := pm.late.Load(); n != 3 {
		t.Errorf("the Publisher counted %d late acknowledgements, want 3", n)
	}
	if got := pm.get(); got.connected != 1 || got.disconnected != 0 {
		t.Errorf("the Publisher reported %+v: the stray acknowledgements ended a connection", got)
	}
	if n := b.published.Load(); n != 3 {
		t.Errorf("the broker had %d PUBLISH packets for 3 notifications", n)
	}
	if got := sm.get(); got.disconnected != 0 || got.connected != 1 {
		t.Errorf("the Subscriber reported %+v", got)
	}
}

// stoppingLate is Metrics whose OnLateAck does not return normally.
type stoppingLate struct{ counts }

func (*stoppingLate) OnLateAck() { guardtest.Stop() }

// An OnLateAck that does not return normally is logged, and the Publisher
// goes on.
func TestLateAckThatDoesNotReturnNormally(t *testing.T) {
	b := newStrayBroker(t)
	log := guardtest.NewHandler()
	m := &stoppingLate{}
	pub, err := NewPublisher(PublisherConfig{Broker: Broker{URL: b.url(), Backoff: 10 * time.Millisecond}, Logger: log.Logger(), Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pub.Close(ctx)
	}()
	pub.Publish("events/create", []byte(eventNotification))
	pub.Publish("events/create", []byte(eventNotification))
	log.WaitFailures(t, "Metrics.OnLateAck", 2)
	eventually(t, "two notifications were published", func() bool { return m.get().published == 2 })
	if got := m.get(); got.disconnected != 0 {
		t.Errorf("the Publisher reported %+v", got)
	}
}
