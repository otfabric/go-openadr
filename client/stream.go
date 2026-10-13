// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
	"github.com/otfabric/go-openadr/internal/ws"
)

// NotificationStream is an open WebSocket with a VTN, on which the VTN
// sends the notifications of the client's subscriptions that name
// openadr.MechanismWebSocket.
//
// WebSocket notifications are not part of OpenADR 3.1.0; a VTN says in
// GET /notifiers whether it has them. Package notification/websocket keeps
// a stream open across failures, which is what a program usually wants.
type NotificationStream struct {
	conn   *ws.Conn
	codec  *wire.Codec
	cancel context.CancelFunc

	mu      sync.Mutex
	pinging bool
	stop    chan struct{}
}

// stream opens the WebSocket at notifiers/ws. Opening it is a call like
// any other: counted, logged, and tried again as the retry policy has it.
func (c *core) stream(ctx context.Context) (*NotificationStream, error) {
	var out *NotificationStream
	err := c.call(ctx, "GET /notifiers/ws", http.MethodGet, func() (err error) {
		out, err = c.openStream(ctx)
		return err
	})
	return out, err
}

func (c *core) openStream(ctx context.Context) (*NotificationStream, error) {
	for attempt := 0; ; attempt++ {
		header := http.Header{}
		token := ""
		if c.tokens != nil {
			var err error
			if token, err = c.tokens.Token(ctx); err != nil {
				return nil, fmt.Errorf("%w: obtaining a token: %w", errNotSent, err)
			}
			header.Set("Authorization", "Bearer "+token)
		}
		if c.ua != "" {
			header.Set("User-Agent", c.ua)
		}
		// The context of the request is that of the connection, so it
		// cannot be the caller's, which is for opening it. The handshake
		// gets the caller's deadline by hand.
		connCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		opened := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-opened:
			}
		}()
		conn, err := ws.Dial(connCtx, c.hc, c.url("notifiers/ws", nil), header)
		close(opened)
		if err == nil {
			if ctx.Err() != nil {
				// Cancelled just as it opened.
				_ = conn.Close(ws.CloseGoingAway, "")
				cancel()
				return nil, ctx.Err()
			}
			return &NotificationStream{conn: conn, codec: c.codec, cancel: cancel, stop: make(chan struct{})}, nil
		}
		cancel()
		var refused *ws.HandshakeError
		if !errors.As(err, &refused) {
			if ctx.Err() != nil {
				return nil, transportError(ctx, ctx.Err())
			}
			return nil, transportError(ctx, err)
		}
		if refused.StatusCode == http.StatusUnauthorized && attempt == 0 && token != "" {
			if inv, ok := c.tokens.(interface{ Invalidate(token string) }); ok {
				inv.Invalidate(token)
				continue
			}
		}
		apiErr := &openadr.Error{Method: http.MethodGet, Path: "/notifiers/ws", StatusCode: refused.StatusCode}
		if p, err := c.codec.UnmarshalProblem(refused.Body); err == nil {
			apiErr.Problem = p
		}
		return nil, apiErr
	}
}

// KeepAlive makes the stream ping the VTN every interval, and makes Next
// fail when nothing at all was heard from the VTN for two and a half
// intervals: a connection that died without a word is noticed that way.
// Call it before the first Next.
func (s *NotificationStream) KeepAlive(interval time.Duration) {
	if interval <= 0 {
		return
	}
	s.conn.SetIdleTimeout(2*interval + interval/2)
	s.mu.Lock()
	started := s.pinging
	s.pinging = true
	s.mu.Unlock()
	if started {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				if s.conn.Ping(interval) != nil {
					return
				}
			}
		}
	}()
}

// Next waits for the next notification. An error ends the stream: Close
// it and open another. One goroutine at a time may call Next.
func (s *NotificationStream) Next() (openadr.Notification, error) {
	for {
		message, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.stop:
				// Ended by Close, on this side.
				return openadr.Notification{}, fmt.Errorf("%w: the notification stream: %w", openadr.ErrClosed, err)
			default:
			}
			return openadr.Notification{}, fmt.Errorf("openadr: the notification stream ended: %w", err)
		}
		// A message that is not a notification is passed over: the stream
		// is still good, and a VTN may come to say things this client
		// does not know of.
		if n, err := s.codec.UnmarshalNotification(message); err == nil {
			return n, nil
		}
	}
}

// Close ends the stream. A Next that is waiting returns an error. Close
// may be called from any goroutine, and more than once.
func (s *NotificationStream) Close() error {
	s.mu.Lock()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.mu.Unlock()
	err := s.conn.Close(ws.CloseNormal, "")
	s.cancel()
	return err
}

// NotificationStream opens a WebSocket on which the VTN sends the
// notifications of this client's subscriptions that name
// openadr.MechanismWebSocket. ctx bounds opening it, not its life. A VTN
// without WebSockets answers with openadr.ErrNotFound.
func (c *VEN) NotificationStream(ctx context.Context) (*NotificationStream, error) {
	return c.core.stream(ctx)
}

// NotificationStream opens a WebSocket on which the VTN sends the
// notifications of this client's subscriptions that name
// openadr.MechanismWebSocket. ctx bounds opening it, not its life. A VTN
// without WebSockets answers with openadr.ErrNotFound.
func (c *BL) NotificationStream(ctx context.Context) (*NotificationStream, error) {
	return c.core.stream(ctx)
}
