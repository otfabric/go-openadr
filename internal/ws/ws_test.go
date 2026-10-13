// SPDX-License-Identifier: MIT

package ws

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serve runs handle on the server end of each WebSocket that is opened.
func serve(t *testing.T, tls bool, handle func(*Conn)) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, status, err := Accept(w, r)
		if err != nil {
			if status != 0 {
				http.Error(w, err.Error(), status)
			}
			return
		}
		handle(conn)
	})
	var ts *httptest.Server
	if tls {
		ts = httptest.NewTLSServer(h)
	} else {
		ts = httptest.NewServer(h)
	}
	t.Cleanup(ts.Close)
	return ts
}

// echo sends every message back, and closes when the client does.
func echo(c *Conn) {
	for {
		m, err := c.ReadMessage()
		if err != nil {
			return
		}
		if c.WriteText(m, time.Second) != nil {
			return
		}
	}
}

func dial(t *testing.T, ts *httptest.Server) *Conn {
	t.Helper()
	conn, err := Dial(context.Background(), ts.Client(), ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(CloseNormal, "") })
	return conn
}

func TestEcho(t *testing.T) {
	for _, tls := range []bool{false, true} {
		conn := dial(t, serve(t, tls, echo))
		// The three ways a length is written, and nothing at all.
		for _, size := range []int{0, 1, 125, 126, 65535, 65536, 300000} {
			want := bytes.Repeat([]byte("é"), size/2)
			if err := conn.WriteText(want, time.Second); err != nil {
				t.Fatal(err)
			}
			got, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("tls=%v size %d: %v", tls, size, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("tls=%v size %d: got %d bytes back", tls, size, len(got))
			}
		}
	}
}

func TestDialSchemesAndHeaders(t *testing.T) {
	var seen http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		conn, _, err := Accept(w, r)
		if err == nil {
			_ = conn.Close(CloseNormal, "bye")
		}
	}))
	defer ts.Close()
	// A client with a timeout: it must not cut the connection short.
	hc := &http.Client{Timeout: 50 * time.Millisecond}
	conn, err := Dial(context.Background(), hc, "ws://"+strings.TrimPrefix(ts.URL, "http://"), http.Header{"Authorization": {"Bearer t"}})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "Bearer t" || seen.Get("Sec-WebSocket-Version") != "13" {
		t.Errorf("the handshake carried %v", seen)
	}
	var closed *CloseError
	if _, err := conn.ReadMessage(); !errors.As(err, &closed) || closed.Code != CloseNormal || closed.Reason != "bye" {
		t.Errorf("got %v, want the close of the server", err)
	}
}

func TestClientTimeoutDoesNotEndTheConnection(t *testing.T) {
	release := make(chan struct{})
	ts := serve(t, false, func(c *Conn) {
		<-release
		_ = c.WriteText([]byte("late"), time.Second)
		echo(c)
	})
	hc := &http.Client{Timeout: 30 * time.Millisecond}
	conn, err := Dial(context.Background(), hc, ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(CloseNormal, "") }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	if m, err := conn.ReadMessage(); err != nil || string(m) != "late" {
		t.Errorf("got %q, %v", m, err)
	}
}

func TestPingIsAnswered(t *testing.T) {
	ponged := make(chan error, 1)
	ts := serve(t, false, func(c *Conn) {
		if err := c.Ping(time.Second); err != nil {
			ponged <- err
			return
		}
		// The pong is a frame like any other to the deadline; the read
		// returns only with the message after it.
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		m, err := c.ReadMessage()
		if err == nil && string(m) != "after" {
			err = errors.New("unexpected message " + string(m))
		}
		ponged <- err
	})
	conn := dial(t, ts)
	go func() {
		// Reading is what answers the ping.
		_, _ = conn.ReadMessage()
	}()
	time.Sleep(50 * time.Millisecond)
	if err := conn.WriteText([]byte("after"), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-ponged; err != nil {
		t.Error(err)
	}
}

func TestReadDeadline(t *testing.T) {
	ts := serve(t, false, func(c *Conn) {
		_, _ = c.ReadMessage()
	})
	// The client end is the body of an HTTP response, without deadlines of
	// its own.
	conn := dial(t, ts)
	conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	start := time.Now()
	if _, err := conn.ReadMessage(); err == nil {
		t.Fatal("a read past its deadline returned a message")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("the deadline took %v", took)
	}

	// The server end is a network connection.
	got := make(chan error, 1)
	ts = serve(t, false, func(c *Conn) {
		c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, err := c.ReadMessage()
		got <- err
	})
	dial(t, ts)
	var timeout net.Error
	if err := <-got; !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Errorf("got %v, want a timeout", err)
	}
}

func TestCloseByClient(t *testing.T) {
	got := make(chan error, 1)
	ts := serve(t, false, func(c *Conn) {
		_, err := c.ReadMessage()
		got <- err
	})
	conn := dial(t, ts)
	if err := conn.Close(CloseGoingAway, "done"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(CloseGoingAway, "again"); err != nil {
		t.Errorf("a second close: %v", err)
	}
	var closed *CloseError
	if err := <-got; !errors.As(err, &closed) || closed.Code != CloseGoingAway || closed.Reason != "done" {
		t.Errorf("the server saw %v", err)
	}
	if err := conn.WriteText([]byte("x"), 0); !errors.Is(err, net.ErrClosed) {
		t.Errorf("a write after close: %v", err)
	}
}

func TestAcceptRefuses(t *testing.T) {
	ts := serve(t, false, echo)
	tests := []struct {
		name   string
		method string
		header map[string]string
		want   int
	}{
		{"an ordinary request", "GET", nil, http.StatusUpgradeRequired},
		{"another method", "POST", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}, http.StatusMethodNotAllowed},
		{"an old version", "GET", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "8", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="}, http.StatusUpgradeRequired},
		{"no key", "GET", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13"}, http.StatusBadRequest},
		{"a short key", "GET", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "c2hvcnQ="}, http.StatusBadRequest},
		{"another protocol", "GET", map[string]string{"Connection": "Upgrade", "Upgrade": "h2c"}, http.StatusUpgradeRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(tt.method, ts.URL, nil)
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("got %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestDialRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/denied":
			http.Error(w, "no", http.StatusForbidden)
		case "/liar":
			// Switches, without the proof.
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", "websocket")
			w.Header().Set("Sec-WebSocket-Accept", "bm90IHRoZSBhbnN3ZXI=")
			w.WriteHeader(http.StatusSwitchingProtocols)
		case "/plain":
			_, _ = io.WriteString(w, "hello")
		}
	}))
	defer ts.Close()

	var refused *HandshakeError
	if _, err := Dial(context.Background(), nil, ts.URL+"/denied", nil); !errors.As(err, &refused) || refused.StatusCode != 403 || !strings.Contains(string(refused.Body), "no") {
		t.Errorf("denied: %v", err)
	}
	if _, err := Dial(context.Background(), nil, ts.URL+"/plain", nil); !errors.As(err, &refused) || refused.StatusCode != 200 {
		t.Errorf("plain: %v", err)
	}
	if _, err := Dial(context.Background(), nil, ts.URL+"/liar", nil); err == nil || errors.As(err, &refused) {
		t.Errorf("liar: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, nil, ts.URL, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// raw opens a WebSocket and returns the bare connection, to write frames
// a well-behaved client would not.
func raw(t *testing.T, ts *httptest.Server) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	// RFC 6455 gives the answer to this key.
	answer := make([]byte, 4096)
	n, _ := conn.Read(answer)
	if !strings.Contains(string(answer[:n]), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=") {
		t.Fatalf("handshake answered with %q", answer[:n])
	}
	return conn
}

// frame builds a masked frame, as a client sends it.
func frame(first byte, payload []byte) []byte {
	out := []byte{first}
	switch n := len(payload); {
	case n <= 125:
		out = append(out, 0x80|byte(n))
	case n <= 0xFFFF:
		out = binary.BigEndian.AppendUint16(append(out, 0x80|126), uint16(n))
	default:
		out = binary.BigEndian.AppendUint64(append(out, 0x80|127), uint64(n))
	}
	mask := []byte{1, 2, 3, 4}
	out = append(out, mask...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

// closeCode reads frames until the close frame and returns its code.
func closeCode(t *testing.T, conn net.Conn) int {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		head := make([]byte, 2)
		if _, err := io.ReadFull(conn, head); err != nil {
			t.Fatalf("no close frame: %v", err)
		}
		payload := make([]byte, head[1]&0x7F)
		if _, err := io.ReadFull(conn, payload); err != nil {
			t.Fatal(err)
		}
		if head[0]&0x0F == opClose {
			return int(binary.BigEndian.Uint16(payload))
		}
	}
}

func TestFragmentsAndControlFramesBetween(t *testing.T) {
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
	_, _ = conn.Write(frame(opText, []byte("one ")))               // not final
	_, _ = conn.Write(frame(0x80|opPing, []byte("are you there"))) // between the parts
	_, _ = conn.Write(frame(opContinuation, []byte("two ")))
	_, _ = conn.Write(frame(0x80|opContinuation, []byte("three")))
	if m := <-got; m != "one two three" {
		t.Errorf("got %q", m)
	}
	// And the ping was answered, with its payload.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	pong := make([]byte, 2+len("are you there"))
	if _, err := io.ReadFull(conn, pong); err != nil || pong[0] != 0x80|opPong || string(pong[2:]) != "are you there" {
		t.Errorf("pong %q, %v", pong, err)
	}
}

func TestProtocolViolations(t *testing.T) {
	tests := []struct {
		name   string
		frames [][]byte
		want   int
	}{
		{"an unmasked frame from a client", [][]byte{{0x80 | opText, 2, 'h', 'i'}}, CloseProtocolError},
		{"reserved bits", [][]byte{frame(0xC0|opText, []byte("hi"))}, CloseProtocolError},
		{"a binary message", [][]byte{frame(0x80|opBinary, []byte{1, 2})}, CloseUnsupported},
		{"text that is not UTF-8", [][]byte{frame(0x80|opText, []byte{0xff, 0xfe})}, CloseInvalidData},
		{"a continuation of nothing", [][]byte{frame(0x80|opContinuation, []byte("x"))}, CloseProtocolError},
		{"a new message inside another", [][]byte{frame(opText, []byte("a")), frame(0x80|opText, []byte("b"))}, CloseProtocolError},
		{"a ping in parts", [][]byte{frame(opPing, nil)}, CloseProtocolError},
		{"a ping that is too long", [][]byte{frame(0x80|opPing, bytes.Repeat([]byte("x"), 126))}, CloseProtocolError},
		{"an unknown opcode", [][]byte{frame(0x80|0x3, nil)}, CloseProtocolError},
		{"a message beyond the limit", [][]byte{frame(0x80|opText, bytes.Repeat([]byte("x"), 2000))}, CloseTooLarge},
		{"a message beyond the limit, in parts", [][]byte{frame(opText, bytes.Repeat([]byte("x"), 600)), frame(0x80|opContinuation, bytes.Repeat([]byte("x"), 600))}, CloseTooLarge},
		{"a length that claims exabytes", [][]byte{{0x80 | opText, 0x80 | 127, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}}, CloseTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failed := make(chan error, 1)
			ts := serve(t, false, func(c *Conn) {
				c.MaxMessage = 1000
				_, err := c.ReadMessage()
				failed <- err
			})
			conn := raw(t, ts)
			for _, f := range tt.frames {
				_, _ = conn.Write(f)
			}
			if got := closeCode(t, conn); got != tt.want {
				t.Errorf("closed with %d, want %d", got, tt.want)
			}
			if err := <-failed; err == nil {
				t.Error("the read returned a message")
			}
		})
	}
}

func TestClientRefusesMaskedFrames(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := Accept(w, r)
		if err != nil {
			return
		}
		// A server must not mask.
		_, _ = conn.rw.Write(frame(0x80|opText, []byte("hi")))
		time.Sleep(200 * time.Millisecond)
		_ = conn.rw.Close()
	}))
	defer ts.Close()
	conn := dial(t, ts)
	if _, err := conn.ReadMessage(); err == nil || !strings.Contains(err.Error(), "masked") {
		t.Errorf("got %v", err)
	}
}

func TestCloseWithoutStatus(t *testing.T) {
	got := make(chan error, 1)
	ts := serve(t, false, func(c *Conn) {
		_, err := c.ReadMessage()
		got <- err
	})
	conn := raw(t, ts)
	_, _ = conn.Write(frame(0x80|opClose, nil))
	var closed *CloseError
	if err := <-got; !errors.As(err, &closed) || closed.Code != 1005 || !strings.Contains(err.Error(), "1005") {
		t.Errorf("got %v", err)
	}
	// The close is answered.
	if code := closeCode(t, conn); code != CloseNormal {
		t.Errorf("answered with %d", code)
	}
}

func TestConnectionCutMidFrame(t *testing.T) {
	got := make(chan error, 1)
	ts := serve(t, false, func(c *Conn) {
		_, err := c.ReadMessage()
		got <- err
	})
	conn := raw(t, ts)
	f := frame(0x80|opText, []byte("a message that never ends"))
	_, _ = conn.Write(f[:len(f)-5])
	_ = conn.Close()
	if err := <-got; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got %v", err)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add(frame(0x80|opText, []byte("hello")))
	f.Add(frame(opText, []byte("a")))
	f.Add(frame(0x80|opClose, []byte{3, 232, 'o', 'k'}))
	f.Add([]byte{0x80 | opText, 0x80 | 127, 0, 0, 0, 0, 0, 1, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		server, client := net.Pipe()
		defer func() { _ = client.Close() }()
		go func() {
			_, _ = client.Write(data)
			// Drain what the reader answers, then end the stream.
			_ = client.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			_, _ = io.Copy(io.Discard, client)
			_ = client.Close()
		}()
		c := &Conn{rw: server, br: bufio.NewReader(server), MaxMessage: 4096}
		for {
			m, err := c.ReadMessage()
			if err != nil {
				break
			}
			if len(m) > 4096 {
				t.Fatalf("a message of %d bytes passed the limit", len(m))
			}
		}
		_ = server.Close()
	})
}

// A peer that answers pings is alive, however long it has nothing to say;
// one that does not is given up after the idle timeout.
func TestIdleTimeout(t *testing.T) {
	ended := make(chan error, 2)
	ts := serve(t, false, func(c *Conn) {
		c.SetIdleTimeout(120 * time.Millisecond)
		stop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(30 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					_ = c.Ping(time.Second)
				case <-stop:
					return
				}
			}
		}()
		_, err := c.ReadMessage()
		close(stop)
		ended <- err
	})

	// This one reads, and so answers the pings.
	alive := dial(t, ts)
	go func() { _, _ = alive.ReadMessage() }()
	select {
	case err := <-ended:
		t.Fatalf("a peer that answers pings was given up: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	_ = alive.Close(CloseNormal, "")
	<-ended

	// This one does not read.
	dial(t, ts)
	select {
	case err := <-ended:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Errorf("a silent peer ended with %v, want a timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a silent peer was never given up")
	}

	// Turned off again, silence is fine.
	quiet := make(chan error, 1)
	ts = serve(t, false, func(c *Conn) {
		c.SetIdleTimeout(20 * time.Millisecond)
		c.SetIdleTimeout(0)
		c.SetReadDeadline(time.Time{})
		_, err := c.ReadMessage()
		quiet <- err
	})
	conn := dial(t, ts)
	select {
	case err := <-quiet:
		t.Errorf("without an idle timeout: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	_ = conn.Close(CloseNormal, "")
}
