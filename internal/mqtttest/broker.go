// SPDX-License-Identifier: MIT

// Package mqtttest is a small MQTT 3.1.1 broker for the tests and examples
// of this module: enough of a broker to carry notifications between a
// publisher and subscribers in one process. It is not for any other use: it
// keeps no sessions, retains nothing and does not resend.
package mqtttest

import (
	"net"
	"strings"
	"sync"

	"github.com/otfabric/go-openadr/internal/mqtt"
)

// Broker is a running broker on a loopback port.
type Broker struct {
	listener net.Listener
	// users, when not nil, are the user names and passwords accepted.
	users map[string]string

	mu       sync.Mutex
	sessions map[*session]struct{}
	closed   bool
	// log is every message published, in order.
	log []Message
	wg  sync.WaitGroup
}

// Message is a message a client published.
type Message struct {
	Topic   string
	Payload []byte
}

type session struct {
	conn    net.Conn
	writeMu sync.Mutex
	filters []string
	nextID  uint16
}

// Start runs a broker that accepts any client. With users, only those user
// names with their passwords.
func Start(users map[string]string) (*Broker, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &Broker{listener: l, users: users, sessions: map[*session]struct{}{}}
	b.wg.Add(1)
	go b.accept()
	return b, nil
}

// URL is the mqtt:// URL of the broker.
func (b *Broker) URL() string { return "mqtt://" + b.listener.Addr().String() }

// Close stops the broker and drops its clients.
func (b *Broker) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	_ = b.listener.Close()
	b.DropClients()
	b.wg.Wait()
}

// DropClients closes every connection, as a network failure would. The
// broker keeps listening.
func (b *Broker) DropClients() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.sessions {
		_ = s.conn.Close()
	}
}

// Clients is the number of clients connected.
func (b *Broker) Clients() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

// Published returns every message published so far.
func (b *Broker) Published() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Message(nil), b.log...)
}

func (b *Broker) accept() {
	defer b.wg.Done()
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.serve(conn)
		}()
	}
}

func (s *session) write(p mqtt.Packet) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.conn.Write(p.Encode())
}

func (b *Broker) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	first, err := mqtt.ReadPacket(conn)
	if err != nil || first.Type != mqtt.TypeConnect {
		return
	}
	code := b.connect(first.Body)
	s := &session{conn: conn}
	// Register before CONNACK, so a client that has its CONNACK can
	// already be dropped by DropClients.
	if code == 0 {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		b.sessions[s] = struct{}{}
		b.mu.Unlock()
	}
	s.write(mqtt.Packet{Type: mqtt.TypeConnack, Body: []byte{0, code}})
	if code != 0 {
		return
	}
	defer func() {
		b.mu.Lock()
		delete(b.sessions, s)
		b.mu.Unlock()
	}()

	for {
		p, err := mqtt.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p.Type {
		case mqtt.TypePublish:
			topic, payload, qos, id, err := mqtt.ParsePublish(p)
			if err != nil || !mqtt.ValidTopic(topic) {
				return
			}
			b.route(topic, payload)
			if qos == 1 {
				s.write(mqtt.Ack(mqtt.TypePuback, id))
			}
		case mqtt.TypeSubscribe:
			id, rest, err := mqtt.Uint16(p.Body)
			if err != nil || p.Flags != 0x02 {
				return
			}
			ack := mqtt.Ack(mqtt.TypeSuback, id)
			var filters []string
			for len(rest) > 0 {
				var filter string
				if filter, rest, err = mqtt.String(rest); err != nil || len(rest) < 1 {
					return
				}
				filters = append(filters, filter)
				ack.Body = append(ack.Body, min(rest[0], 1))
				rest = rest[1:]
			}
			b.mu.Lock()
			s.filters = append(s.filters, filters...)
			b.mu.Unlock()
			s.write(ack)
		case mqtt.TypePuback:
		case mqtt.TypePingreq:
			s.write(mqtt.Packet{Type: mqtt.TypePingresp})
		case mqtt.TypeDisconnect:
			return
		default:
			return
		}
	}
}

// connect checks a CONNECT packet and returns the CONNACK return code.
func (b *Broker) connect(body []byte) byte {
	name, rest, err := mqtt.String(body)
	if err != nil || name != "MQTT" || len(rest) < 4 {
		return 1
	}
	if rest[0] != 4 {
		return 1
	}
	flags := rest[1]
	rest = rest[4:]
	if _, rest, err = mqtt.String(rest); err != nil { // client identifier
		return 2
	}
	var user, password string
	if flags&0x80 != 0 {
		if user, rest, err = mqtt.String(rest); err != nil {
			return 4
		}
	}
	if flags&0x40 != 0 {
		if password, _, err = mqtt.String(rest); err != nil {
			return 4
		}
	}
	if b.users != nil {
		if want, ok := b.users[user]; !ok || want != password {
			return 4
		}
	}
	return 0
}

// route delivers a message to every session with a matching filter.
func (b *Broker) route(topic string, payload []byte) {
	b.mu.Lock()
	b.log = append(b.log, Message{Topic: topic, Payload: append([]byte(nil), payload...)})
	var to []*session
	ids := map[*session]uint16{}
	for s := range b.sessions {
		for _, f := range s.filters {
			if Match(f, topic) {
				if s.nextID++; s.nextID == 0 {
					s.nextID = 1
				}
				to, ids[s] = append(to, s), s.nextID
				break
			}
		}
	}
	b.mu.Unlock()
	for _, s := range to {
		s.write(mqtt.Publish(topic, payload, 1, ids[s], false))
	}
}

// Match reports whether a topic filter, with its + and # wildcards, matches
// a topic name.
func Match(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, level := range f {
		if level == "#" {
			return i == len(f)-1
		}
		if i >= len(t) || level != "+" && level != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}
