// SPDX-License-Identifier: MIT

// Package ws is the part of the WebSocket protocol (RFC 6455) that
// notifications need: the opening handshake of a server and of a client,
// text messages, ping, pong and close. It has no extensions and no
// subprotocols, and it refuses binary messages.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 defines the handshake with SHA-1; it protects nothing.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Close codes of RFC 6455 that this package and its users send.
const (
	CloseNormal        = 1000
	CloseGoingAway     = 1001
	CloseProtocolError = 1002
	CloseUnsupported   = 1003
	CloseInvalidData   = 1007
	ClosePolicy        = 1008
	CloseTooLarge      = 1009
	CloseTryAgainLater = 1013
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	// The GUID every handshake is hashed with.
	acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// DefaultMaxMessage bounds a message that is read.
	DefaultMaxMessage = 4 << 20
)

// CloseError is returned by ReadMessage when the peer closed the connection
// with a close frame.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("websocket: closed by the peer (%d)", e.Code)
	}
	return fmt.Sprintf("websocket: closed by the peer (%d: %s)", e.Code, e.Reason)
}

// HandshakeError is returned by Dial when the server answers the opening
// handshake with something other than a WebSocket.
type HandshakeError struct {
	// StatusCode is the HTTP status of the answer, and Body the beginning
	// of what came with it.
	StatusCode int
	Body       []byte
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("websocket: the server answered the handshake with %d %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// Conn is one end of a WebSocket. ReadMessage is for one goroutine at a
// time; the methods that write may be called from any.
type Conn struct {
	rw io.ReadWriteCloser
	br *bufio.Reader
	// client says this is the end that opened the connection: it masks
	// what it sends and expects what it reads not to be.
	client bool
	// MaxMessage bounds a message that is read; DefaultMaxMessage when 0.
	MaxMessage int64

	// idle, in nanoseconds, is how long the peer may be silent.
	idle atomic.Int64

	wmu    sync.Mutex
	closed bool

	dmu      sync.Mutex
	deadline *time.Timer
}

func accept(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID)) //nolint:gosec // as above
	return base64.StdEncoding.EncodeToString(sum[:])
}

// hasToken reports whether a comma-separated header names token.
func hasToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// IsUpgrade reports whether a request asks for a WebSocket.
func IsUpgrade(r *http.Request) bool {
	return hasToken(r.Header, "Connection", "upgrade") && hasToken(r.Header, "Upgrade", "websocket")
}

// Accept answers the opening handshake of a client and takes the
// connection over. When it fails it has not written anything unless the
// error says so: the caller answers the request, with the status Accept
// returns.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, int, error) {
	return AcceptAfter(w, r, nil)
}

// AcceptAfter is Accept, with a function that is called when the
// connection has been taken over and before the client is told so. What
// it does is done by the time the client can know that the WebSocket is
// open: a server that keeps a record of its connections is never behind
// what its clients have seen.
func AcceptAfter(w http.ResponseWriter, r *http.Request, before func()) (*Conn, int, error) {
	switch {
	case r.Method != http.MethodGet:
		return nil, http.StatusMethodNotAllowed, errors.New("websocket: the handshake is a GET")
	case !IsUpgrade(r):
		return nil, http.StatusUpgradeRequired, errors.New("websocket: not a WebSocket handshake")
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		w.Header().Set("Sec-WebSocket-Version", "13")
		return nil, http.StatusUpgradeRequired, errors.New("websocket: only version 13 of the protocol is spoken")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		return nil, http.StatusBadRequest, errors.New("websocket: Sec-WebSocket-Key is not 16 bytes in base64")
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		// HTTP/2, or a wrapper that hides the connection.
		return nil, http.StatusUpgradeRequired, fmt.Errorf("websocket: the connection cannot be taken over: %w", err)
	}
	// The deadlines of the HTTP server are not those of the WebSocket.
	_ = conn.SetDeadline(time.Time{})
	if before != nil {
		before()
	}
	answer := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept(key) + "\r\n\r\n"
	if _, err = brw.WriteString(answer); err == nil {
		err = brw.Flush()
	}
	if err != nil {
		_ = conn.Close()
		return nil, 0, fmt.Errorf("websocket: the handshake could not be answered: %w", err)
	}
	return &Conn{rw: conn, br: brw.Reader}, 0, nil
}

// Dial opens a WebSocket with the given HTTP client, which decides about
// TLS and proxies. target is an http or https URL (or ws, wss), and header
// is sent with the handshake. ctx bounds the life of the connection, not
// only the handshake: cancel it, or call Close, to end it.
func Dial(ctx context.Context, hc *http.Client, target string, header http.Header) (*Conn, error) {
	switch {
	case strings.HasPrefix(target, "ws://"):
		target = "http://" + strings.TrimPrefix(target, "ws://")
	case strings.HasPrefix(target, "wss://"):
		target = "https://" + strings.TrimPrefix(target, "wss://")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)

	// The timeout of an HTTP client covers the body, which here is the
	// connection. The caller's context does that job.
	if hc == nil {
		hc = http.DefaultClient
	}
	if hc.Timeout != 0 {
		copied := *hc
		copied.Timeout = 0
		hc = &copied
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		return nil, &HandshakeError{StatusCode: resp.StatusCode, Body: body}
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	switch {
	case !ok:
		err = errors.New("websocket: the HTTP client does not hand over the connection")
	case !hasToken(resp.Header, "Connection", "upgrade") || !hasToken(resp.Header, "Upgrade", "websocket"):
		err = errors.New("websocket: the server switched to something that is not a WebSocket")
	case resp.Header.Get("Sec-WebSocket-Accept") != accept(key):
		err = errors.New("websocket: the server did not prove that it read the handshake")
	case resp.Header.Get("Sec-WebSocket-Extensions") != "":
		err = errors.New("websocket: the server chose an extension that was not offered")
	}
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return &Conn{rw: rw, br: bufio.NewReader(rw), client: true}, nil
}

// SetReadDeadline makes a ReadMessage that has not returned by t fail, and
// with it the connection. The zero time means no deadline.
func (c *Conn) SetReadDeadline(t time.Time) {
	if nc, ok := c.rw.(net.Conn); ok {
		_ = nc.SetReadDeadline(t)
		return
	}
	// The body of an HTTP response has no deadlines: close it instead.
	c.dmu.Lock()
	defer c.dmu.Unlock()
	if c.deadline != nil {
		c.deadline.Stop()
		c.deadline = nil
	}
	if !t.IsZero() {
		c.deadline = time.AfterFunc(time.Until(t), func() { _ = c.rw.Close() })
	}
}

// SetIdleTimeout makes ReadMessage, and with it the connection, fail when
// the peer sends nothing at all for d: no message, and no ping or pong
// either. Zero turns it off. It takes effect with the next frame read.
func (c *Conn) SetIdleTimeout(d time.Duration) {
	c.idle.Store(int64(d))
}

// setWriteDeadline bounds a write, where the connection can.
func (c *Conn) setWriteDeadline(t time.Time) {
	if nc, ok := c.rw.(net.Conn); ok {
		_ = nc.SetWriteDeadline(t)
	}
}

// ReadMessage returns the next text message. It answers pings, and when
// the peer closes the connection it answers that too and returns a
// *CloseError. After an error the connection is of no more use.
func (c *Conn) ReadMessage() ([]byte, error) {
	limit := c.MaxMessage
	if limit <= 0 {
		limit = DefaultMaxMessage
	}
	var (
		message []byte
		partial bool
	)
	for {
		// Per frame, not per message: a pong is a sign of life too.
		if idle := time.Duration(c.idle.Load()); idle > 0 {
			c.SetReadDeadline(time.Now().Add(idle))
		}
		fin, opcode, payload, err := c.readFrame(limit - int64(len(message)))
		if err != nil {
			var failed *protocolError
			if errors.As(err, &failed) {
				_ = c.Close(failed.code, failed.reason)
			}
			return nil, err
		}
		switch opcode {
		case opPing:
			if err := c.writeFrame(opPong, payload, 10*time.Second); err != nil {
				return nil, err
			}
		case opPong:
		case opClose:
			closed := &CloseError{Code: 1005}
			if len(payload) >= 2 {
				closed.Code, closed.Reason = int(binary.BigEndian.Uint16(payload)), string(payload[2:])
			}
			_ = c.Close(CloseNormal, "")
			return nil, closed
		case opText, opContinuation:
			if (opcode == opContinuation) != partial {
				_ = c.Close(CloseProtocolError, "a message out of order")
				return nil, errors.New("websocket: a fragment out of order")
			}
			message = append(message, payload...)
			if partial = !fin; partial {
				continue
			}
			if !utf8.Valid(message) {
				_ = c.Close(CloseInvalidData, "text that is not UTF-8")
				return nil, errors.New("websocket: a text message that is not UTF-8")
			}
			if message == nil {
				message = []byte{}
			}
			return message, nil
		case opBinary:
			_ = c.Close(CloseUnsupported, "binary messages are not understood")
			return nil, errors.New("websocket: a binary message")
		default:
			_ = c.Close(CloseProtocolError, "an unknown kind of frame")
			return nil, fmt.Errorf("websocket: a frame with the unknown opcode %#x", opcode)
		}
	}
}

// protocolError is a fault of the peer that is answered with a close frame.
type protocolError struct {
	code   int
	reason string
}

func (e *protocolError) Error() string { return "websocket: " + e.reason }

// readFrame reads one frame with a payload of at most room bytes.
func (c *Conn) readFrame(room int64) (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin, opcode = head[0]&0x80 != 0, head[0]&0x0F
	if head[0]&0x70 != 0 {
		return false, 0, nil, &protocolError{CloseProtocolError, "reserved bits are set, and no extension was agreed"}
	}
	masked := head[1]&0x80 != 0
	// A client masks every frame, a server none.
	if masked == c.client {
		return false, 0, nil, &protocolError{CloseProtocolError, "a frame that is masked the wrong way round"}
	}
	length := uint64(head[1] & 0x7F)
	control := opcode >= 0x8
	if control && (length > 125 || !fin) {
		return false, 0, nil, &protocolError{CloseProtocolError, "a control frame that is too long or in parts"}
	}
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if !control && (room < 0 || length > uint64(room)) {
		return false, 0, nil, &protocolError{CloseTooLarge, "a message that is too large"}
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// writeFrame writes one whole frame.
func (c *Conn) writeFrame(opcode byte, payload []byte, timeout time.Duration) error {
	frame := make([]byte, 0, len(payload)+14)
	frame = append(frame, 0x80|opcode)
	var maskBit byte
	if c.client {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n <= 125:
		frame = append(frame, maskBit|byte(n))
	case n <= 0xFFFF:
		frame = append(frame, maskBit|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(n))
	default:
		frame = append(frame, maskBit|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(n))
	}
	if c.client {
		var mask [4]byte
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		frame = append(frame, mask[:]...)
		start := len(frame)
		frame = append(frame, payload...)
		for i := range payload {
			frame[start+i] ^= mask[i%4]
		}
	} else {
		frame = append(frame, payload...)
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if timeout > 0 {
		c.setWriteDeadline(time.Now().Add(timeout))
	}
	_, err := c.rw.Write(frame)
	return err
}

// WriteText sends a text message. A write that takes longer than timeout
// fails, where the connection can tell; 0 is no limit.
func (c *Conn) WriteText(message []byte, timeout time.Duration) error {
	return c.writeFrame(opText, message, timeout)
}

// Ping sends a ping, which the peer answers while it reads.
func (c *Conn) Ping(timeout time.Duration) error {
	return c.writeFrame(opPing, nil, timeout)
}

// Close says why and closes the connection. It does not wait for the peer
// to agree. It is safe to call more than once and from any goroutine.
func (c *Conn) Close(code int, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(reason)), uint16(code))
	_ = c.writeFrame(opClose, append(payload, reason...), 2*time.Second)

	c.wmu.Lock()
	already := c.closed
	c.closed = true
	c.wmu.Unlock()
	c.SetReadDeadline(time.Time{})
	if already {
		return nil
	}
	return c.rw.Close()
}
