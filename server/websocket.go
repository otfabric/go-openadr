// SPDX-License-Identifier: MIT

package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/internal/ws"
)

// WebSocketConfig is the WebSocket binding of a VTN's notifications, which
// is not part of OpenADR 3.1.0: an extension of OpenLEADR that this module
// shares. A client opens GET /notifiers/ws with its bearer token and is
// sent, as text messages, the notifications of its subscriptions that name
// the mechanism WEBSOCKET. The zero value is a usable configuration.
//
// A WebSocket is not a queue. What happens while a client is not connected
// it is not told, and a client that reads too slowly is disconnected; a
// client catches up by searching, as it does after any reconnect.
type WebSocketConfig struct {
	// PingInterval is how often the VTN pings a connected client; a client
	// that does not answer two pings is disconnected. 30 seconds when zero.
	PingInterval time.Duration
	// MaxPerClient is the number of connections one client may have open;
	// one more is refused with 409. 4 when zero.
	MaxPerClient int
	// Queue is the number of notifications that may wait for one
	// connection. A connection whose queue is full is closed. 256 when zero.
	Queue int
}

// socket is one connected client.
type socket struct {
	conn     *ws.Conn
	clientID string
	queue    chan []byte
	// gone is closed when the reader has seen the connection end.
	gone chan struct{}
	// drop is closed to make the writer close the connection.
	drop     chan struct{}
	dropOnce sync.Once
	// why is the close code to drop with.
	why int
}

func (c *socket) close(code int) {
	c.dropOnce.Do(func() {
		c.why = code
		close(c.drop)
	})
}

// sockets are the WebSockets of a VTN.
type sockets struct {
	cfg WebSocketConfig

	mu       sync.Mutex
	byClient map[string][]*socket
	closed   bool
	serving  sync.WaitGroup
}

func newSockets(cfg WebSocketConfig) *sockets {
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.MaxPerClient <= 0 {
		cfg.MaxPerClient = 4
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 256
	}
	return &sockets{cfg: cfg, byClient: map[string][]*socket{}}
}

// send queues a notification for every connection of a client.
func (ss *sockets) send(clientID string, body []byte) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for _, c := range ss.byClient[clientID] {
		select {
		case c.queue <- body:
		default:
			// It does not keep up. Better gone, so that it notices and
			// catches up, than silently behind.
			c.close(ws.CloseTryAgainLater)
		}
	}
}

var (
	errSocketsClosed = errors.New("the VTN is shutting down")
	errTooManySocket = errors.New("websocket connection already open")
)

// reserve makes room for one more connection of a client.
func (ss *sockets) reserve(c *socket) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	switch {
	case ss.closed:
		return errSocketsClosed
	case len(ss.byClient[c.clientID]) >= ss.cfg.MaxPerClient:
		return errTooManySocket
	}
	ss.byClient[c.clientID] = append(ss.byClient[c.clientID], c)
	ss.serving.Add(1)
	return nil
}

func (ss *sockets) release(c *socket) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	list := ss.byClient[c.clientID]
	for i, other := range list {
		if other == c {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(ss.byClient, c.clientID)
	} else {
		ss.byClient[c.clientID] = list
	}
	ss.serving.Done()
}

// close disconnects every client and waits for the connections to end.
func (ss *sockets) close() {
	ss.mu.Lock()
	ss.closed = true
	for _, list := range ss.byClient {
		for _, c := range list {
			c.close(ws.CloseGoingAway)
		}
	}
	ss.mu.Unlock()
	ss.serving.Wait()
}

// serveSocket is GET /notifiers/ws.
func (s *Server) serveSocket(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if id.ClientID == "" {
		// Notifications go to the subscriptions of a client.
		w.Header().Set("WWW-Authenticate", "Bearer")
		s.problem(w, r, http.StatusUnauthorized, "this operation needs a bearer token")
		return
	}
	if !ws.IsUpgrade(r) {
		w.Header().Set("Upgrade", "websocket")
		s.problem(w, r, http.StatusUpgradeRequired, "this is the address of a WebSocket")
		return
	}
	ss := s.sockets
	c := &socket{clientID: id.ClientID, queue: make(chan []byte, ss.cfg.Queue), gone: make(chan struct{}), drop: make(chan struct{})}
	if err := ss.reserve(c); err != nil {
		status := http.StatusConflict
		if errors.Is(err, errSocketsClosed) {
			status = http.StatusServiceUnavailable
		}
		s.problem(w, r, status, err.Error())
		return
	}
	defer ss.release(c)

	conn, status, err := ws.Accept(w, r)
	if err != nil {
		if status != 0 {
			s.problem(w, r, status, err.Error())
		}
		return
	}
	c.conn = conn
	// A client has nothing to say here; a large message is a mistake.
	conn.MaxMessage = 64 << 10

	// The reader answers pings and notices the end of the connection.
	// Every frame of the client, a pong above all, shows that it is alive.
	conn.SetIdleTimeout(2*ss.cfg.PingInterval + ss.cfg.PingInterval/2)
	go func() {
		defer close(c.gone)
		for {
			if _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	const writeTimeout = 10 * time.Second
	ping := time.NewTicker(ss.cfg.PingInterval)
	defer ping.Stop()
	for {
		select {
		case body := <-c.queue:
			if conn.WriteText(body, writeTimeout) != nil {
				_ = conn.Close(ws.CloseGoingAway, "")
				<-c.gone
				return
			}
		case <-ping.C:
			if conn.Ping(writeTimeout) != nil {
				_ = conn.Close(ws.CloseGoingAway, "")
				<-c.gone
				return
			}
		case <-c.drop:
			reason := "the VTN is shutting down"
			if c.why == ws.CloseTryAgainLater {
				reason = "notifications were not read fast enough"
			}
			_ = conn.Close(c.why, reason)
			<-c.gone
			return
		case <-c.gone:
			_ = conn.Close(ws.CloseNormal, "")
			return
		}
	}
}

// Close ends what the Server has open: the WebSockets of its clients. The
// HTTP server that serves it does not do that when it shuts down, because
// those connections are no longer its own. New WebSockets are refused
// after. A Server without WebSockets has nothing to close.
func (s *Server) Close() error {
	if s.sockets != nil {
		s.sockets.close()
	}
	return nil
}
