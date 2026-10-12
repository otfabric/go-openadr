// SPDX-License-Identifier: MIT

// Package mqtt is the MQTT binding of OpenADR 3 notifications, for both
// sides: Publisher sends a VTN's notifications to a broker, and Subscriber
// receives them in a client.
//
// The broker is not part of this module: a VTN that notifies by MQTT runs
// one, and its access control decides who may read which topic. See
// SECURITY.md.
package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
	imqtt "github.com/otfabric/go-openadr/internal/mqtt"
	v310 "github.com/otfabric/go-openadr/internal/wire/v310"
)

// Broker says how to reach and log in to a broker.
type Broker struct {
	// URL is the address of the broker: mqtt://host:1883 or mqtts://host:8883.
	URL string
	// ClientID identifies this client to the broker. Two connections with
	// the same ID take each other's place, so it has to be unique among the
	// clients of the broker. When empty the broker assigns one.
	ClientID string
	Username string
	Password string
	// TLS configures a connection to an mqtts URL; the defaults of
	// crypto/tls when nil.
	TLS *tls.Config
	// Backoff is the wait before a second attempt to connect, doubled for
	// each one after, up to a minute; 1 second when zero.
	Backoff time.Duration
}

func (b Broker) options() imqtt.Options {
	return imqtt.Options{ClientID: b.ClientID, Username: b.Username, Password: b.Password, TLS: b.TLS}
}

// connect dials until it succeeds or stop is closed. Each failure is
// reported once per change of its text, not once per attempt.
func (b Broker) connect(stop <-chan struct{}, o imqtt.Options, report func(error)) *imqtt.Conn {
	wait := b.Backoff
	if wait <= 0 {
		wait = time.Second
	}
	last := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		done := make(chan struct{})
		go func() {
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}()
		conn, err := imqtt.Dial(ctx, b.URL, o)
		close(done)
		cancel()
		if err == nil {
			return conn
		}
		if err.Error() != last {
			last = err.Error()
			report(err)
		}
		select {
		case <-stop:
			return nil
		case <-time.After(wait):
		}
		if wait *= 2; wait > time.Minute {
			wait = time.Minute
		}
	}
}

// PublisherConfig configures a Publisher.
type PublisherConfig struct {
	Broker
	// Queue is the number of notifications that may wait; 1024 when zero. A
	// notification for a full queue is dropped.
	Queue int
	// Logger receives connection failures and dropped notifications. Nil
	// disables logging.
	Logger *slog.Logger
}

// Publisher sends notifications to a broker, in the order they were given,
// at QoS 1. While the broker cannot be reached they wait, up to the size of
// the queue, and the connection is tried again.
//
// Delivery to the broker is at least once while the Publisher runs. It is
// not durable: what is queued when the process stops is lost. What the
// broker does for a client that is not connected is the broker's business;
// with the clean sessions the Subscriber of this package uses, such a client
// misses the message.
//
// A Publisher is safe for concurrent use. Close it to stop it.
type Publisher struct {
	cfg   PublisherConfig
	queue chan message
	stop  chan struct{}
	done  chan struct{}

	mu     sync.Mutex
	closed bool
}

type message struct {
	topic string
	body  []byte
}

// NewPublisher returns a running Publisher. It does not wait for the
// broker: the connection is made in the background.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if err := checkURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 1024
	}
	p := &Publisher{cfg: cfg, queue: make(chan message, cfg.Queue), stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p, nil
}

func checkURL(uri string) error {
	_, _, _, err := imqtt.ParseURL(uri)
	return err
}

// Publish queues a notification for a topic and returns at once.
func (p *Publisher) Publish(topic string, body []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.queue <- message{topic: topic, body: body}:
	default:
		p.warn("mqtt: queue full, notification dropped", slog.String("topic", topic))
	}
}

// Close stops accepting notifications and waits until those that are queued
// have reached the broker, or until ctx ends, whichever is first.
func (p *Publisher) Close(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		select {
		case <-p.stop:
		default:
			close(p.stop)
		}
		<-p.done
		return ctx.Err()
	}
}

func (p *Publisher) warn(msg string, attrs ...any) {
	if p.cfg.Logger != nil {
		p.cfg.Logger.Warn(msg, attrs...)
	}
}

func (p *Publisher) run() {
	defer close(p.done)
	var conn *imqtt.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	for m := range p.queue {
		// Until the broker has it: a message that failed is sent again on
		// the next connection, before anything that came after it.
		for {
			if conn == nil {
				conn = p.cfg.connect(p.stop, p.cfg.options(), func(err error) {
					p.warn("mqtt: cannot connect to the broker", slog.Any("error", err))
				})
				if conn == nil {
					return // closed while the broker was away
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := conn.Publish(ctx, m.topic, m.body)
			cancel()
			if err == nil {
				break
			}
			p.warn("mqtt: publishing failed, reconnecting", slog.String("topic", m.topic), slog.Any("error", err))
			_ = conn.Close()
			conn = nil
			select {
			case <-p.stop:
				return
			default:
			}
		}
	}
}

// SubscriberConfig configures a Subscriber.
type SubscriberConfig struct {
	Broker
	// Topics are the topic filters to subscribe to, as a VTN names them in
	// its topic operations (see the MQTTTopics method of the clients).
	// Required.
	Topics []string
	// Notify is called for each notification, one at a time, in the order
	// the broker delivers them. Required. It should return soon: nothing
	// else is received meanwhile.
	Notify func(ctx context.Context, topic string, n openadr.Notification)
	// Logger receives connection failures and messages that are not
	// notifications. Nil disables logging.
	Logger *slog.Logger
}

// Subscriber receives the notifications a VTN publishes on a broker. It
// keeps the connection up: when it is lost, it connects and subscribes
// again.
//
// What is published while the Subscriber is not connected is missed: it
// uses clean sessions. A message may arrive twice. As with webhooks, a
// client that must not miss a change still polls from time to time, and
// does so after every reconnect; Connected tells when.
type Subscriber struct {
	cfg  SubscriberConfig
	stop chan struct{}
	done chan struct{}

	mu        sync.Mutex
	connected chan struct{} // closed while subscribed
	sessions  int

	// calling is held while Notify runs, so that Close can wait for it.
	calling sync.Mutex
}

var codec = v310.Codec()

// NewSubscriber returns a running Subscriber. It does not wait for the
// broker; use Ready for that.
func NewSubscriber(cfg SubscriberConfig) (*Subscriber, error) {
	if err := checkURL(cfg.URL); err != nil {
		return nil, err
	}
	if len(cfg.Topics) == 0 {
		return nil, errors.New("mqtt: a Subscriber needs Topics")
	}
	for _, t := range cfg.Topics {
		if t == "" {
			return nil, errors.New("mqtt: an empty topic filter")
		}
	}
	if cfg.Notify == nil {
		return nil, errors.New("mqtt: a Subscriber needs Notify")
	}
	s := &Subscriber{cfg: cfg, stop: make(chan struct{}), done: make(chan struct{}), connected: make(chan struct{})}
	go s.run()
	return s, nil
}

// Ready waits until the Subscriber is connected and subscribed: from then
// on, what the VTN publishes is received.
func (s *Subscriber) Ready(ctx context.Context) error {
	closed := errors.New("mqtt: the subscriber is closed")
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

// Sessions is the number of times the Subscriber has connected and
// subscribed. It grows by one with every reconnect: a client that polls
// after a reconnect watches it.
func (s *Subscriber) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

// Close disconnects and stops the Subscriber. Notify is not called after
// Close has returned.
func (s *Subscriber) Close() error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	// A call to Notify that had begun is over when this lock is had.
	s.calling.Lock()
	defer s.calling.Unlock()
	return nil
}

func (s *Subscriber) warn(msg string, attrs ...any) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Warn(msg, attrs...)
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

	o := s.cfg.options()
	o.OnMessage = func(topic string, payload []byte) {
		n, err := codec.UnmarshalNotification(payload)
		if err != nil {
			s.warn("mqtt: a message that is not a notification", slog.String("topic", topic), slog.Any("error", err))
			return
		}
		s.calling.Lock()
		defer s.calling.Unlock()
		if ctx.Err() == nil {
			s.cfg.Notify(ctx, topic, n)
		}
	}
	for {
		conn := s.cfg.connect(s.stop, o, func(err error) {
			s.warn("mqtt: cannot connect to the broker", slog.Any("error", err))
		})
		if conn == nil {
			return
		}
		subCtx, subCancel := context.WithTimeout(ctx, 30*time.Second)
		err := conn.Subscribe(subCtx, s.cfg.Topics...)
		subCancel()
		if err != nil {
			_ = conn.Close()
			if ctx.Err() != nil {
				return
			}
			s.warn("mqtt: subscribing failed", slog.Any("error", err))
			select {
			case <-s.stop:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		s.mu.Lock()
		s.sessions++
		close(s.connected)
		s.mu.Unlock()

		select {
		case <-conn.Done():
			s.warn("mqtt: connection to the broker lost", slog.Any("error", conn.Err()))
		case <-s.stop:
			_ = conn.Close()
			return
		}
		s.mu.Lock()
		s.connected = make(chan struct{})
		s.mu.Unlock()
	}
}
