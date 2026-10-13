// SPDX-License-Identifier: MIT

// Package server is an OpenADR 3.1.0 VTN as an http.Handler.
//
// The Server implements the REST API and its access rules. It leaves to the
// embedding program what a VTN deployment decides for itself: the listener
// and TLS, where tokens come from (auth.Validator) and where objects are
// kept (storage.Store).
//
// Programs, events, reports, VENs and resources are always served.
// Subscriptions are served when the Server is given a way to deliver their
// notifications, a WebhookSender or WebSockets, and GET /notifiers when it
// has any way to notify, MQTT included. Without, they answer 501 Not
// Implemented, as the MQTT topic operations do without Config.MQTT.
//
// Two things a Server can do are not part of OpenADR 3.1.0, and are off
// unless Config says otherwise: notifications over a WebSocket and resource
// groups. Both are extensions shared with OpenLEADR.
//
// Mount the handler at the root of the API. To serve it under a path prefix,
// wrap it in http.StripPrefix.
package server

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/internal/wire"
	v310 "github.com/otfabric/go-openadr/internal/wire/v310"
	"github.com/otfabric/go-openadr/server/storage"
)

// Config configures a Server.
type Config struct {
	// Store holds the VTN's objects. Required.
	Store storage.Store
	// Validator resolves bearer tokens. Required, except for a VTN that
	// only serves clients that do not authenticate (AllowAnonymous).
	Validator auth.Validator
	// TokenURL is the OAuth2 token endpoint that GET /auth/server announces
	// to clients. Required with a Validator, and an absolute http or https
	// URL: the schema says so, and clients of other implementations do not resolve a
	// relative reference. For a VTN that serves its own token endpoint (see
	// TokenHandler) it is the public URL of the API followed by /auth/token.
	TokenURL string
	// TokenHandler serves POST /auth/token, for a VTN that issues its own
	// tokens (see auth.Issuer). When nil the endpoint answers 501.
	TokenHandler http.Handler
	// Webhooks delivers notifications and verifies callback URLs; see
	// package notification/webhook for the implementation. When nil, the
	// VTN has no subscriptions to webhooks, and without WebSocket either,
	// no subscriptions at all.
	Webhooks WebhookSender
	// MQTT publishes notifications to a broker and makes the VTN announce
	// it; see package notification/mqtt for a publisher. When nil, the
	// MQTT topic operations answer 501.
	MQTT *MQTTConfig
	// WebSocket lets clients receive notifications over a WebSocket, which
	// is not part of OpenADR 3.1.0; see WebSocketConfig. When nil,
	// /notifiers/ws does not exist. Call Close when the VTN stops.
	WebSocket *WebSocketConfig
	// ResourceGroups makes the VTN serve /resource_groups, which is not
	// part of OpenADR 3.1.0: see openadr.ResourceGroup. Without it the
	// path does not exist.
	ResourceGroups bool
	// AllowAnonymous makes the VTN answer requests that carry no token, as
	// the specification allows for public information such as price
	// schedules ("Non-Authenticating VTNs"). Such a client can read the
	// programs and events that have no targets, and ask how the VTN
	// notifies; every other operation still needs a token. A request that
	// does carry a token is validated as usual.
	AllowAnonymous bool
	// DisableCompression turns off compression of responses. By default a
	// response of 1 KiB or more is compressed, with gzip or deflate, for a
	// client that accepts one of them.
	DisableCompression bool
	// Logger receives internal errors, with the method and the path of the
	// request and never its body or token; and at debug level one line
	// per request, with its operation, status and duration. Nil disables
	// logging.
	Logger *slog.Logger
	// Outbox makes webhook notifications survive: each is recorded there
	// before it is sent and forgotten when its webhook has confirmed it,
	// and what is still recorded is sent again, when the VTN starts and
	// while it runs. Delivery is then at least once across restarts, where
	// without an Outbox what is on its way when the VTN stops is lost.
	// storage.File is an Outbox as well as a Store; so is storage.Memory,
	// which lasts as long as the process and so only adds the sending
	// again. It needs Webhooks to be a ConfirmingSender, which
	// *webhook.Sender is, and Close to be called when the VTN stops.
	// Notifications by MQTT and over WebSockets are not kept.
	Outbox storage.Outbox
	// NotificationTTL is how long a notification that cannot be delivered
	// is kept before it is given up; a day when zero. Only with Outbox.
	NotificationTTL time.Duration
	// RedeliverEvery is how often the VTN hands to the sender again what
	// is recorded and not on its way; a minute when zero. Only with Outbox.
	RedeliverEvery time.Duration
	// Limiter decides, for every request of a client it has identified,
	// whether the VTN takes it now; see Limiter and NewRateLimiter. A
	// request it refuses is answered 429 with a Retry-After header, which
	// the clients of this module honour. Nil takes every request.
	Limiter Limiter
	// NotifyReads makes the VTN notify the subscribers that asked for the
	// operation READ whenever an object is read, by ID or in a search.
	// Off by default: OpenADR 3.1.0 lists READ among the operations of a
	// subscription and says nothing of what it means, it is one
	// notification per object per read, and it costs the VTN a look at
	// its subscriptions for every read. Without it a subscription to READ
	// is accepted and never notified of one.
	NotifyReads bool
	// Metrics receives a callback for every request, and, when it
	// implements them, for notifications and WebSockets; see Metrics. Nil
	// disables them.
	Metrics Metrics
	// MaxBodyBytes limits the size of a request body; 1 MiB when zero.
	MaxBodyBytes int64
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// WebhookSender is what the VTN needs to notify by webhook. *webhook.Sender
// implements it.
type WebhookSender interface {
	// Verify checks, before a subscription is accepted, that a callback
	// URL may be used and that a webhook answers there. Its error is shown
	// to the client that named the URL.
	Verify(ctx context.Context, callbackURL string) error
	// Send delivers a notification in the background and returns at once.
	// bearerToken is empty when the subscription has none.
	Send(callbackURL, bearerToken string, body []byte)
}

// MQTTConfig is the MQTT binding of a VTN's notifications.
type MQTTConfig struct {
	// Publisher sends the notifications. Required. *mqtt.Publisher of
	// package notification/mqtt implements it.
	Publisher MQTTPublisher
	// URIs are the addresses of the broker as clients reach it, such as
	// mqtts://broker.example.com. Required: GET /notifiers announces them.
	URIs []string
	// Authentication says how clients log in to the broker, for
	// GET /notifiers. The zero value announces anonymous access.
	Authentication openadr.MQTTAuthentication
	// TopicPrefix is put before every topic, for a broker that is shared
	// with other applications. It should end in a slash.
	TopicPrefix string
}

// MQTTPublisher is what the VTN needs to notify by MQTT.
type MQTTPublisher interface {
	// Publish sends a message in the background and returns at once.
	Publish(topic string, body []byte)
}

// Server is a VTN. It is safe for concurrent use and holds no state besides
// its Store and, with Config.WebSocket, the connections of its clients.
type Server struct {
	store     storage.Store
	validator auth.Validator
	webhooks  WebhookSender
	mqtt      *MQTTConfig
	sockets   *sockets
	outbox    *outbox
	limiter   Limiter
	reads     bool
	anonymous bool
	compress  bool
	codec     *wire.Codec
	logger    *slog.Logger
	metrics   Metrics
	// The optional extensions of metrics, nil when it has not got them.
	notifications NotificationMetrics
	socketMetrics SocketMetrics
	maxBody       int64
	now           func() time.Time
	mux           *http.ServeMux
}

// New returns a Server for cfg.
func New(cfg Config) (*Server, error) {
	s, err := newServer(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", openadr.ErrConfiguration, err)
	}
	return s, nil
}

func newServer(cfg Config) (*Server, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("server: Config.Store is required")
	case cfg.Validator == nil && !cfg.AllowAnonymous:
		return nil, errors.New("server: Config.Validator is required")
	case cfg.TokenURL == "" && cfg.Validator != nil:
		return nil, errors.New("server: Config.TokenURL is required")
	}
	if cfg.Validator == nil {
		// Nobody can authenticate: every token is unknown.
		cfg.Validator = auth.ValidatorFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{}, auth.ErrInvalidToken
		})
	}
	if cfg.MQTT != nil {
		if cfg.MQTT.Publisher == nil || len(cfg.MQTT.URIs) == 0 {
			return nil, errors.New("server: Config.MQTT needs a Publisher and the URIs of the broker")
		}
		if cfg.MQTT.Authentication.Method == "" {
			cfg.MQTT.Authentication.Method = openadr.MQTTAnonymous
		}
	}
	if u, err := url.Parse(cfg.TokenURL); cfg.TokenURL != "" && (err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "") {
		return nil, fmt.Errorf("server: Config.TokenURL %q is not an absolute http or https URL", cfg.TokenURL)
	}
	s := &Server{
		store:     cfg.Store,
		validator: cfg.Validator,
		webhooks:  cfg.Webhooks,
		mqtt:      cfg.MQTT,
		limiter:   cfg.Limiter,
		reads:     cfg.NotifyReads,
		anonymous: cfg.AllowAnonymous,
		compress:  !cfg.DisableCompression,
		codec:     v310.Codec(),
		logger:    cfg.Logger,
		metrics:   cfg.Metrics,
		maxBody:   cfg.MaxBodyBytes,
		now:       cfg.Now,
		mux:       http.NewServeMux(),
	}
	if cfg.Outbox != nil {
		sender, ok := cfg.Webhooks.(ConfirmingSender)
		if !ok {
			return nil, errors.New("server: Config.Outbox needs Config.Webhooks to be a ConfirmingSender, such as *webhook.Sender")
		}
		// After everything it uses: it starts to send at once.
		defer func() { s.outbox = newOutbox(s, cfg.Outbox, sender, cfg.NotificationTTL, cfg.RedeliverEvery) }()
	}
	s.notifications, _ = cfg.Metrics.(NotificationMetrics)
	s.socketMetrics, _ = cfg.Metrics.(SocketMetrics)
	if cfg.WebSocket != nil {
		s.sockets = newSockets(*cfg.WebSocket)
	}
	if s.maxBody <= 0 {
		s.maxBody = 1 << 20
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.routes(cfg)
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.observe(w, r)
}

// handler is an API operation, called for an authenticated and authorized
// client.
type handler func(w http.ResponseWriter, r *http.Request, id auth.Identity)

func (s *Server) routes(cfg Config) {
	// paths collects the methods of each path, for the 405 fallback.
	paths := map[string][]string{}
	handle := func(method, path string, h http.Handler) {
		s.mux.Handle(method+" "+path, h)
		paths[path] = append(paths[path], method)
	}
	op := func(method, path, scope string, h handler) {
		handle(method, path, s.authorized(scope, h))
	}

	op("GET", "/programs", auth.ScopeReadTargets, s.listPrograms)
	op("POST", "/programs", auth.ScopeWritePrograms, s.createProgram)
	op("GET", "/programs/{id}", auth.ScopeReadTargets, s.getProgram)
	op("PUT", "/programs/{id}", auth.ScopeWritePrograms, s.updateProgram)
	op("DELETE", "/programs/{id}", auth.ScopeWritePrograms, s.deleteProgram)

	op("GET", "/events", auth.ScopeReadTargets, s.listEvents)
	op("POST", "/events", auth.ScopeWriteEvents, s.createEvent)
	op("GET", "/events/{id}", auth.ScopeReadTargets, s.getEvent)
	op("PUT", "/events/{id}", auth.ScopeWriteEvents, s.updateEvent)
	op("DELETE", "/events/{id}", auth.ScopeWriteEvents, s.deleteEvent)

	op("GET", "/reports", auth.ScopeReadVENObjects, s.listReports)
	op("POST", "/reports", auth.ScopeWriteReports, s.createReport)
	op("GET", "/reports/{id}", auth.ScopeReadVENObjects, s.getReport)
	op("PUT", "/reports/{id}", auth.ScopeWriteReports, s.updateReport)
	op("DELETE", "/reports/{id}", auth.ScopeWriteReports, s.deleteReport)

	op("GET", "/vens", auth.ScopeReadVENObjects, s.listVENs)
	op("POST", "/vens", auth.ScopeWriteVENs, s.createVEN)
	op("GET", "/vens/{id}", auth.ScopeReadVENObjects, s.getVEN)
	op("PUT", "/vens/{id}", auth.ScopeWriteVENs, s.updateVEN)
	op("DELETE", "/vens/{id}", auth.ScopeWriteVENs, s.deleteVEN)

	op("GET", "/resources", auth.ScopeReadVENObjects, s.listResources)
	op("POST", "/resources", auth.ScopeWriteVENs, s.createResource)
	op("GET", "/resources/{id}", auth.ScopeReadVENObjects, s.getResource)
	op("PUT", "/resources/{id}", auth.ScopeWriteVENs, s.updateResource)
	op("DELETE", "/resources/{id}", auth.ScopeWriteVENs, s.deleteResource)

	if cfg.ResourceGroups {
		op("GET", "/resource_groups", auth.ScopeReadVENObjects, s.listResourceGroups)
		op("POST", "/resource_groups", auth.ScopeWriteVENs, businessLogicOnly(s, s.createResourceGroup))
		op("GET", "/resource_groups/{id}", auth.ScopeReadVENObjects, s.getResourceGroup)
		op("PUT", "/resource_groups/{id}", auth.ScopeWriteVENs, businessLogicOnly(s, s.updateResourceGroup))
		op("DELETE", "/resource_groups/{id}", auth.ScopeWriteVENs, businessLogicOnly(s, s.deleteResourceGroup))
	}

	handle("GET", "/auth/server", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.TokenURL == "" {
			s.problem(w, r, http.StatusNotImplemented, "this VTN does not authenticate its clients")
			return
		}
		body, err := s.codec.MarshalAuthServer(cfg.TokenURL)
		s.write(w, r, http.StatusOK, body, err)
	}))
	if cfg.TokenHandler != nil {
		handle("POST", "/auth/token", cfg.TokenHandler)
	} else {
		handle("POST", "/auth/token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.problem(w, r, http.StatusNotImplemented, "this VTN does not issue tokens; GET /auth/server names the token endpoint")
		}))
	}

	// What this VTN does not have. Authentication still applies.
	notImplemented := s.authorized("", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		s.problem(w, r, http.StatusNotImplemented, "this VTN does not implement "+strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0])
	})
	if cfg.Webhooks != nil || cfg.WebSocket != nil {
		op("GET", "/subscriptions", auth.ScopeReadVENObjects, s.listSubscriptions)
		op("POST", "/subscriptions", auth.ScopeWriteSubscriptions, s.createSubscription)
		op("GET", "/subscriptions/{id}", auth.ScopeReadVENObjects, s.getSubscription)
		op("PUT", "/subscriptions/{id}", auth.ScopeWriteSubscriptions, s.updateSubscription)
		op("DELETE", "/subscriptions/{id}", auth.ScopeWriteSubscriptions, s.deleteSubscription)
	} else {
		for _, path := range []string{"/subscriptions", "/subscriptions/{id}"} {
			s.mux.Handle(path, notImplemented)
		}
	}
	if cfg.WebSocket != nil {
		op("GET", "/notifiers/ws", "", s.serveSocket)
	}
	if cfg.Webhooks != nil || cfg.MQTT != nil || cfg.WebSocket != nil {
		// Every client may ask how the VTN notifies.
		op("GET", "/notifiers", "", s.getNotifiers)
	} else {
		s.mux.Handle("/notifiers", notImplemented)
	}
	if cfg.MQTT != nil {
		s.mqttRoutes(op)
	} else {
		s.mux.Handle("/notifiers/", notImplemented)
	}

	// A known path with another method is 405, anything else 404, both with
	// a problem body rather than the mux's plain text.
	for path, methods := range paths {
		allow := strings.Join(methods, ", ")
		s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			s.problem(w, r, http.StatusMethodNotAllowed, "")
		})
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.problem(w, r, http.StatusNotFound, "unrecognized URL")
	})
}

// authorized authenticates the request and requires scope before calling h.
// An empty scope requires authentication only.
func (s *Server) authorized(scope string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" && s.anonymous {
			// A client that does not authenticate, on a VTN that lets it.
			// It has no client ID, and so no objects and no targets.
			id := auth.Identity{Scopes: []string{auth.ScopeReadTargets}}
			if scope != "" && !id.Allows(scope) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				s.problem(w, r, http.StatusUnauthorized, "this operation needs a bearer token")
				return
			}
			if s.limited(w, r, id) {
				return
			}
			h(w, r, id)
			return
		}
		scheme, token, ok := strings.Cut(header, " ")
		token = strings.TrimSpace(token)
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.problem(w, r, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		id, err := s.validator.Validate(r.Context(), token)
		if errors.Is(err, auth.ErrInvalidToken) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			s.problem(w, r, http.StatusUnauthorized, "the bearer token is invalid or has expired")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if scope != "" && !id.Allows(scope) {
			s.problem(w, r, http.StatusForbidden, "the token lacks the scope "+scope)
			return
		}
		if s.limited(w, r, id) {
			return
		}
		h(w, r, id)
	})
}

// --- responses

func (s *Server) write(w http.ResponseWriter, r *http.Request, status int, body []byte, err error) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The answer depends on what the client can read.
	w.Header().Add("Vary", "Accept-Encoding")
	if coding := ""; s.compress && len(body) >= compressFrom {
		if coding = contentCoding(r.Header.Get("Accept-Encoding")); coding != "" {
			if packed, ok := compressBody(coding, body); ok {
				w.Header().Set("Content-Encoding", coding)
				body = packed
			}
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// compressFrom is the size from which a response is worth compressing.
const compressFrom = 1024

// contentCoding chooses between the content codings the VTN has, gzip and
// deflate, by what the client accepts (RFC 9110, section 12.5.3): the one
// with the higher weight, gzip when they weigh the same, and none when the
// client lists neither or refuses both. Those are the codings the standard
// library can write; br and zstd are not among them.
func contentCoding(accept string) string {
	weights := map[string]float64{}
	for _, part := range strings.Split(accept, ",") {
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding == "" {
			continue
		}
		weight := 1.0
		if name, value, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.EqualFold(strings.TrimSpace(name), "q") {
			q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || q < 0 || q > 1 {
				continue
			}
			weight = q
		}
		weights[coding] = weight
	}
	best, bestWeight := "", 0.0
	for _, coding := range []string{"gzip", "deflate"} {
		weight, listed := weights[coding]
		if !listed {
			// "*" stands for every coding that is not named.
			weight = weights["*"]
		}
		if weight > bestWeight {
			best, bestWeight = coding, weight
		}
	}
	return best
}

// compressBody encodes a body, and reports whether that made it smaller.
func compressBody(coding string, body []byte) ([]byte, bool) {
	var (
		packed bytes.Buffer
		zw     io.WriteCloser
	)
	switch coding {
	case "gzip":
		zw = gzip.NewWriter(&packed)
	case "deflate":
		// The coding HTTP calls deflate is the zlib format (RFC 1950)
		// around a deflate stream, not the bare stream.
		zw = zlib.NewWriter(&packed)
	default:
		return nil, false
	}
	if _, err := zw.Write(body); err != nil || zw.Close() != nil || packed.Len() >= len(body) {
		return nil, false
	}
	return packed.Bytes(), true
}

func (s *Server) problem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	body, err := s.codec.MarshalProblem(openadr.Problem{Title: http.StatusText(status), Status: status, Detail: detail})
	if err != nil {
		s.logError(r, err)
		body = nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// forbidden is an error a handler returns to refuse an authenticated client.
type forbidden string

func (f forbidden) Error() string { return string(f) }

// fail answers a request with the problem that fits err.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid    *openadr.ValidationError
		constraint *storage.ConstraintError
		tooLarge   *http.MaxBytesError
		refused    forbidden
	)
	switch {
	case errors.As(err, &invalid):
		s.problem(w, r, http.StatusBadRequest, invalid.Field+" "+invalid.Reason)
	case errors.As(err, &refused):
		s.problem(w, r, http.StatusForbidden, string(refused))
	case errors.As(err, &constraint):
		s.problem(w, r, http.StatusConflict, constraint.Detail)
	case errors.Is(err, openadr.ErrConflict):
		s.problem(w, r, http.StatusConflict, "")
	case errors.Is(err, openadr.ErrNotFound):
		s.problem(w, r, http.StatusNotFound, "")
	case errors.As(err, &tooLarge):
		s.problem(w, r, http.StatusRequestEntityTooLarge, "the request body exceeds "+strconv.FormatInt(tooLarge.Limit, 10)+" bytes")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client is gone or out of time; there is nobody to tell why.
		s.problem(w, r, http.StatusServiceUnavailable, "")
	default:
		s.logError(r, err)
		s.problem(w, r, http.StatusInternalServerError, "")
	}
}

func (s *Server) logError(r *http.Request, err error) {
	if s.logger != nil {
		s.logger.ErrorContext(r.Context(), "openadr: request failed",
			slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	}
}

// --- requests

func invalidParam(name, reason string) error {
	return &openadr.ValidationError{Field: "query parameter " + name, Reason: reason}
}

// readObject decodes and validates the request body.
func readObject[T interface{ Validate() error }](s *Server, w http.ResponseWriter, r *http.Request, c wire.ObjectCodec[T]) (T, wire.Form, error) {
	var zero T
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return zero, 0, err
		}
		return zero, 0, &openadr.ValidationError{Field: "body", Reason: "could not be read"}
	}
	v, form, err := c.Unmarshal(body)
	if err != nil {
		return zero, 0, err
	}
	if err := v.Validate(); err != nil {
		return zero, 0, err
	}
	return v, form, nil
}

// pathID returns the object ID in the request path.
func pathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !openadr.ValidID(id) {
		return "", &openadr.ValidationError{Field: "path", Reason: "does not end in an object ID"}
	}
	return id, nil
}

// queryID returns an optional query parameter that holds an object ID.
func queryID(r *http.Request, name string) (string, error) {
	v := r.URL.Query().Get(name)
	if v != "" && !openadr.ValidID(v) {
		return "", invalidParam(name, "is not an object ID")
	}
	return v, nil
}

// The limits of the pagination parameters.
const maxLimit = 50

type page struct{ skip, limit int }

func parsePage(r *http.Request) (page, error) {
	p := page{limit: maxLimit}
	q := r.URL.Query()
	if v := q.Get("skip"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 {
			return p, invalidParam("skip", "must be an integer of at least 0")
		}
		p.skip = int(n)
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 || n > maxLimit {
			return p, invalidParam("limit", "must be an integer from 0 to "+strconv.Itoa(maxLimit))
		}
		p.limit = int(n)
	}
	return p, nil
}

func paginate[T any](items []T, p page) []T {
	if p.skip >= len(items) {
		return []T{}
	}
	items = items[p.skip:]
	if len(items) > p.limit {
		items = items[:p.limit]
	}
	return items
}

// writeList answers a search with one page of items.
func writeList[T any](s *Server, w http.ResponseWriter, r *http.Request, c wire.ObjectCodec[T], items []T, p page) {
	body, err := c.MarshalList(paginate(items, p))
	s.write(w, r, http.StatusOK, body, err)
}

func writeObject[T any](s *Server, w http.ResponseWriter, r *http.Request, status int, c wire.ObjectCodec[T], v T) {
	body, err := c.Marshal(v, wire.Object)
	s.write(w, r, status, body, err)
}

// created returns the metadata of a new object.
func (s *Server) created() (openadr.Metadata, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return openadr.Metadata{}, err
	}
	now := s.now().UTC()
	return openadr.Metadata{ID: hex.EncodeToString(raw), Created: now, Modified: now}, nil
}

// modified returns the metadata of an object that is being replaced.
func (s *Server) modified(old openadr.Metadata) openadr.Metadata {
	old.Modified = s.now().UTC()
	return old
}
