// SPDX-License-Identifier: MIT

package mqtt

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// pipeBroker connects a Conn to a broker that answers CONNECT and then
// every PUBLISH and SUBSCRIBE with answer(request), and returns the Conn.
func pipeBroker(t *testing.T, answer func(p Packet, id uint16) []byte) *Conn {
	t.Helper()
	saved := dial
	t.Cleanup(func() { dial = saved })
	client, broker := net.Pipe()
	dial = func(context.Context, string) (net.Conn, error) { return client, nil }
	go func() {
		defer func() { _ = broker.Close() }()
		for {
			p, err := ReadPacket(broker)
			if err != nil || p.Type == TypeDisconnect {
				return
			}
			switch p.Type {
			case TypeConnect:
				_, _ = broker.Write([]byte{TypeConnack << 4, 2, 0, 0})
			case TypePublish:
				topic, _, _ := String(p.Body)
				id, _, _ := Uint16(p.Body[2+len(topic):])
				_, _ = broker.Write(answer(p, id))
			case TypeSubscribe:
				id, _, _ := Uint16(p.Body)
				_, _ = broker.Write(answer(p, id))
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := Dial(ctx, "mqtt://broker.invalid", Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = broker.Close()
	})
	return conn
}

// An acknowledgement completes only a request it answers: of the type
// that answers it (MQTT 3.1.1 sections 3.4, 3.9), with reserved flags 0
// and the length of its type (section 2.2.2, 3.4.1, 3.11.1). Anything else
// ends the connection as a protocol error.
func TestAcknowledgementOfTheWrongKind(t *testing.T) {
	hi, lo := func(id uint16) byte { return byte(id >> 8) }, func(id uint16) byte { return byte(id) }
	for name, tc := range map[string]struct {
		subscribe bool
		answer    func(id uint16) []byte
	}{
		"SUBACK for a PUBLISH":   {false, func(id uint16) []byte { return []byte{TypeSuback << 4, 3, hi(id), lo(id), 1} }},
		"UNSUBACK for a PUBLISH": {false, func(id uint16) []byte { return []byte{TypeUnsuback << 4, 2, hi(id), lo(id)} }},
		"PUBACK with flags":      {false, func(id uint16) []byte { return []byte{TypePuback<<4 | 2, 2, hi(id), lo(id)} }},
		"PUBACK of 3 bytes":      {false, func(id uint16) []byte { return []byte{TypePuback << 4, 3, hi(id), lo(id), 0} }},
		"PUBACK for a SUBSCRIBE": {true, func(id uint16) []byte { return []byte{TypePuback << 4, 2, hi(id), lo(id)} }},
		"SUBACK without a code":  {true, func(id uint16) []byte { return []byte{TypeSuback << 4, 2, hi(id), lo(id)} }},
	} {
		t.Run(name, func(t *testing.T) {
			conn := pipeBroker(t, func(_ Packet, id uint16) []byte { return tc.answer(id) })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var err error
			if tc.subscribe {
				err = conn.Subscribe(ctx, "a/b")
			} else {
				err = conn.Publish(ctx, "a/b", []byte("x"))
			}
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("request: %v", err)
			}
			<-conn.Done()
			if !errors.Is(conn.Err(), ErrMalformed) {
				t.Errorf("connection ended with %v", conn.Err())
			}
		})
	}

	// The right answers complete their requests.
	conn := pipeBroker(t, func(p Packet, id uint16) []byte {
		if p.Type == TypeSubscribe {
			return []byte{TypeSuback << 4, 3, hi(id), lo(id), 1}
		}
		return Ack(TypePuback, id).Encode()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Publish(ctx, "a/b", []byte("x")); err != nil {
		t.Errorf("Publish: %v", err)
	}
	if err := conn.Subscribe(ctx, "a/b"); err != nil {
		t.Errorf("Subscribe: %v", err)
	}
}

// What MQTT cannot carry is refused before anything is sent, with an error
// that says so, and the connection goes on.
func TestPublishOfWhatMQTTCannotCarry(t *testing.T) {
	conn := pipeBroker(t, func(_ Packet, id uint16) []byte { return Ack(TypePuback, id).Encode() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, topic := range []string{"", "a/+", "a/#", "a\x00", string(bytes.Repeat([]byte("t"), 65536))} {
		if err := conn.Publish(ctx, topic, nil); !errors.Is(err, ErrTopic) {
			t.Errorf("Publish to a topic of %d bytes: %v", len(topic), err)
		}
	}
	if err := conn.Publish(ctx, "a/b", nil); err != nil {
		t.Errorf("Publish after: %v", err)
	}
}

// When every packet identifier waits for an answer, a request fails at
// once instead of searching for ever; once one is free it is used.
func TestEveryPacketIdentifierInUse(t *testing.T) {
	c := &Conn{waiting: map[uint16]pending{}, done: make(chan struct{})}
	for id := 1; id <= 65535; id++ {
		c.waiting[uint16(id)] = pending{}
	}
	build := func(uint16) (Packet, error) { return Packet{}, errors.New("not sent") }
	if _, err := c.request(context.Background(), TypePuback, build); !errors.Is(err, errNoID) {
		t.Fatalf("request with every identifier in use: %v", err)
	}
	delete(c.waiting, 777)
	var got uint16
	_, err := c.request(context.Background(), TypePuback, func(id uint16) (Packet, error) {
		got = id
		return Packet{}, errors.New("not sent")
	})
	if err == nil || got != 777 {
		t.Errorf("request with one free identifier: id %d, %v", got, err)
	}
}
