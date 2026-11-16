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
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/backoff"
	"github.com/otfabric/go-openadr/internal/goid"
	"github.com/otfabric/go-openadr/internal/guard"
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
	// each one after, up to a minute; 1 second when zero. It is also the
	// wait, doubled in the same way, before connecting again after a
	// connection that was lost: a broker that drops every connection at
	// once is not dialled again at once.
	Backoff time.Duration
}

func (b Broker) options() imqtt.Options {
	return imqtt.Options{ClientID: b.ClientID, Username: b.Username, Password: b.Password, TLS: b.TLS}
}

// firstWait is the wait before a second attempt to connect.
func (b Broker) firstWait() time.Duration {
	if b.Backoff <= 0 {
		return time.Second
	}
	return b.Backoff
}

// afterSession is the wait after a session that lasted as long as it did: a
// session of more than a minute is not part of a run of failures, and the
// waits start again.
func (b Broker) afterSession(lasted, wait time.Duration) time.Duration {
	if lasted > time.Minute {
		return b.firstWait()
	}
	return wait
}

// nextWait doubles a wait up to a minute, never past what a Duration holds.
func nextWait(wait time.Duration) time.Duration { return backoff.Double(wait, time.Minute) }

// pause waits before the broker is tried again, and reports false when
// stop was closed meanwhile.
func pause(stop <-chan struct{}, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

// connect dials until it succeeds or stop is closed. Each failure is
// reported once per change of its text, not once per attempt.
func (b Broker) connect(stop <-chan struct{}, o imqtt.Options, report func(error)) *imqtt.Conn {
	wait := b.firstWait()
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
		if !pause(stop, wait) {
			return nil
		}
		wait = nextWait(wait)
	}
}

// Metrics receives a callback for what a Publisher or a Subscriber does.
// A Publisher calls OnPublished and OnDropped, a Subscriber OnReceived,
// and both the two about the connection. The callbacks run on the
// goroutines of the Publisher or Subscriber, or for OnDropped on the
// caller's of Publish, and must not block. They run without a lock of the
// Publisher or Subscriber held, but OnReceived, which runs as Notify does:
// one at a time, and a Close on another goroutine waits for it.
// Implementations must be safe for concurrent use.
//
// A callback that does not return normally is logged at error level, as
// "mqtt: callback did not return normally" with the fields callback,
// value and stack, and the Publisher or Subscriber goes on as if it had
// returned. So is the handler of the Logger, which loses that entry.
type Metrics interface {
	// OnConnected is called when a connection to the broker is made, and,
	// for a Subscriber, the subscriptions with it.
	OnConnected()
	// OnDisconnected is called when a connection is lost or given up, with
	// the reason.
	OnDisconnected(err error)
	// OnPublished is called when the broker has confirmed a notification.
	OnPublished()
	// OnDropped is called when a notification is not queued, because the
	// queue is full: the broker has been away for too long. It is also
	// called for a notification that MQTT cannot carry, a topic with a
	// wildcard or NUL or a topic or body too long, which is dropped
	// instead of sent again.
	OnDropped()
	// OnReceived is called for every notification a Subscriber hands to
	// its Notify function, before it does. A notification is not handed
	// to Notify when the Subscriber was closed meanwhile, by OnReceived
	// for one.
	OnReceived()
}

// LateAckMetrics is an extension of Metrics: a Metrics value of a Publisher
// or a Subscriber that also has OnLateAck is told of every acknowledgement
// from the broker that nothing waits for.
type LateAckMetrics interface {
	// OnLateAck is called for an acknowledgement (PUBACK, SUBACK or
	// UNSUBACK) of a packet identifier that no request of the connection
	// waits for: the answer to a publish or a subscription that was given
	// up on, which came after all, or one that answers nothing. It is
	// dropped, and completes no other request. OnLateAck runs on the
	// goroutine that reads the connection. Like the other callbacks it must
	// not block, and one that does not return normally is logged in the
	// same way.
	OnLateAck()
}

// lateAck returns the OnLateAck of m, called through log, or nil when m has
// none. enter, when not nil, is called first and the function it returns
// after.
func lateAck(log guard.Logger, m Metrics, enter func() (leave func())) func() {
	late, ok := m.(LateAckMetrics)
	if !ok {
		return nil
	}
	return func() {
		if enter != nil {
			defer enter()()
		}
		log.Call("Metrics.OnLateAck", late.OnLateAck)
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
	// Metrics receives a callback for what happens; see Metrics. Nil
	// disables them.
	Metrics Metrics
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
	cfg      PublisherConfig
	log      guard.Logger
	queue    chan message
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	// running bounds what run waits for; Close ends it, with stop. It is a
	// pointer so that Publisher stays comparable.
	running *runContext
	// waitedFor are the goroutines Close waits for: that of run, and one
	// that reads a connection while it is in OnLateAck, which run may wait
	// for. A Close on one of them does not wait. A pointer, as running.
	waitedFor *goid.Set

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
		return nil, fmt.Errorf("%w: %w", openadr.ErrConfiguration, err)
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 1024
	}
	p := &Publisher{cfg: cfg, log: guard.NewLogger(cfg.Logger, "mqtt"), queue: make(chan message, cfg.Queue), stop: make(chan struct{}), done: make(chan struct{}),
		waitedFor: &goid.Set{}}
	ctx, cancel := context.WithCancel(context.Background())
	p.running = &runContext{ctx: ctx, cancel: cancel}
	go p.run()
	return p, nil
}

func checkURL(uri string) error {
	_, _, _, err := imqtt.ParseURL(uri)
	return err
}

// Publish queues a notification for a topic and returns at once.
func (p *Publisher) Publish(topic string, body []byte) {
	if !p.enqueue(message{topic: topic, body: body}) {
		// Reported once the lock is released: a Logger or Metrics that
		// is slow holds up this call and no other.
		p.warn("mqtt: queue full, notification dropped", slog.String("topic", topic))
		metric(p.log, p.cfg.Metrics, "Metrics.OnDropped", Metrics.OnDropped)
	}
}

// enqueue queues m, and reports false when the queue is full. After Close
// it drops m and reports true: there is nothing to report.
func (p *Publisher) enqueue(m message) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return true
	}
	select {
	case p.queue <- m:
		return true
	default:
		return false
	}
}

// Close stops accepting notifications and waits until those that are queued
// have reached the broker, or until ctx ends, whichever is first. It may be
// called more than once and from several goroutines at the same time; each
// call waits in the same way.
//
// Close may also be called from the Metrics callbacks and the Logger's
// handler. On a goroutine of the Publisher, which it would wait for, it does
// not wait: it stops accepting notifications and returns nil at once, and
// the Publisher goes on, once that callback has returned, until what is
// queued has reached the broker or ctx has ended, as it does for a Close on
// another goroutine. OnDropped for a full queue, and its log entry, run on
// the goroutine that called Publish: a Close there waits.
func (p *Publisher) Close(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	if p.waitedFor.Has() {
		go func() { _ = p.wait(ctx) }()
		return nil
	}
	return p.wait(ctx)
}

// wait waits until run has ended, or until ctx ends: then what is queued,
// and a PUBLISH waiting for its acknowledgement, are given up.
func (p *Publisher) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		p.stopOnce.Do(func() {
			close(p.stop)
			p.running.cancel()
		})
		<-p.done
		return ctx.Err()
	}
}

func (p *Publisher) warn(msg string, attrs ...any) {
	p.log.Warn(msg, attrs...)
}

// metric calls a callback of m, when there is an m, through log.
func metric(log guard.Logger, m Metrics, callback string, f func(Metrics)) {
	if m != nil {
		log.Call(callback, func() { f(m) })
	}
}

func (p *Publisher) run() {
	defer close(p.done)
	defer p.running.cancel()
	defer p.waitedFor.Enter()()
	o := p.cfg.options()
	o.OnLateAck = lateAck(p.log, p.cfg.Metrics, p.waitedFor.Enter)
	var conn *imqtt.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	// wait is the pause after a connection that took a message and lost
	// it: a broker that accepts every connection and drops it at the first
	// PUBLISH is not dialled again at once, and for ever.
	wait := p.cfg.firstWait()
	for m := range p.queue {
		// Until the broker has it: a message that failed is sent again on
		// the next connection, before anything that came after it.
		for {
			if conn == nil {
				conn = p.cfg.connect(p.stop, o, func(err error) {
					p.warn("mqtt: cannot connect to the broker", slog.Any("error", err))
				})
				if conn == nil {
					return // closed while the broker was away
				}
				metric(p.log, p.cfg.Metrics, "Metrics.OnConnected", Metrics.OnConnected)
			}
			ctx, cancel := context.WithTimeout(p.running.ctx, 30*time.Second)
			err := conn.Publish(ctx, m.topic, m.body)
			cancel()
			if err == nil {
				metric(p.log, p.cfg.Metrics, "Metrics.OnPublished", Metrics.OnPublished)
				wait = p.cfg.firstWait()
				break
			}
			if errors.Is(err, imqtt.ErrTopic) || errors.Is(err, imqtt.ErrTooLong) {
				// Fails on every connection: sending it again would hold
				// up the queue for ever. The connection is not affected.
				p.warn("mqtt: notification cannot be published, dropped", slog.String("topic", clip(m.topic)), slog.String("error", clip(err.Error())))
				metric(p.log, p.cfg.Metrics, "Metrics.OnDropped", Metrics.OnDropped)
				break
			}
			if p.running.ctx.Err() != nil {
				return // Close gave up on it; the connection is closed on the way out
			}
			metric(p.log, p.cfg.Metrics, "Metrics.OnDisconnected", func(m Metrics) { m.OnDisconnected(err) })
			p.warn("mqtt: publishing failed, reconnecting", slog.String("topic", m.topic), slog.Any("error", err))
			_ = conn.Close()
			conn = nil
			if !pause(p.stop, wait) {
				return
			}
			wait = nextWait(wait)
		}
	}
}

// runContext is the context of a Publisher's run goroutine.
type runContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// maxLogged is the most bytes of a string from the broker, or of an error
// that quotes one, that goes into a log entry.
const maxLogged = 256

// clip shortens s to maxLogged bytes, at a character boundary, and says
// how long it was.
func clip(s string) string {
	if len(s) <= maxLogged {
		return s
	}
	n := maxLogged
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return fmt.Sprintf("%s... (%d bytes)", s[:n], len(s))
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
	// else is received meanwhile. It may call Close (see Close). A Notify
	// that does not return normally is logged at error level, as
	// "mqtt: callback did not return normally" with the fields callback,
	// value and stack; the message is acknowledged and the Subscriber
	// goes on with the next.
	Notify func(ctx context.Context, topic string, n openadr.Notification)
	// Logger receives connection failures and messages that are not
	// notifications. Nil disables logging.
	Logger *slog.Logger
	// Metrics receives a callback for what happens; see Metrics. Nil
	// disables them.
	Metrics Metrics
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
	cfg      SubscriberConfig
	log      guard.Logger
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}

	mu        sync.Mutex
	connected chan struct{} // closed while subscribed
	sessions  int

	// calling is held while Notify runs, so that Close can wait for it.
	calling sync.Mutex
	// runner is the goroutine of run, and notifying the one in Notify
	// (0 when none): a Close called on one of them does not wait for it.
	runner, notifying atomic.Uint64
}

var codec = v310.Codec()

// NewSubscriber returns a running Subscriber. It does not wait for the
// broker; use Ready for that.
func NewSubscriber(cfg SubscriberConfig) (*Subscriber, error) {
	s, err := newSubscriber(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", openadr.ErrConfiguration, err)
	}
	return s, nil
}

func newSubscriber(cfg SubscriberConfig) (*Subscriber, error) {
	if err := checkURL(cfg.URL); err != nil {
		return nil, err
	}
	if len(cfg.Topics) == 0 {
		return nil, errors.New("mqtt: a Subscriber needs Topics")
	}
	if slices.Contains(cfg.Topics, "") {
		return nil, errors.New("mqtt: an empty topic filter")
	}
	if cfg.Notify == nil {
		return nil, errors.New("mqtt: a Subscriber needs Notify")
	}
	s := &Subscriber{cfg: cfg, log: guard.NewLogger(cfg.Logger, "mqtt"), stop: make(chan struct{}), done: make(chan struct{}), connected: make(chan struct{})}
	go s.run()
	return s, nil
}

// Ready waits until the Subscriber is connected and subscribed: from then
// on, what the VTN publishes is received.
func (s *Subscriber) Ready(ctx context.Context) error {
	closed := fmt.Errorf("%w: the MQTT subscriber", openadr.ErrClosed)
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

// Close disconnects and stops the Subscriber, and returns when it has
// stopped and a Notify in progress has returned. Notify is not called after
// Close has returned.
//
// Close may be called more than once, from several goroutines at the same
// time, and from Notify, the Metrics callbacks and the Logger's handler.
// From one of those it does not wait for the callback it is called from,
// which goes on until it returns. Called from Notify, OnReceived or
// OnLateAck, it returns when the rest of the Subscriber has stopped.
// Called from OnConnected, OnDisconnected or the Logger's handler, it may
// return before that: the Subscriber stops once that callback has
// returned, and a Notify in progress goes on until it returns. In every case no Notify
// begins after Close has returned. A goroutine that Notify waits for is not
// Notify: a Close there waits for Notify, which must not wait for it.
func (s *Subscriber) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	me := goid.ID()
	if me != 0 && me == s.runner.Load() {
		// A callback on the goroutine that Close would wait for: it stops
		// when the callback has returned. A Notify in progress is not
		// waited for either: it may call Close, which waits for this
		// goroutine.
		return nil
	}
	<-s.done
	if me == 0 || me != s.notifying.Load() {
		// A call to Notify that had begun is over when this lock is had,
		// unless Close is called from it.
		s.calling.Lock()
		defer s.calling.Unlock()
	}
	return nil
}

// stopped reports whether Close has been called.
func (s *Subscriber) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Subscriber) warn(msg string, attrs ...any) {
	s.log.Warn(msg, attrs...)
}

func (s *Subscriber) run() {
	defer close(s.done)
	s.runner.Store(goid.ID())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.stop
		cancel()
	}()

	o := s.cfg.options()
	o.OnLateAck = lateAck(s.log, s.cfg.Metrics, nil)
	o.OnMessage = func(topic string, payload []byte) {
		n, err := codec.UnmarshalNotification(payload)
		if err != nil {
			s.warn("mqtt: a message that is not a notification", slog.String("topic", clip(topic)), slog.String("error", clip(err.Error())))
			return
		}
		// What does not return normally is reported once calling is
		// released.
		var received, notified *guard.Failure
		s.calling.Lock()
		if !s.stopped() {
			s.notifying.Store(goid.ID())
			if s.cfg.Metrics != nil {
				received = guard.Try("Metrics.OnReceived", s.cfg.Metrics.OnReceived)
			}
			// Not after a Close that came meanwhile, from OnReceived for
			// one.
			if !s.stopped() {
				notified = guard.Try("Notify", func() { s.cfg.Notify(ctx, topic, n) })
			}
			s.notifying.Store(0)
		}
		s.calling.Unlock()
		s.log.Report(received)
		s.log.Report(notified)
	}
	// wait is the pause after a session that was lost: a broker that
	// accepts every connection and subscription and drops it at once is
	// not dialled again at once, and for ever.
	wait := s.cfg.firstWait()
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
		if err == nil {
			wait = s.cfg.afterSession(s.session(conn), wait)
		} else {
			_ = conn.Close()
			if ctx.Err() != nil {
				return
			}
			s.warn("mqtt: subscribing failed", slog.Any("error", err))
		}
		if !pause(s.stop, wait) {
			return
		}
		wait = nextWait(wait)
	}
}

// session runs a subscribed connection until it is lost or the subscriber
// stops, and returns how long it lasted.
func (s *Subscriber) session(conn *imqtt.Conn) time.Duration {
	// Reported before Ready returns: whoever was told the subscriber
	// is connected finds it in the metrics.
	metric(s.log, s.cfg.Metrics, "Metrics.OnConnected", Metrics.OnConnected)
	s.mu.Lock()
	s.sessions++
	close(s.connected)
	s.mu.Unlock()
	began := time.Now()

	select {
	case <-conn.Done():
		metric(s.log, s.cfg.Metrics, "Metrics.OnDisconnected", func(m Metrics) { m.OnDisconnected(conn.Err()) })
		s.warn("mqtt: connection to the broker lost", slog.Any("error", conn.Err()))
	case <-s.stop:
		// The pause that follows sees stop and ends the run.
		_ = conn.Close()
	}
	s.mu.Lock()
	s.connected = make(chan struct{})
	s.mu.Unlock()
	return time.Since(began)
}
