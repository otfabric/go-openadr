// SPDX-License-Identifier: MIT

// Package webhook is the webhook binding of OpenADR 3 notifications, for
// both sides: Sender delivers a VTN's notifications to the callback URLs of
// its subscriptions, and Handler receives them in a client.
package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/backoff"
	"github.com/otfabric/go-openadr/internal/goid"
	"github.com/otfabric/go-openadr/internal/guard"
)

// SenderConfig configures a Sender. The zero value is a Sender for
// production use: https only, public addresses only.
type SenderConfig struct {
	// HTTPClient makes the requests. When nil, a client is used that does
	// not follow redirects and connects to public addresses only. A client
	// given here is used as it is: its redirect and dial rules are its own.
	HTTPClient *http.Client
	// AllowInsecure accepts callback URLs with the http scheme, and lets the
	// default client connect to loopback and private addresses. For tests
	// and closed networks; OpenADR requires https.
	AllowInsecure bool
	// Timeout bounds one request; 10 seconds when zero.
	Timeout time.Duration
	// Attempts is how often a notification is sent at most; 4 when zero.
	Attempts int
	// Backoff is the wait before the second attempt, doubled for each one
	// after, up to MaxBackoff; 1 second when zero.
	Backoff time.Duration
	// MaxBackoff is the longest wait between two attempts; 1 minute when
	// zero.
	MaxBackoff time.Duration
	// Queue is the number of notifications that may wait per webhook host
	// (the host name of the callback URL), retries included; 256 when
	// zero. Queue times Workers may wait in all, and no less than twice
	// Queue. A notification for a full queue is dropped.
	Queue int
	// Workers is the number of deliveries that run at once, each to a
	// host of its own; 4 when zero.
	Workers int
	// Logger receives failed and dropped deliveries. Nil disables logging.
	Logger *slog.Logger
	// Metrics receives a callback for every notification; see Metrics. Nil
	// disables them.
	Metrics Metrics
}

// Metrics receives a callback for what becomes of each notification given
// to a Sender: exactly one of the three. The callbacks run on the
// Sender's goroutines, or for OnDropped on the caller's, without a lock of
// the Sender held, and must not block. Implementations must be safe for
// concurrent use.
//
// A callback that does not return normally is logged at error level, as
// "webhook: callback did not return normally" with the fields callback,
// value and stack, and the Sender goes on as if it had returned. So are
// the done function of SendConfirmed and the handler of the Logger, which
// loses that entry.
type Metrics interface {
	// OnDelivered is called when a webhook has confirmed a notification:
	// after how many attempts, and how long after the first began.
	OnDelivered(attempts int, duration time.Duration)
	// OnFailed is called when a notification is given up on: its attempts
	// are used up, the webhook refused it for good, or the Sender was
	// closed while it waited.
	OnFailed(attempts int, duration time.Duration, err error)
	// OnDropped is called when a notification is not even queued: the
	// queue of its webhook is full.
	OnDropped()
}

// Sender delivers notifications to webhooks. Notifications for one callback
// URL are delivered in the order they were sent, one at a time. A webhook
// host has at most one request on its way and at most Queue notifications
// waiting, and a notification that waits for its next attempt holds no
// worker: a webhook that is slow, down or asks to wait delays its own
// notifications and fills its own share of the queue, and nothing else.
//
// Delivery is at least once while the Sender runs: a notification is sent
// again when the webhook did not confirm it, so a webhook may see one twice.
// It is not durable: what is queued when the process stops is lost, and so
// is a notification whose attempts are used up.
//
// A Sender is safe for concurrent use. Close it to stop its workers.
type Sender struct {
	cfg    SenderConfig
	client *http.Client
	logger guard.Logger
	// perHost and total bound the notifications that wait: for one
	// webhook host, and for all of them.
	perHost, total int

	mu   sync.Mutex
	cond *sync.Cond // signalled when there is work, or nothing left
	// closed: no more notifications are accepted. stopping: Close ran out
	// of time, and what waits is given up on.
	closed, stopping bool
	lanes            map[string]*lane // by callback URL
	hosts            map[string]*host // by host name
	ready            []*host          // hosts with a lane ready and nothing on its way, in turn
	waiting          int              // notifications not on their way
	pending          int              // notifications not finished
	wg               sync.WaitGroup
	// workers are the goroutines of work, which Close waits for: a Close
	// on one of them does not wait.
	workers  goid.Set
	stopOnce sync.Once
	// stopped is cancelled when the workers have stopped after Close, or
	// Close ran out of time, and ends the requests on their way.
	stopped context.Context
	abort   context.CancelFunc
}

type delivery struct {
	url, token string
	body       []byte
	// done, when not nil, is told what became of the delivery.
	done func(error)

	// Kept between attempts, under the lock of the Sender.
	attempts int
	start    time.Time
	wait     time.Duration
	last     error
}

// lane is the notifications for one callback URL, in order. Only its head
// is attempted.
type lane struct {
	url   string
	host  *host
	queue []*delivery
	// inFlight: the head is on its way. timer: the head waits for its
	// next attempt.
	inFlight bool
	timer    *time.Timer
}

// host is what the Sender keeps for one webhook host.
type host struct {
	name     string
	count    int  // notifications not finished
	waiting  int  // notifications not on their way
	inFlight bool // one is on its way
	ready    []*lane
}

// afterFunc schedules the next attempt of a notification; tests may
// replace it.
var afterFunc = time.AfterFunc

// finish tells done what became of the delivery.
func (s *Sender) finish(d *delivery, err error) {
	if d.done != nil {
		s.logger.Call("SendConfirmed done", func() { d.done(err) })
	}
}

// metric calls a callback of the Metrics, when there are any.
func (s *Sender) metric(callback string, f func(Metrics)) {
	if s.cfg.Metrics != nil {
		s.logger.Call(callback, func() { f(s.cfg.Metrics) })
	}
}

// RefusedError is what SendConfirmed reports for a notification that
// another attempt cannot deliver: the webhook answered that it will not
// take it, or its URL is one the Sender does not send to.
type RefusedError struct{ Err error }

func (e *RefusedError) Error() string { return "webhook: refused for good: " + e.Err.Error() }
func (e *RefusedError) Unwrap() error { return e.Err }

// Permanent reports that trying again is of no use. A VTN that keeps
// notifications until they are delivered asks an error this way, so that
// it need not know this package.
func (e *RefusedError) Permanent() bool { return true }

// NewSender returns a running Sender.
func NewSender(cfg SenderConfig) *Sender {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 4
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = time.Minute
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 256
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	s := &Sender{
		cfg: cfg, client: cfg.HTTPClient, logger: guard.NewLogger(cfg.Logger, "webhook"),
		perHost: cfg.Queue, total: capacity(cfg.Queue, cfg.Workers),
		lanes: make(map[string]*lane), hosts: make(map[string]*host),
	}
	s.cond = sync.NewCond(&s.mu)
	s.stopped, s.abort = context.WithCancel(context.Background())
	if s.client == nil {
		s.client = defaultClient(cfg.AllowInsecure)
	}
	for range cfg.Workers {
		s.wg.Add(1)
		go s.work()
	}
	return s
}

// defaultClient is an HTTP client for requests to URLs that clients of the
// VTN chose: the server-side request forgery surface of a VTN.
func defaultClient(allowInsecure bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowInsecure {
		// Checked on the address that is actually connected to, after name
		// resolution, so that a name cannot resolve to something else
		// between a check and the connection.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("webhook: cannot tell where %s is", address)
			}
			if !public(ap.Addr()) {
				return fmt.Errorf("webhook: %s is not a public address", ap.Addr())
			}
			return nil
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       90 * time.Second,
			// The environment's proxy would make the connection for us, to
			// wherever the URL says.
			Proxy: nil,
		},
		// A redirect could lead anywhere, and the token would follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// refused are address ranges a webhook on the Internet does not have, that
// the classes of netip.Addr do not cover: the shared address space of RFC
// 6598, special-purpose IPv4 (RFC 6890, RFC 2544, RFC 1112), IPv4-compatible
// and IPv4-translated IPv6, deprecated site-local IPv6 (RFC 3879), the
// local-use NAT64 prefix of RFC 8215 and Teredo (RFC 4380), whose IPv4 part
// is not where the connection ends up.
var refused = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
}

var (
	// nat64 is the well-known NAT64 prefix of RFC 6052: the IPv4 address
	// is in the last 32 bits.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	// sixToFour is 6to4 (RFC 3056): the IPv4 address follows the prefix.
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// public reports whether an address is one a webhook on the Internet can
// have. An IPv6 address that stands for an IPv4 address is judged by that
// one.
func public(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range refused {
		if p.Contains(addr) {
			return false
		}
	}
	b := addr.As16()
	switch {
	case nat64.Contains(addr):
		return public(netip.AddrFrom4([4]byte(b[12:16])))
	case sixToFour.Contains(addr):
		return public(netip.AddrFrom4([4]byte(b[2:6])))
	}
	return true
}

// checkURL applies the rules for a callback URL that need no request.
func (s *Sender) checkURL(callbackURL string) (*url.URL, error) {
	u, err := url.Parse(callbackURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("is not an absolute URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && s.cfg.AllowInsecure:
	default:
		return nil, errors.New("must use https")
	}
	if u.User != nil {
		return nil, errors.New("must not carry credentials")
	}
	return u, nil
}

// Verify checks that a callback URL may be used and that whoever answers
// there expects notifications, as OpenADR requires before a subscription is
// accepted: the VTN sends GET <url>?echo=<random> and the webhook has to
// answer 200 with that value as the body.
//
// The error says what is wrong with the URL, in words fit for the client
// that named it.
func (s *Sender) Verify(ctx context.Context, callbackURL string) error {
	u, err := s.checkURL(callbackURL)
	if err != nil {
		return err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	echo := hex.EncodeToString(raw)
	query := u.Query()
	query.Set("echo", echo)
	u.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("is not a URL a request can be sent to")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// The cause may name addresses inside the VTN's network.
		return errors.New("could not be reached for verification")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered the verification request with %d, not 200", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != echo {
		return errors.New("did not answer the verification request with the value of its echo parameter")
	}
	return nil
}

// Send queues a notification for a webhook and returns at once. bearerToken
// is sent as the Authorization of the request when not empty.
func (s *Sender) Send(callbackURL, bearerToken string, body []byte) {
	s.SendConfirmed(callbackURL, bearerToken, body, nil)
}

// SendConfirmed is Send for a caller that wants to know what became of the
// notification. done is called exactly once, from another goroutine or,
// when the notification is not even queued, from this one: with nil when
// the webhook confirmed it, with a *RefusedError when it will never take
// it, and otherwise with the reason it was given up on for now: its
// attempts are used up, the queue was full, the Sender was closed. A VTN
// uses it to keep a notification until it is delivered. done may call
// Close (see Close).
func (s *Sender) SendConfirmed(callbackURL, bearerToken string, body []byte, done func(error)) {
	d := &delivery{url: callbackURL, token: bearerToken, body: body, done: done}
	name := hostOf(callbackURL)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.finish(d, fmt.Errorf("%w: the sender", openadr.ErrClosed))
		return
	}
	h := s.hosts[name]
	if (h != nil && h.waiting >= s.perHost) || s.waiting >= s.total {
		s.mu.Unlock()
		s.log("webhook: queue full, notification dropped", callbackURL, nil)
		s.metric("Metrics.OnDropped", Metrics.OnDropped)
		s.finish(d, errors.New("webhook: the queue is full"))
		return
	}
	if h == nil {
		h = &host{name: name}
		s.hosts[name] = h
	}
	l := s.lanes[callbackURL]
	if l == nil {
		l = &lane{url: callbackURL, host: h}
		s.lanes[callbackURL] = l
	}
	l.queue = append(l.queue, d)
	h.count++
	h.waiting++
	s.waiting++
	s.pending++
	if len(l.queue) == 1 {
		s.readied(l)
	}
	s.mu.Unlock()
}

// capacity is how many notifications may wait in all: Queue per host slot,
// and room for two hosts at least, so that one host never fills it.
func capacity(queue, workers int) int {
	n := max(workers, 2)
	if queue > math.MaxInt/n {
		return math.MaxInt
	}
	return queue * n
}

// hostOf is the host a callback URL names, the key of its share.
func hostOf(callbackURL string) string {
	u, err := url.Parse(callbackURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// readied makes the head of a lane one that may be attempted. Called with
// s.mu held.
func (s *Sender) readied(l *lane) {
	h := l.host
	h.ready = append(h.ready, l)
	if !h.inFlight && len(h.ready) == 1 {
		s.ready = append(s.ready, h)
		s.cond.Signal()
	}
}

// released ends the request a host had on its way. Called with s.mu held.
func (s *Sender) released(h *host) {
	h.inFlight = false
	if len(h.ready) > 0 {
		s.ready = append(s.ready, h)
		s.cond.Signal()
	}
}

// next takes the head of the next lane that may be attempted, the hosts in
// turn. Called with s.mu held.
func (s *Sender) next() (*lane, *delivery) {
	if len(s.ready) == 0 {
		return nil, nil
	}
	h := s.ready[0]
	s.ready[0] = nil
	s.ready = s.ready[1:]
	l := h.ready[0]
	h.ready[0] = nil
	h.ready = h.ready[1:]
	h.inFlight, l.inFlight = true, true
	h.waiting--
	s.waiting--
	return l, l.queue[0]
}

// removed accounts for a notification that is finished and no longer in
// its lane. Called with s.mu held.
func (s *Sender) removed(l *lane) {
	h := l.host
	h.count--
	s.pending--
	if len(l.queue) == 0 && s.lanes[l.url] == l {
		delete(s.lanes, l.url)
	}
	if h.count == 0 && s.hosts[h.name] == h {
		delete(s.hosts, h.name)
	}
	if s.pending == 0 {
		s.cond.Broadcast()
	}
}

// takeWaiting takes a notification that is not on its way, to be given up
// on. Called with s.mu held, once Close ran out of time.
func (s *Sender) takeWaiting() *delivery {
	for _, l := range s.lanes {
		i := 0
		if l.inFlight {
			i = 1
		}
		if len(l.queue) <= i {
			continue
		}
		d := l.queue[i]
		l.queue = append(l.queue[:i], l.queue[i+1:]...)
		if i == 0 && l.timer != nil {
			l.timer.Stop()
			l.timer = nil
		}
		l.host.waiting--
		s.waiting--
		s.removed(l)
		return d
	}
	return nil
}

// Close stops accepting notifications and waits until those that are queued
// have been delivered or given up on, or until ctx ends, whichever is first.
// When ctx ends first, the requests on their way are cancelled and what is
// still queued is given up on without being sent. It may be called more
// than once and from several goroutines at the same time; each call waits
// in the same way.
//
// Close may also be called from the Metrics callbacks, the done function of
// SendConfirmed and the Logger's handler. On a goroutine of the Sender,
// which it would wait for, it does not wait: it stops accepting
// notifications and returns nil at once, and the Sender goes on, once that
// callback has returned, until what is queued has been delivered or given
// up on, or until ctx ends, as it does for a Close on another goroutine.
// OnDropped, and a done function called before SendConfirmed returns, run
// on the goroutine that called SendConfirmed: a Close there waits.
func (s *Sender) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cond.Broadcast()
	}
	s.mu.Unlock()
	if s.workers.Has() {
		go func() { _ = s.wait(ctx) }()
		return nil
	}
	return s.wait(ctx)
}

// wait waits until the workers have stopped, or until ctx ends: then the
// requests on their way are cancelled and what is still queued is given up
// on.
func (s *Sender) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.abort()
		return nil
	case <-ctx.Done():
		// Give up on what is still waiting, and on the requests on their
		// way.
		s.stopOnce.Do(func() {
			s.mu.Lock()
			s.stopping = true
			s.cond.Broadcast()
			s.mu.Unlock()
		})
		s.abort()
		<-done
		return ctx.Err()
	}
}

// work attempts notifications until the Sender is closed and nothing is
// left, or Close ran out of time and nothing is left to give up on.
func (s *Sender) work() {
	defer s.wg.Done()
	defer s.workers.Enter()()
	s.mu.Lock()
	for {
		if s.stopping {
			if d := s.takeWaiting(); d != nil {
				s.mu.Unlock()
				s.failed(d, d.attempts, s.closedError(d.last))
				s.mu.Lock()
				continue
			}
		} else if l, d := s.next(); l != nil {
			s.mu.Unlock()
			s.attempt(l, d)
			s.mu.Lock()
			continue
		}
		if s.closed && (s.pending == 0 || s.stopping) {
			s.mu.Unlock()
			return
		}
		s.cond.Wait()
	}
}

// closedError is why a notification is given up on when the Sender is
// closed: after the error of its last attempt, if it had one.
func (s *Sender) closedError(last error) error {
	if last == nil {
		return fmt.Errorf("%w: the sender", openadr.ErrClosed)
	}
	return fmt.Errorf("%w: the sender, after: %w", openadr.ErrClosed, last)
}

// failed reports a notification that is given up on.
func (s *Sender) failed(d *delivery, attempts int, err error) {
	var took time.Duration
	if !d.start.IsZero() {
		took = time.Since(d.start)
	}
	s.log("webhook: notification not delivered", d.url, err)
	s.metric("Metrics.OnFailed", func(m Metrics) { m.OnFailed(attempts, took, err) })
	s.finish(d, err)
}

// attempt makes one attempt of the head of a lane, and then either
// finishes it or schedules its next attempt, without waiting for it.
func (s *Sender) attempt(l *lane, d *delivery) {
	if d.attempts == 0 {
		d.start, d.wait = time.Now(), s.cfg.Backoff
	}
	d.attempts++
	retry, after, err := s.post(d)
	if err != nil && !retry {
		err = &RefusedError{Err: err}
	}
	again := err != nil && retry && d.attempts < s.cfg.Attempts
	if again && s.stopped.Err() != nil {
		again, err = false, s.closedError(err)
	}

	s.mu.Lock()
	h := l.host
	l.inFlight = false
	if again && s.stopping {
		again, err = false, s.closedError(err)
	}
	if again {
		// A webhook that says when to come back is taken at its word, up
		// to the longest wait there is. The notification waits in its
		// lane, and holds no worker.
		delay := min(max(d.wait, after), s.cfg.MaxBackoff)
		d.wait = backoff.Double(d.wait, s.cfg.MaxBackoff)
		d.last = err
		h.waiting++
		s.waiting++
		l.timer = afterFunc(delay, func() { s.retry(l) })
		s.released(h)
		s.mu.Unlock()
		return
	}
	l.queue[0] = nil
	l.queue = l.queue[1:]
	if len(l.queue) > 0 {
		s.readied(l)
	}
	s.released(h)
	s.removed(l)
	s.mu.Unlock()

	if err == nil {
		s.metric("Metrics.OnDelivered", func(m Metrics) { m.OnDelivered(d.attempts, time.Since(d.start)) })
		s.finish(d, nil)
		return
	}
	s.failed(d, d.attempts, err)
}

// retry makes the head of a lane ready for its next attempt, when its wait
// is over.
func (s *Sender) retry(l *lane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.timer == nil || s.stopping {
		return
	}
	l.timer = nil
	s.readied(l)
}

// post sends one request. retry says whether another attempt can help, and
// after how long the webhook asked to wait first, if it did.
func (s *Sender) post(d *delivery) (retry bool, after time.Duration, err error) {
	if _, err := s.checkURL(d.url); err != nil {
		return false, 0, err
	}
	ctx, cancel := context.WithTimeout(s.stopped, s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(d.body))
	if err != nil {
		return false, 0, redactError(err, d.url)
	}
	req.Header.Set("Content-Type", "application/json")
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return true, 0, redactError(err, d.url)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, 0, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout:
		return true, retryAfter(resp.Header.Get("Retry-After"), time.Now()), fmt.Errorf("the webhook answered %d", resp.StatusCode)
	default:
		// A redirect, or a refusal that will not change by asking again.
		return false, 0, fmt.Errorf("the webhook answered %d", resp.StatusCode)
	}
}

// retryAfter is the wait a Retry-After field asks for (RFC 9110, section
// 10.2.3): delay-seconds, digits only, or an HTTP-date; zero when there is
// none or it cannot be read. A number too large for a Duration is the
// largest one; the caller caps it.
func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.Trim(value, "0123456789") == "" {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return math.MaxInt64
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// redacted is a callback URL without its query, fragment and credentials,
// which may hold a secret of the subscriber.
func redacted(callbackURL string) string {
	if u, err := url.Parse(callbackURL); err == nil {
		u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment, u.User = "", false, "", "", nil
		return u.String()
	}
	if i := strings.IndexAny(callbackURL, "?#"); i >= 0 {
		callbackURL = callbackURL[:i]
	}
	if i := strings.LastIndex(callbackURL, "@"); i >= 0 {
		if j := strings.Index(callbackURL, "//"); j >= 0 && j < i {
			callbackURL = callbackURL[:j+2] + callbackURL[i+1:]
		}
	}
	return callbackURL
}

// redactError is err without the query of the callback URL: the URL of a
// *url.Error, which net/http reports in full, is redacted, and an error
// whose text still holds the query reads without it.
func redactError(err error, callbackURL string) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.URL != "" {
		err = &url.Error{Op: ue.Op, URL: redacted(ue.URL), Err: ue.Err}
	}
	secret := callbackURL
	if u, perr := url.Parse(callbackURL); perr == nil {
		secret = u.RawQuery
	} else if i := strings.IndexByte(callbackURL, '?'); i >= 0 {
		secret = callbackURL[i+1:]
	}
	if secret != "" && strings.Contains(err.Error(), secret) {
		return &redactedError{text: strings.ReplaceAll(err.Error(), secret, "…"), err: err}
	}
	return err
}

// redactedError reads as an error with a secret taken out of its text, and
// unwraps to it.
type redactedError struct {
	text string
	err  error
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.err }

// log reports a failure without the query of the URL, which may hold a
// secret of the subscriber.
func (s *Sender) log(msg, callbackURL string, err error) {
	if !s.logger.Active() {
		return
	}
	attrs := []any{slog.String("callback", redacted(callbackURL))}
	if err != nil {
		attrs = append(attrs, slog.String("error", redactError(err, callbackURL).Error()))
	}
	s.logger.Warn(msg, attrs...)
}
