// SPDX-License-Identifier: MIT

package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// Options configure a connection.
type Options struct {
	// ClientID identifies the client to the broker. Two connections with
	// one ID take each other's place; when empty the broker assigns one.
	ClientID string
	Username string
	Password string
	// TLS configures a connection to an mqtts URL; the defaults of
	// crypto/tls when nil.
	TLS *tls.Config
	// KeepAlive is how often the connection is checked when idle; 30
	// seconds when zero.
	KeepAlive time.Duration
	// OnMessage is called for every message received, one at a time, in the
	// order they arrive. It must not block for long: nothing else is read
	// meanwhile. A message is acknowledged when OnMessage has returned.
	OnMessage func(topic string, payload []byte)
}

// Conn is a connection to a broker. It is safe for concurrent use.
type Conn struct {
	conn      net.Conn
	keepAlive time.Duration
	onMessage func(string, []byte)

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint16
	waiting map[uint16]chan Packet
	err     error
	closing bool
	done    chan struct{}
	// seen is closed and replaced whenever a packet arrives: the proof of
	// life the keep-alive looks for.
	lastSeen time.Time
}

// ConnectError is a broker refusing a connection (CONNACK return code).
type ConnectError struct {
	Code byte
}

func (e *ConnectError) Error() string {
	reasons := map[byte]string{
		1: "unacceptable protocol version", 2: "client identifier rejected", 3: "server unavailable",
		4: "bad user name or password", 5: "not authorized",
	}
	if r, ok := reasons[e.Code]; ok {
		return "mqtt: the broker refused the connection: " + r
	}
	return fmt.Sprintf("mqtt: the broker refused the connection with code %d", e.Code)
}

// Dial connects to the broker at uri, an mqtt:// or mqtts:// URL (tcp://,
// ssl:// and tls:// are accepted for the same). The default ports are 1883
// and 8883. ctx bounds the connection and the handshake.
func Dial(ctx context.Context, uri string, o Options) (*Conn, error) {
	address, host, secure, err := ParseURL(uri)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	nc, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	// The handshake is bounded by ctx too.
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	fail := func(err error) (*Conn, error) {
		stop()
		_ = nc.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if secure {
		cfg := o.TLS.Clone()
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(err)
		}
		nc = tc
	}

	keepAlive := o.KeepAlive
	if keepAlive <= 0 {
		keepAlive = 30 * time.Second
	}
	seconds := min(max(int(keepAlive/time.Second), 1), 65535)
	if _, err := nc.Write(Connect(o.ClientID, o.Username, o.Password, uint16(seconds)).Encode()); err != nil {
		return fail(err)
	}
	ack, err := ReadPacket(nc)
	if err != nil {
		return fail(fmt.Errorf("mqtt: no answer to CONNECT: %w", err))
	}
	if ack.Type != TypeConnack || len(ack.Body) != 2 {
		return fail(fmt.Errorf("%w: expected CONNACK", ErrMalformed))
	}
	if ack.Body[1] != 0 {
		return fail(&ConnectError{Code: ack.Body[1]})
	}
	if !stop() {
		// ctx ended between the handshake and here, and closed the connection.
		return nil, ctx.Err()
	}

	c := &Conn{
		conn: nc, keepAlive: time.Duration(seconds) * time.Second, onMessage: o.OnMessage,
		waiting: map[uint16]chan Packet{}, done: make(chan struct{}), lastSeen: time.Now(),
	}
	go c.read()
	go c.ping()
	return c, nil
}

// ParseURL takes a broker URL apart: the address to connect to, the host
// name, and whether the connection uses TLS.
func ParseURL(uri string) (address, host string, secure bool, err error) {
	u, err := url.Parse(uri)
	if err != nil || u.Hostname() == "" {
		return "", "", false, fmt.Errorf("mqtt: %q is not a broker URL", uri)
	}
	port := "1883"
	switch u.Scheme {
	case "mqtt", "tcp":
	case "mqtts", "ssl", "tls":
		secure, port = true, "8883"
	default:
		return "", "", false, fmt.Errorf("mqtt: %q is not an mqtt or mqtts URL", uri)
	}
	if u.Port() != "" {
		port = u.Port()
	}
	return net.JoinHostPort(u.Hostname(), port), u.Hostname(), secure, nil
}

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err says why the connection ended; nil while it is up, and after Close.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// ErrClosed is returned for an operation on a connection that has ended.
var ErrClosed = errors.New("mqtt: connection closed")

// shut ends the connection, once. err is nil for a close by the application.
func (c *Conn) shut(err error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	// Once the application has said goodbye, whatever ends the connection
	// is that goodbye: a broker hangs up on a DISCONNECT, and the reader
	// may notice before Close gets here.
	if c.closing {
		err = nil
	}
	c.err = err
	close(c.done)
	c.mu.Unlock()
	_ = c.conn.Close()
}

// Close says goodbye to the broker and closes the connection.
func (c *Conn) Close() error {
	select {
	case <-c.done:
		return nil
	default:
	}
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	_ = c.write(Packet{Type: TypeDisconnect})
	c.shut(nil)
	return nil
}

func (c *Conn) write(p Packet) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.keepAlive))
	_, err := c.conn.Write(p.Encode())
	return err
}

func (c *Conn) read() {
	for {
		p, err := ReadPacket(c.conn)
		if err != nil {
			c.shut(fmt.Errorf("mqtt: connection lost: %w", err))
			return
		}
		c.mu.Lock()
		c.lastSeen = time.Now()
		c.mu.Unlock()
		switch p.Type {
		case TypePublish:
			topic, payload, qos, id, err := ParsePublish(p)
			if err != nil || qos == 2 {
				c.shut(fmt.Errorf("%w: PUBLISH", ErrMalformed))
				return
			}
			if c.onMessage != nil {
				c.onMessage(topic, payload)
			}
			if qos == 1 {
				if err := c.write(Ack(TypePuback, id)); err != nil {
					c.shut(fmt.Errorf("mqtt: connection lost: %w", err))
					return
				}
			}
		case TypePuback, TypeSuback, TypeUnsuback:
			id, _, err := Uint16(p.Body)
			if err != nil {
				c.shut(fmt.Errorf("%w: acknowledgement", ErrMalformed))
				return
			}
			c.mu.Lock()
			ch := c.waiting[id]
			delete(c.waiting, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- p
			}
		case TypePingresp:
		default:
			c.shut(fmt.Errorf("%w: unexpected packet type %d from the broker", ErrMalformed, p.Type))
			return
		}
	}
}

// ping keeps the connection alive, and ends it when the broker has been
// silent for one and a half keep-alive periods.
func (c *Conn) ping() {
	ticker := time.NewTicker(c.keepAlive / 2)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			silent := time.Since(c.lastSeen)
			c.mu.Unlock()
			if silent > c.keepAlive*3/2 {
				c.shut(errors.New("mqtt: connection lost: the broker stopped answering"))
				return
			}
			if err := c.write(Packet{Type: TypePingreq}); err != nil {
				c.shut(fmt.Errorf("mqtt: connection lost: %w", err))
				return
			}
		}
	}
}

// request sends a packet that is answered by one with the same packet
// identifier, and waits for the answer.
func (c *Conn) request(ctx context.Context, build func(id uint16) Packet) (Packet, error) {
	c.mu.Lock()
	select {
	case <-c.done:
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return Packet{}, err
	default:
	}
	// Packet identifiers are 1..65535; skip those still waiting.
	for {
		if c.nextID++; c.nextID == 0 {
			c.nextID = 1
		}
		if _, busy := c.waiting[c.nextID]; !busy {
			break
		}
	}
	id := c.nextID
	ch := make(chan Packet, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
	}

	if err := c.write(build(id)); err != nil {
		forget()
		c.shut(fmt.Errorf("mqtt: connection lost: %w", err))
		return Packet{}, c.Err()
	}
	select {
	case p := <-ch:
		return p, nil
	case <-ctx.Done():
		forget()
		return Packet{}, ctx.Err()
	case <-c.done:
		forget()
		if err := c.Err(); err != nil {
			return Packet{}, err
		}
		return Packet{}, ErrClosed
	}
}

// Publish sends a message at QoS 1 and returns when the broker has
// acknowledged it. When it returns an error the broker may or may not have
// the message: sending it again is the at-least-once of QoS 1.
func (c *Conn) Publish(ctx context.Context, topic string, payload []byte) error {
	if !ValidTopic(topic) {
		return fmt.Errorf("mqtt: %q is not a topic to publish to", topic)
	}
	_, err := c.request(ctx, func(id uint16) Packet { return Publish(topic, payload, 1, id, false) })
	return err
}

// Subscribe asks for the messages of the topic filters, at QoS 1. It fails
// if the broker refuses any of them.
func (c *Conn) Subscribe(ctx context.Context, filters ...string) error {
	if len(filters) == 0 {
		return nil
	}
	for _, f := range filters {
		if f == "" {
			return errors.New("mqtt: an empty topic filter")
		}
	}
	ack, err := c.request(ctx, func(id uint16) Packet { return Subscribe(id, filters) })
	if err != nil {
		return err
	}
	codes := ack.Body[2:]
	if ack.Type != TypeSuback || len(codes) != len(filters) {
		return fmt.Errorf("%w: SUBACK", ErrMalformed)
	}
	for i, code := range codes {
		if code == 0x80 {
			return fmt.Errorf("mqtt: the broker refused the subscription to %q", filters[i])
		}
	}
	return nil
}
