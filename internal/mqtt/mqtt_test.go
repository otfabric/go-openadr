// SPDX-License-Identifier: MIT

package mqtt_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/mqtt"
	"github.com/otfabric/go-openadr/internal/mqtttest"
)

// The bytes of each packet, written out from the MQTT 3.1.1 specification
// (sections 3.1, 3.3, 3.4, 3.8), not taken from the encoder.
func TestPacketBytes(t *testing.T) {
	for name, tc := range map[string]struct {
		packet mqtt.Packet
		want   string
	}{
		"CONNECT, clean session, keep alive 60": {mqtt.Connect("ven", "", "", 60),
			"10 0f 0004 4d515454 04 02 003c 0003 76656e"},
		"CONNECT with user name and password": {mqtt.Connect("c", "u", "pw", 10),
			"10 14 0004 4d515454 04 c2 000a 0001 63 0001 75 0002 7077"},
		"PUBLISH QoS 1, packet 10, topic a/b": {mqtt.Publish("a/b", []byte("hi"), 1, 10, false),
			"32 09 0003 612f62 000a 6869"},
		"PUBLISH QoS 1, duplicate": {mqtt.Publish("a", nil, 1, 1, true), "3a 05 0001 61 0001"},
		"PUBLISH QoS 0":            {mqtt.Publish("a", []byte("x"), 0, 0, false), "30 04 0001 61 78"},
		"PUBACK for packet 10":     {mqtt.Ack(mqtt.TypePuback, 10), "40 02 000a"},
		"SUBSCRIBE to a/+ and #":   {mqtt.Subscribe(2, []string{"a/+", "#"}), "82 0c 0002 0003 612f2b 01 0001 23 01"},
		"PINGREQ":                  {mqtt.Packet{Type: mqtt.TypePingreq}, "c0 00"},
		"DISCONNECT":               {mqtt.Packet{Type: mqtt.TypeDisconnect}, "e0 00"},
	} {
		want, _ := hex.DecodeString(strings.ReplaceAll(tc.want, " ", ""))
		if got := tc.packet.Encode(); !bytes.Equal(got, want) {
			t.Errorf("%s:\n got %x\nwant %x", name, got, want)
		}
		back, err := mqtt.ReadPacket(bytes.NewReader(want))
		if err != nil || back.Type != tc.packet.Type || back.Flags != tc.packet.Flags || !bytes.Equal(back.Body, tc.packet.Body) {
			t.Errorf("%s: read back %+v, %v", name, back, err)
		}
	}

	// The remaining length is a variable-length number (section 2.2.3).
	for length, prefix := range map[int]string{0: "00", 127: "7f", 128: "8001", 16383: "ff7f", 16384: "808001", 2097151: "ffff7f", 2097152: "80808001"} {
		got := mqtt.Packet{Type: mqtt.TypePublish, Body: make([]byte, length)}.Encode()
		if want, _ := hex.DecodeString("30" + prefix); !bytes.HasPrefix(got, want) || len(got) != len(want)+length {
			t.Errorf("a body of %d bytes starts %x, want %x", length, got[:5], want)
		}
		if back, err := mqtt.ReadPacket(bytes.NewReader(got)); err != nil || len(back.Body) != length {
			t.Errorf("a body of %d bytes read back as %d, %v", length, len(back.Body), err)
		}
	}
	for name, in := range map[string]string{
		"length of five bytes": "30 ffffffff7f",
		"larger than allowed":  "30 ffffff7f",
		"cut in the body":      "30 05 0001",
		"cut in the length":    "30 80",
	} {
		raw, _ := hex.DecodeString(strings.ReplaceAll(in, " ", ""))
		if _, err := mqtt.ReadPacket(bytes.NewReader(raw)); err == nil {
			t.Errorf("%s: read without an error", name)
		}
	}
	if _, _, _, _, err := mqtt.ParsePublish(mqtt.Packet{Type: mqtt.TypePublish, Flags: 0x02, Body: []byte{0, 1, 'a'}}); err == nil {
		t.Error("a QoS 1 PUBLISH without a packet identifier was parsed")
	}
}

func FuzzReadPacket(f *testing.F) {
	f.Add(mqtt.Publish("a/b", []byte("hi"), 1, 10, false).Encode())
	f.Add(mqtt.Connect("c", "u", "p", 10).Encode())
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := mqtt.ReadPacket(bytes.NewReader(data))
		if err != nil {
			return
		}
		if again, err := mqtt.ReadPacket(bytes.NewReader(p.Encode())); err != nil || !bytes.Equal(again.Body, p.Body) {
			t.Fatalf("a packet that was read does not survive being written: %v", err)
		}
		_, _, _, _, _ = mqtt.ParsePublish(p)
	})
}

func broker(t *testing.T, users map[string]string) *mqtttest.Broker {
	t.Helper()
	b, err := mqtttest.Start(users)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestPublishSubscribe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := broker(t, map[string]string{"vtn": "s3cret", "ven": "pw"})

	got := make(chan string, 64)
	sub, err := mqtt.Dial(ctx, b.URL(), mqtt.Options{ClientID: "sub", Username: "ven", Password: "pw",
		OnMessage: func(topic string, payload []byte) { got <- topic + "=" + string(payload) }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Subscribe(ctx, "events/+", "vens/v1/#"); err != nil {
		t.Fatal(err)
	}
	pub, err := mqtt.Dial(ctx, b.URL(), mqtt.Options{ClientID: "pub", Username: "vtn", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pub.Close() }()

	// In order, concurrently published or not, and only what was asked for.
	large := strings.Repeat("x", 200_000)
	for _, m := range [][2]string{{"events/create", "1"}, {"programs/create", "no"}, {"vens/v1/events/update", "2"},
		{"vens/v2/events/update", "no"}, {"events/create/extra", "no"}, {"events/delete", large}} {
		if err := pub.Publish(ctx, m[0], []byte(m[1])); err != nil {
			t.Fatalf("Publish(%s): %v", m[0], err)
		}
	}
	for _, want := range []string{"events/create=1", "vens/v1/events/update=2", "events/delete=" + large} {
		select {
		case m := <-got:
			if m != want {
				t.Fatalf("received %.60q, want %.60q", m, want)
			}
		case <-ctx.Done():
			t.Fatalf("%.40q did not arrive", want)
		}
	}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pub.Publish(ctx, "events/update", fmt.Appendf(nil, "%d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for range 50 {
		select {
		case m := <-got:
			seen[m] = true
		case <-ctx.Done():
			t.Fatalf("only %d of 50 concurrent messages arrived", len(seen))
		}
	}
	if len(seen) != 50 {
		t.Errorf("%d distinct messages of 50", len(seen))
	}

	if err := pub.Publish(ctx, "events/+", nil); err == nil {
		t.Error("publishing to a wildcard succeeded")
	}
	if err := pub.Close(); err != nil {
		t.Error(err)
	}
	if err := pub.Publish(ctx, "a", nil); !errors.Is(err, mqtt.ErrClosed) {
		t.Errorf("Publish after Close: %v", err)
	}
	<-pub.Done()
	if pub.Err() != nil {
		t.Errorf("Err after Close = %v", pub.Err())
	}
}

func TestDialFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := broker(t, map[string]string{"vtn": "s3cret"})

	var refused *mqtt.ConnectError
	if _, err := mqtt.Dial(ctx, b.URL(), mqtt.Options{Username: "vtn", Password: "wrong"}); !errors.As(err, &refused) || refused.Code != 4 {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := mqtt.Dial(ctx, b.URL(), mqtt.Options{}); !errors.As(err, &refused) {
		t.Errorf("no credentials: %v", err)
	}
	for _, uri := range []string{"", "broker:1883", "http://broker", "mqtt://"} {
		if _, err := mqtt.Dial(ctx, uri, mqtt.Options{}); err == nil {
			t.Errorf("Dial(%q) succeeded", uri)
		}
	}
	if _, err := mqtt.Dial(ctx, "mqtt://127.0.0.1:1", mqtt.Options{}); err == nil {
		t.Error("Dial to a closed port succeeded")
	}
	// A listener that accepts and says nothing: the handshake ends with the context.
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = silent.Close() }()
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := mqtt.Dial(short, "mqtt://"+silent.Addr().String(), mqtt.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("silent listener: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Dial took %v with a 200 ms context", time.Since(start))
	}
}

func TestConnectionLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := broker(t, nil)
	c, err := mqtt.Dial(ctx, b.URL(), mqtt.Options{ClientID: "c", KeepAlive: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b.DropClients()
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("the connection did not notice that it was dropped")
	}
	if c.Err() == nil {
		t.Error("Err is nil after the broker dropped the connection")
	}
	if err := c.Publish(ctx, "a", nil); err == nil || errors.Is(err, mqtt.ErrClosed) {
		t.Errorf("Publish on a lost connection: %v, want the reason it was lost", err)
	}

	// A broker that stops answering is noticed by the keep-alive.
	mute, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = mute.Close() }()
	go func() {
		conn, err := mute.Accept()
		if err != nil {
			return
		}
		_, _ = mqtt.ReadPacket(conn)
		_, _ = conn.Write(mqtt.Packet{Type: mqtt.TypeConnack, Body: []byte{0, 0}}.Encode())
		// ... and then reads without ever answering.
		for {
			if _, err := mqtt.ReadPacket(conn); err != nil {
				return
			}
		}
	}()
	c, err = mqtt.Dial(ctx, "mqtt://"+mute.Addr().String(), mqtt.Options{KeepAlive: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
		if c.Err() == nil || !strings.Contains(c.Err().Error(), "stopped answering") {
			t.Errorf("Err = %v", c.Err())
		}
	case <-ctx.Done():
		t.Fatal("a broker that stopped answering was not noticed")
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		filter, topic string
		want          bool
	}{
		{"a/b", "a/b", true}, {"a/b", "a/c", false}, {"a/+", "a/b", true}, {"a/+", "a/b/c", false}, {"a/+", "a", false},
		{"+/b", "a/b", true}, {"#", "a/b/c", true}, {"a/#", "a", true}, {"a/#", "a/b/c", true}, {"a/#", "b", false},
		{"a/+/c", "a/b/c", true}, {"a/+/c", "a/b/d", false}, {"a", "a/b", false},
	} {
		if got := mqtttest.Match(tc.filter, tc.topic); got != tc.want {
			t.Errorf("Match(%q, %q) = %v", tc.filter, tc.topic, got)
		}
	}
}
