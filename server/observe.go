// SPDX-License-Identifier: MIT

package server

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// Metrics receives a callback for every request a VTN serves. The
// callbacks run on the goroutine of the request and must not block: count,
// observe, return. Implementations must be safe for concurrent use.
//
// op names the operation by method and path as the specification writes
// it, such as "GET /events" or "PUT /programs/{id}". A request that is not
// an operation of the VTN (an unknown path, a method the path does not
// have) is "other". The set of values is small and fixed, so op is fit to
// be a label; the path of the request, with its IDs, would not be.
//
// A value that also implements NotificationMetrics or SocketMetrics is
// told of those too.
//
// Where a client can see an effect, the callback for it comes first: the
// opening of a WebSocket (OnResponse and OnSocketOpened) is reported
// before the handshake is answered, and a notification before it is
// handed to the sender, the broker or the WebSocket. A reading of the
// metrics taken after such an effect was observed includes it. An ordinary
// response is reported when the handler has written it, which its client
// may see a moment earlier.
type Metrics interface {
	// OnRequest is called when a request arrives.
	OnRequest(op string)
	// OnResponse is called when it has been answered, with the HTTP status
	// and how long that took. A request that opens a WebSocket is answered
	// when the WebSocket is open (status 101), not when it closes.
	OnResponse(op string, status int, duration time.Duration)
}

// The mechanisms of a notification, as NotificationMetrics names them.
const (
	MechanismWebhook   = string(openadr.MechanismWebhook)
	MechanismWebSocket = string(openadr.MechanismWebSocket)
	MechanismMQTT      = "MQTT"
)

// NotificationMetrics is an optional extension of Metrics.
type NotificationMetrics interface {
	// OnNotification is called for every notification the VTN hands to a
	// mechanism: one per webhook request queued, per message published to
	// the broker, and per WebSocket it is queued for. What becomes of it
	// after is for the metrics of the sender and the publisher to say.
	OnNotification(mechanism string, object openadr.ObjectType, operation openadr.Operation)
}

// Reasons a WebSocket ends, as SocketMetrics names them.
const (
	// SocketClosed: the client closed it, or the connection was lost.
	SocketClosed = "closed"
	// SocketSlow: the VTN closed it because its queue was full.
	SocketSlow = "slow"
	// SocketShutdown: the VTN closed it because it is shutting down.
	SocketShutdown = "shutdown"
)

// SocketMetrics is an optional extension of Metrics, for a VTN with
// Config.WebSocket.
type SocketMetrics interface {
	// OnSocketOpened is called when a client has opened a WebSocket.
	OnSocketOpened()
	// OnSocketClosed is called when one has ended, with the reason.
	OnSocketClosed(reason string)
}

// opOf names a request for metrics and logs: the pattern of its route.
func (s *Server) opOf(r *http.Request) string {
	_, pattern := s.mux.Handler(r)
	// The patterns of operations begin with their method; those of the
	// fallbacks, for an unknown path or method, do not.
	if !strings.Contains(pattern, " ") {
		return "other"
	}
	return pattern
}

// observed is a response of which the status is remembered.
type observed struct {
	http.ResponseWriter
	status int
	once   sync.Once
	done   func(status int)
}

func (o *observed) WriteHeader(status int) {
	if o.status == 0 {
		o.status = status
	}
	o.ResponseWriter.WriteHeader(status)
}

func (o *observed) Write(b []byte) (int, error) {
	if o.status == 0 {
		o.status = http.StatusOK
	}
	return o.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach what the wrapped writer can do.
func (o *observed) Unwrap() http.ResponseWriter { return o.ResponseWriter }

// Hijack hands the connection over, for a WebSocket. The request counts as
// answered from here: what follows is the WebSocket's life, not the time
// it took to answer.
func (o *observed) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(o.ResponseWriter).Hijack()
	if err == nil {
		o.finish(http.StatusSwitchingProtocols)
	}
	return conn, rw, err
}

func (o *observed) finish(status int) {
	o.once.Do(func() { o.done(status) })
}

// observe serves a request and reports it to the metrics and the log.
func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	debug := s.logger != nil && s.logger.Enabled(r.Context(), slog.LevelDebug)
	if s.metrics == nil && !debug {
		s.mux.ServeHTTP(w, r)
		return
	}
	op, start := s.opOf(r), time.Now()
	if s.metrics != nil {
		s.metrics.OnRequest(op)
	}
	o := &observed{ResponseWriter: w}
	o.done = func(status int) {
		duration := time.Since(start)
		if s.metrics != nil {
			s.metrics.OnResponse(op, status, duration)
		}
		if debug {
			// The operation, not the path: IDs stay out of the log unless
			// asked for by a failure, which logError reports.
			s.logger.DebugContext(r.Context(), "openadr: request", slog.String("op", op), slog.Int("status", status), slog.Duration("duration", duration))
		}
	}
	s.mux.ServeHTTP(o, r)
	if o.status == 0 {
		o.status = http.StatusOK
	}
	o.finish(o.status)
}

// notified reports a notification handed to a mechanism.
func (s *Server) notified(mechanism string, sub subject, op openadr.Operation) {
	if s.notifications != nil {
		s.notifications.OnNotification(mechanism, sub.kind, op)
	}
}
