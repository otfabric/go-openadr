// SPDX-License-Identifier: MIT

package ws

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A server that takes the connection and never reads it. The client end
// of a WebSocket is the body of an HTTP response, which has no deadlines:
// a write to it must still give up, and Close must not wait for it.
func TestClientWriteToAPeerThatDoesNotRead(t *testing.T) {
	release := make(chan struct{})
	ts := serve(t, false, func(*Conn) { <-release })
	defer close(release)
	conn := dial(t, ts)

	message := bytes.Repeat([]byte("x"), 1<<20)
	failed := make(chan error, 1)
	go func() {
		// The socket buffers fill up after a few megabytes at most.
		for range 256 {
			if err := conn.WriteText(message, 200*time.Millisecond); err != nil {
				failed <- err
				return
			}
		}
		failed <- nil
	}()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("256 MiB were written to a peer that reads nothing")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a write to a peer that does not read never gave up")
	}
	closed := make(chan struct{})
	go func() {
		_ = conn.Close(CloseNormal, "")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a peer that does not read")
	}
}

// A ping flood is answered while the reader reads, and a message after it
// still arrives.
func TestPingFlood(t *testing.T) {
	got := make(chan string, 1)
	ts := serve(t, false, func(c *Conn) {
		m, err := c.ReadMessage()
		if err != nil {
			got <- err.Error()
			return
		}
		got <- string(m)
	})
	conn := raw(t, ts)
	// Drain the pongs, so that the server's writes do not stall.
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	var flood []byte
	for range 2000 {
		flood = append(flood, frame(0x80|opPing, []byte("ping"))...)
	}
	flood = append(flood, frame(0x80|opText, []byte("after"))...)
	if _, err := conn.Write(flood); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "after" {
			t.Errorf("got %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the message after the pings never arrived")
	}
}

// A long close reason is cut between characters, never inside one.
func TestCloseReasonStaysUTF8(t *testing.T) {
	reasons := make(chan string, 1)
	ts := serve(t, false, func(c *Conn) {
		// After the handshake has been read on the other side.
		if _, err := c.ReadMessage(); err == nil {
			_ = c.Close(CloseGoingAway, strings.Repeat("é", 100))
		}
	})
	conn := raw(t, ts)
	_, _ = conn.Write(frame(0x80|opText, []byte("go")))
	go func() {
		head := make([]byte, 2)
		if _, err := io.ReadFull(conn, head); err != nil {
			reasons <- ""
			return
		}
		payload := make([]byte, head[1]&0x7F)
		_, _ = io.ReadFull(conn, payload)
		if len(payload) < 2 || binary.BigEndian.Uint16(payload) != CloseGoingAway {
			reasons <- ""
			return
		}
		reasons <- string(payload[2:])
	}()
	select {
	case reason := <-reasons:
		if reason == "" || !utf8.ValidString(reason) || len(reason) > 123 {
			t.Errorf("close reason of %d bytes, valid UTF-8 %v", len(reason), utf8.ValidString(reason))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no close frame")
	}
}
