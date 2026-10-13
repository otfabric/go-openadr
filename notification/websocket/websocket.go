// SPDX-License-Identifier: MIT

// Package websocket receives the notifications of a VTN over a WebSocket.
//
// WebSocket notifications are not part of OpenADR 3.1.0. They are an
// extension of OpenLEADR, which this module's VTN shares: a client
// subscribes with the mechanism WEBSOCKET in place of a callback URL, opens
// GET /notifiers/ws with its bearer token, and is sent what it subscribed
// to. Unlike a webhook this needs no address the VTN can reach, and unlike
// MQTT no broker. GET /notifiers says whether a VTN has it.
//
//	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{
//		Dial:   ven.NotificationStream,
//		Notify: func(ctx context.Context, n openadr.Notification) { ... },
//	})
package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
)

// SubscriberConfig configures a Subscriber.
type SubscriberConfig struct {
	// Dial opens the WebSocket: the NotificationStream method of a VEN or
	// BL client. Required.
	Dial func(ctx context.Context) (*client.NotificationStream, error)
	// Notify is called for each notification, one at a time, in the order
	// the VTN sends them. Required. It should return soon: nothing else is
	// received meanwhile, and a VTN disconnects a client that falls behind.
	Notify func(ctx context.Context, n openadr.Notification)
	// KeepAlive is how often the VTN is pinged; a connection on which
	// nothing is heard for two and a half times as long is given up and
	// opened again. 30 seconds when zero.
	KeepAlive time.Duration
	// Backoff is the wait before a second attempt to connect, doubled for
	// each one after, up to a minute; 1 second when zero.
	Backoff time.Duration
	// Logger receives connection failures. Nil disables logging.
	Logger *slog.Logger
	// Metrics receives a callback for what happens; see Metrics. Nil
	// disables them.
	Metrics Metrics
}

// Metrics receives a callback for what a Subscriber does. The callbacks
// run on the Subscriber's goroutine and must not block.
type Metrics interface {
	// OnConnected is called when the WebSocket is open.
	OnConnected()
	// OnDisconnected is called when it has ended, with the reason.
	OnDisconnected(err error)
	// OnReceived is called for every notification handed to Notify.
	OnReceived()
}

// Subscriber receives the notifications a VTN sends over a WebSocket. It
// keeps the connection up: when it is lost, it connects again.
//
// What happens while the Subscriber is not connected is missed: a VTN does
// not keep notifications for a client that is away. As with webhooks and
// MQTT, a client that must not miss a change still searches from time to
// time, and does so after every reconnect; Sessions tells when.
type Subscriber struct {
	cfg  SubscriberConfig
	stop chan struct{}
	done chan struct{}

	mu        sync.Mutex
	connected chan struct{} // closed while connected
	sessions  int
	stream    *client.NotificationStream

	// calling is held while Notify runs, so that Close can wait for it.
	calling sync.Mutex
}

// NewSubscriber returns a running Subscriber. It does not wait for the
// VTN; use Ready for that.
func NewSubscriber(cfg SubscriberConfig) (*Subscriber, error) {
	switch {
	case cfg.Dial == nil:
		return nil, fmt.Errorf("%w: websocket: a Subscriber needs Dial", openadr.ErrConfiguration)
	case cfg.Notify == nil:
		return nil, fmt.Errorf("%w: websocket: a Subscriber needs Notify", openadr.ErrConfiguration)
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = time.Second
	}
	s := &Subscriber{cfg: cfg, stop: make(chan struct{}), done: make(chan struct{}), connected: make(chan struct{})}
	go s.run()
	return s, nil
}

// Ready waits until the Subscriber is connected: from then on, what the
// VTN sends is received.
func (s *Subscriber) Ready(ctx context.Context) error {
	closed := fmt.Errorf("%w: the WebSocket subscriber", openadr.ErrClosed)
	select {
	case <-s.done:
		return closed
	default:
	}
	s.mu.Lock()
	connected := s.connected
	s.mu.Unlock()
	select {
	case <-connected:
		return nil
	case <-s.done:
		return closed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Sessions is the number of times the Subscriber has connected. It grows
// by one with every reconnect: a client that searches after a reconnect
// watches it.
func (s *Subscriber) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

// Close disconnects and stops the Subscriber. Notify is not called after
// Close has returned.
func (s *Subscriber) Close() error {
	s.mu.Lock()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	stream := s.stream
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	<-s.done
	// A call to Notify that had begun is over when this lock is had.
	s.calling.Lock()
	defer s.calling.Unlock()
	return nil
}

func (s *Subscriber) warn(msg string, err error) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Warn(msg, slog.Any("error", err))
	}
}

func (s *Subscriber) run() {
	defer close(s.done)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.stop
		cancel()
	}()

	wait, last := s.cfg.Backoff, ""
	for ctx.Err() == nil {
		dialCtx, dialCancel := context.WithTimeout(ctx, 30*time.Second)
		stream, err := s.cfg.Dial(dialCtx)
		dialCancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Once per change of its text, not once per attempt.
			if err.Error() != last {
				last = err.Error()
				s.warn("websocket: cannot connect to the VTN", err)
			}
			select {
			case <-s.stop:
				return
			case <-time.After(wait):
			}
			if wait *= 2; wait > time.Minute {
				wait = time.Minute
			}
			continue
		}
		wait, last = s.cfg.Backoff, ""
		stream.KeepAlive(s.cfg.KeepAlive)

		s.mu.Lock()
		select {
		case <-s.stop:
			s.mu.Unlock()
			_ = stream.Close()
			return
		default:
		}
		// From here Close finds the stream, and ends it.
		s.stream = stream
		s.mu.Unlock()
		// Reported before Ready returns, and not under the lock: whoever
		// was told the subscriber is connected finds it in the metrics.
		if s.cfg.Metrics != nil {
			s.cfg.Metrics.OnConnected()
		}
		s.mu.Lock()
		s.sessions++
		close(s.connected)
		s.mu.Unlock()

		for {
			n, err := stream.Next()
			if err != nil {
				if ctx.Err() == nil {
					s.warn("websocket: connection to the VTN lost", err)
				}
				if s.cfg.Metrics != nil {
					s.cfg.Metrics.OnDisconnected(err)
				}
				break
			}
			s.calling.Lock()
			if ctx.Err() == nil {
				if s.cfg.Metrics != nil {
					s.cfg.Metrics.OnReceived()
				}
				s.cfg.Notify(ctx, n)
			}
			s.calling.Unlock()
		}
		_ = stream.Close()
		s.mu.Lock()
		s.stream = nil
		s.connected = make(chan struct{})
		s.mu.Unlock()

		// A VTN that accepts and drops at once is not hammered.
		select {
		case <-s.stop:
			return
		case <-time.After(s.cfg.Backoff):
		}
	}
}
