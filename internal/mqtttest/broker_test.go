// SPDX-License-Identifier: MIT

package mqtttest

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/mqtt"
)

// handshake dials b, sends a CONNECT and returns the connection with the
// CONNACK return code, or -1 when the broker closed without a CONNACK.
func handshake(t *testing.T, b *Broker, user, password string) (net.Conn, int) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(b.URL(), "mqtt://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(mqtt.Connect("c1", user, password, 0).Encode()); err != nil {
		t.Fatal(err)
	}
	p, err := mqtt.ReadPacket(conn)
	if err != nil {
		return conn, -1
	}
	if p.Type != mqtt.TypeConnack || len(p.Body) != 2 {
		t.Fatalf("got packet type %d body %v, want a CONNACK", p.Type, p.Body)
	}
	return conn, int(p.Body[1])
}

func TestBrokerRegistersBeforeConnack(t *testing.T) {
	b, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	conn, code := handshake(t, b, "", "")
	if code != 0 {
		t.Fatalf("CONNACK code %d, want 0", code)
	}
	// The session is registered before the CONNACK is sent, so it is
	// counted and can be dropped as soon as the client has its CONNACK.
	if n := b.Clients(); n != 1 {
		t.Fatalf("Clients() = %d, want 1", n)
	}
	b.DropClients()
	if _, err := mqtt.ReadPacket(conn); err == nil {
		t.Fatal("read after DropClients succeeded, want the connection closed")
	}
}

func TestBrokerRefusesBadCredentials(t *testing.T) {
	b, err := Start(map[string]string{"ven": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	conn, code := handshake(t, b, "ven", "wrong")
	if code != 4 {
		t.Fatalf("CONNACK code %d, want 4", code)
	}
	if _, err := mqtt.ReadPacket(conn); err == nil {
		t.Fatal("read after a refused CONNECT succeeded, want the connection closed")
	}
	if n := b.Clients(); n != 0 {
		t.Fatalf("Clients() = %d, want 0", n)
	}
}

func TestBrokerClosingSendsNoConnack(t *testing.T) {
	b, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// A broker that is closing while a client connects must not register
	// the session or answer it.
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()

	if _, code := handshake(t, b, "", ""); code != -1 {
		t.Fatalf("CONNACK code %d from a closing broker, want none", code)
	}
	if n := b.Clients(); n != 0 {
		t.Fatalf("Clients() = %d, want 0", n)
	}
}
