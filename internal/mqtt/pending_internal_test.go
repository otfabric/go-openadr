// SPDX-License-Identifier: MIT

package mqtt

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// L6, L7: a request whose context ends returns the error of the context
// and is no longer waited for; the next request on the connection has a
// packet identifier of its own and succeeds, and the answer to the first,
// when it comes after all, is dropped: it completes no other request, and
// is counted by OnLateAck, as is an acknowledgement that answers nothing.
func TestCancelledRequestAndLateAnswer(t *testing.T) {
	saved := dial
	t.Cleanup(func() { dial = saved })
	client, broker := net.Pipe()
	dial = func(context.Context, string) (net.Conn, error) { return client, nil }
	// The broker reads on a goroutine of its own and says which SUBSCRIBE
	// it has; the test writes the answers.
	asked := make(chan uint16, 2)
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
			case TypeSubscribe:
				id, _, _ := Uint16(p.Body)
				asked <- id
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var late atomic.Int32
	conn, err := Dial(ctx, "mqtt://broker.invalid", Options{OnLateAck: func() { late.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = broker.Close()
	})
	next := func() uint16 {
		t.Helper()
		select {
		case id := <-asked:
			return id
		case <-ctx.Done():
			t.Fatal("the broker was not asked")
			return 0
		}
	}
	suback := func(id uint16, code byte) {
		t.Helper()
		p := Packet{Type: TypeSuback, Body: []byte{byte(id >> 8), byte(id), code}}
		if _, err := broker.Write(p.Encode()); err != nil {
			t.Fatal(err)
		}
	}

	// A SUBSCRIBE that the broker has and does not answer.
	waiting, giveUp := context.WithCancel(ctx)
	first := make(chan error, 1)
	go func() { first <- conn.Subscribe(waiting, "a/b") }()
	id1 := next()
	giveUp()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the request whose context ended: %v", err)
	}
	conn.mu.Lock()
	pending := len(conn.waiting)
	conn.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d requests are waited for after the only one ended", pending)
	}

	// The next one. The answer to the first comes before its own, and
	// refuses, which the second must not take for its answer.
	second := make(chan error, 1)
	go func() { second <- conn.Subscribe(ctx, "c/d") }()
	id2 := next()
	if id2 == id1 {
		t.Fatalf("the next request has the packet identifier of the one before, %d", id2)
	}
	suback(id1, 0x80)
	suback(id2, 1)
	if err := <-second; err != nil {
		t.Errorf("the next request: %v", err)
	}
	// The connection reads in order: the late answer was read before the
	// one that ended the second request.
	if n := late.Load(); n != 1 {
		t.Errorf("OnLateAck was called %d times for one late answer", n)
	}

	// A PUBACK of an identifier that was never asked for, then a third
	// request, whose answer is read after it.
	if _, err := broker.Write(Ack(TypePuback, id2+100).Encode()); err != nil {
		t.Fatal(err)
	}
	third := make(chan error, 1)
	go func() { third <- conn.Subscribe(ctx, "e/f") }()
	id3 := next()
	suback(id3, 1)
	if err := <-third; err != nil {
		t.Errorf("the third request: %v", err)
	}
	if n := late.Load(); n != 2 {
		t.Errorf("OnLateAck was called %d times for two acknowledgements nothing waited for", n)
	}
}
