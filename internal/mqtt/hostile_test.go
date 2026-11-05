// SPDX-License-Identifier: MIT

package mqtt_test

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/mqtt"
)

// A broker that takes the connection and then reads nothing. A PUBLISH too
// large for the socket buffers is stuck in its write; Close must not wait
// for that write to time out, nor send a DISCONNECT behind it.
func TestCloseWhileAWriteIsStuck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, err := mqtt.ReadPacket(conn); err != nil {
			return
		}
		_, _ = conn.Write([]byte{mqtt.TypeConnack << 4, 2, 0, 0})
		<-release
	}()

	conn, err := mqtt.Dial(context.Background(), "mqtt://"+ln.Addr().String(), mqtt.Options{KeepAlive: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	published := make(chan error, 1)
	go func() {
		// Far beyond what loopback socket buffers hold.
		published <- conn.Publish(context.Background(), "a/b", bytes.Repeat([]byte("x"), 64<<20))
	}()
	// Until the write is stuck, give or take: closing earlier tests less,
	// not something else.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	_ = conn.Close()
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Close took %v", took)
	}
	select {
	case err := <-published:
		if err == nil {
			t.Error("a PUBLISH to a broker that reads nothing succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck PUBLISH did not end with the connection")
	}
}
