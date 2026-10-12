// SPDX-License-Identifier: MIT

// Package server is an OpenADR 3.1.0 VTN as an http.Handler.
//
// The Server implements the REST API and its access rules. It leaves to the
// embedding program what a VTN deployment decides for itself: the listener
// and TLS, where tokens come from (auth.Validator) and where objects are
// kept (storage.Store).
//
// Programs, events, reports, VENs and resources are implemented.
// Subscriptions and the notifier endpoints answer 501 Not Implemented until
// notification delivery exists.
//
// Mount the handler at the root of the API. To serve it under a path prefix,
// wrap it in http.StripPrefix.
package server

import (
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
	// Validator resolves bearer tokens. Required.
	Validator auth.Validator
	// TokenURL is the OAuth2 token endpoint that GET /auth/server announces
	// to clients. Required, and an absolute http or https URL: the schema
	// says so, and clients of other implementations do not resolve a
	// relative reference. For a VTN that serves its own token endpoint (see
	// TokenHandler) it is the public URL of the API followed by /auth/token.
	TokenURL string
	// TokenHandler serves POST /auth/token, for a VTN that issues its own
	// tokens (see auth.Issuer). When nil the endpoint answers 501.
	TokenHandler http.Handler
	// Logger receives internal errors. Nil disables logging.
	Logger *slog.Logger
	// MaxBodyBytes limits the size of a request body; 1 MiB when zero.
	MaxBodyBytes int64
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// Server is a VTN. It is safe for concurrent use and holds no state besides
// its Store.
type Server struct {
	store     storage.Store
	validator auth.Validator
	codec     *wire.Codec
	logger    *slog.Logger
	maxBody   int64
	now       func() time.Time
	mux       *http.ServeMux
}

// New returns a Server for cfg.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("server: Config.Store is required")
	case cfg.Validator == nil:
		return nil, errors.New("server: Config.Validator is required")
	case cfg.TokenURL == "":
		return nil, errors.New("server: Config.TokenURL is required")
	}
	if u, err := url.Parse(cfg.TokenURL); err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("server: Config.TokenURL %q is not an absolute http or https URL", cfg.TokenURL)
	}
	s := &Server{
		store:     cfg.Store,
		validator: cfg.Validator,
		codec:     v310.Codec(),
		logger:    cfg.Logger,
		maxBody:   cfg.MaxBodyBytes,
		now:       cfg.Now,
		mux:       http.NewServeMux(),
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
	s.mux.ServeHTTP(w, r)
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

	handle("GET", "/auth/server", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// Not implemented yet. Authentication still applies.
	notImplemented := s.authorized("", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		s.problem(w, r, http.StatusNotImplemented, "subscriptions and notifiers are not implemented by this VTN")
	})
	for _, path := range []string{"/subscriptions", "/subscriptions/{id}", "/notifiers", "/notifiers/"} {
		s.mux.Handle(path, notImplemented)
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
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
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
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
