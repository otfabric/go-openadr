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
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
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
	// Queue is the number of notifications that may wait per worker; 256
	// when zero. A notification for a full queue is dropped.
	Queue int
	// Workers is the number of deliveries that run at once; 4 when zero.
	Workers int
	// Logger receives failed and dropped deliveries. Nil disables logging.
	Logger *slog.Logger
}

// Sender delivers notifications to webhooks. Notifications for one callback
// URL are delivered in the order they were sent, one at a time; a webhook
// that is slow or down delays those that share its worker, and nothing else.
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

	mu     sync.Mutex
	closed bool
	queues []chan delivery
	wg     sync.WaitGroup
	// stop ends the waits between attempts when Close runs out of time.
	stop chan struct{}
}

type delivery struct {
	url, token string
	body       []byte
}

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
	s := &Sender{cfg: cfg, client: cfg.HTTPClient, stop: make(chan struct{})}
	if s.client == nil {
		s.client = defaultClient(cfg.AllowInsecure)
	}
	s.queues = make([]chan delivery, cfg.Workers)
	for i := range s.queues {
		s.queues[i] = make(chan delivery, cfg.Queue)
		s.wg.Add(1)
		go s.work(s.queues[i])
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

// shared is 100.64.0.0/10, the shared address space of RFC 6598.
var shared = netip.MustParsePrefix("100.64.0.0/10")

// public reports whether an address is one a webhook on the Internet can
// have.
func public(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() && addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() && !shared.Contains(addr)
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
	h := fnv.New32a()
	_, _ = h.Write([]byte(callbackURL))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.queues[int(h.Sum32()%uint32(len(s.queues)))] <- delivery{url: callbackURL, token: bearerToken, body: body}:
	default:
		s.log("webhook: queue full, notification dropped", callbackURL, nil)
	}
}

// Close stops accepting notifications and waits until those that are queued
// have been delivered or given up on, or until ctx ends, whichever is first.
func (s *Sender) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for _, q := range s.queues {
			close(q)
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Give up on what is still waiting between attempts.
		select {
		case <-s.stop:
		default:
			close(s.stop)
		}
		<-done
		return ctx.Err()
	}
}

func (s *Sender) work(queue chan delivery) {
	defer s.wg.Done()
	for d := range queue {
		s.deliver(d)
	}
}

func (s *Sender) deliver(d delivery) {
	wait := s.cfg.Backoff
	for attempt := 1; ; attempt++ {
		retry, err := s.post(d)
		if err == nil {
			return
		}
		if !retry || attempt >= s.cfg.Attempts {
			s.log("webhook: notification not delivered", d.url, err)
			return
		}
		select {
		case <-time.After(wait):
		case <-s.stop:
			s.log("webhook: notification not delivered", d.url, errors.New("the sender was closed"))
			return
		}
		if wait *= 2; wait > s.cfg.MaxBackoff {
			wait = s.cfg.MaxBackoff
		}
	}
}

// post sends one request. retry says whether another attempt can help.
func (s *Sender) post(d delivery) (retry bool, err error) {
	if _, err := s.checkURL(d.url); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(d.body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout:
		return true, fmt.Errorf("the webhook answered %d", resp.StatusCode)
	default:
		// A redirect, or a refusal that will not change by asking again.
		return false, fmt.Errorf("the webhook answered %d", resp.StatusCode)
	}
}

// log reports a failure without the query of the URL, which may hold a
// secret of the subscriber.
func (s *Sender) log(msg, callbackURL string, err error) {
	if s.cfg.Logger == nil {
		return
	}
	where := callbackURL
	if u, perr := url.Parse(callbackURL); perr == nil {
		u.RawQuery, u.User = "", nil
		where = u.String()
	}
	attrs := []any{slog.String("callback", where)}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	s.cfg.Logger.Warn(msg, attrs...)
}
