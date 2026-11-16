// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/guard"
	"github.com/otfabric/go-openadr/internal/wire/codecs"
)

// Handler receives the notifications of a VTN: it is what a subscription's
// callback URL points at.
//
// It answers the VTN's verification request (GET with an echo parameter),
// and for every notification (POST) checks the bearer token, decodes the
// body and calls Notify. The VTN is told 200 when Notify returns nil, and
// 500 otherwise, on which it sends the notification again later. A Notify
// that does not return normally is answered 500 too, and logged with its
// stack: through Logger when there is one, and otherwise where the
// http.Server that serves the Handler writes errors, its ErrorLog or the
// standard logger.
//
// Notify may be called concurrently, and with the same notification more
// than once: delivery is at least once. Notifications tell that something
// changed; a client that must not miss anything still polls from time to
// time.
type Handler struct {
	// BearerToken is the token given in the subscription
	// (ObjectOperation.BearerToken). Requests without it are refused. When
	// empty, anyone who can reach the handler can send it notifications.
	BearerToken string
	// Notify is called for each notification. Required.
	Notify func(ctx context.Context, n openadr.Notification) error
	// MaxBodyBytes limits the size of a notification; 1 MiB when zero.
	MaxBodyBytes int64
	// Version is the revision of OpenADR the VTN notifies in:
	// openadr.Version310 when empty, or openadr.Version301.
	Version openadr.Version
	// Logger receives the requests the Handler refuses, at warn level, and
	// the notifications it answers with 500, at error level; see
	// OBSERVABILITY.md. Bodies, tokens, the path and the query are never
	// logged, and what the request chose (its method, the reason a body is
	// not a notification) is cut to 256 bytes and quoted, as is an error of
	// Notify. A handler of the Logger that does not return normally costs
	// its entry and is reported through the Logger. Nil disables logging.
	Logger *slog.Logger
}

// The messages of the Handler's log entries.
const (
	msgRefused = "webhook: request refused"
	msgFailed  = "webhook: notification failed"
)

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// The verification request of the VTN. Answering it proves that
		// this endpoint expects notifications; it gives nothing away.
		echo := r.URL.Query().Get("echo")
		if echo == "" || len(echo) > 256 {
			h.refused(r, http.StatusBadRequest, "echo parameter missing or longer than 256 bytes", nil)
			http.Error(w, "an echo parameter is required", http.StatusBadRequest)
			return
		}
		// The echo goes back as it came, as plain text that no browser may
		// take for anything else.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, echo)
	case http.MethodPost:
		h.receive(w, r)
	default:
		h.refused(r, http.StatusMethodNotAllowed, "method not allowed", nil)
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// logger is the Logger of the Handler, or none.
func (h *Handler) logger() guard.Logger { return guard.NewLogger(h.Logger, "webhook") }

// refused logs a request that is answered with status, a 4xx. err, when
// not nil, may quote the body: it is cut and quoted.
func (h *Handler) refused(r *http.Request, status int, reason string, err error) {
	log := h.logger()
	if !log.Enabled(r.Context(), slog.LevelWarn) {
		return
	}
	attrs := []any{slog.Int("status", status), slog.String("method", quoted(r.Method)), slog.String("reason", reason)}
	if err != nil {
		attrs = append(attrs, slog.String("error", quoted(err.Error())))
	}
	log.Log(r.Context(), slog.LevelWarn, msgRefused, attrs...)
}

// failed logs a notification that is answered with 500. err, when not nil,
// is cut and quoted, as it may quote the notification.
func (h *Handler) failed(r *http.Request, reason string, err error) {
	log := h.logger()
	if !log.Enabled(r.Context(), slog.LevelError) {
		return
	}
	attrs := []any{slog.Int("status", http.StatusInternalServerError), slog.String("reason", reason)}
	if err != nil {
		attrs = append(attrs, slog.String("error", quoted(err.Error())))
	}
	log.Log(r.Context(), slog.LevelError, msgFailed, attrs...)
}

// maxLogged is the most bytes of a string that the request chose, or of an
// error that may quote one, that goes into a log entry.
const maxLogged = 256

// quoted is s for a log entry: cut to maxLogged bytes at a character
// boundary and quoted as a Go string, so that it holds no line break or
// control character; when it was cut, followed by how long it was.
func quoted(s string) string {
	if len(s) <= maxLogged {
		return strconv.Quote(s)
	}
	n := maxLogged
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strconv.Quote(s[:n]) + "... (" + strconv.Itoa(len(s)) + " bytes)"
}

func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	if h.BearerToken != "" {
		scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		want, got := sha256.Sum256([]byte(h.BearerToken)), sha256.Sum256([]byte(strings.TrimSpace(token)))
		if !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			h.refused(r, http.StatusUnauthorized, "bearer token missing or wrong", nil)
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
	}
	limit := h.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.refused(r, http.StatusRequestEntityTooLarge, "body longer than MaxBodyBytes", nil)
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		h.refused(r, http.StatusBadRequest, "body could not be read", err)
		http.Error(w, "the body could not be read", http.StatusBadRequest)
		return
	}
	codec, err := codecs.For(h.Version)
	if err != nil {
		h.failed(r, "Version not supported", err)
		http.Error(w, "webhook.Handler.Version: "+err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := codec.UnmarshalNotification(body)
	if err != nil {
		// Sending it again would not help: 4xx tells the VTN so.
		h.refused(r, http.StatusBadRequest, "not a notification", err)
		http.Error(w, "not a notification: "+err.Error(), http.StatusBadRequest)
		return
	}
	if h.Notify == nil {
		h.failed(r, "no Notify", nil)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if err := h.notify(r, n); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// notify calls Notify, and logs an error it returns. One that does not
// return normally is an error too: it is reported through the Logger, as
// the other components of the module report one, and without a Logger
// written where the HTTP server writes errors, as net/http writes a handler
// that does not return normally.
func (h *Handler) notify(r *http.Request, n openadr.Notification) (err error) {
	fail := guard.Try("Notify", func() { err = h.Notify(r.Context(), n) })
	if fail == nil {
		if err != nil {
			h.failed(r, "Notify returned an error", err)
		}
		return err
	}
	if fail.Value() == http.ErrAbortHandler {
		// How a handler asks net/http to abort the response: passed on.
		panic(http.ErrAbortHandler)
	}
	if log := h.logger(); log.Active() {
		log.Report(fail)
		return fail
	}
	entry := fmt.Sprintf("webhook: callback did not return normally: callback=Notify value=%v\n%s", fail.Value(), fail.Stack())
	if srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server); ok && srv.ErrorLog != nil {
		srv.ErrorLog.Print(entry)
	} else {
		log.Print(entry)
	}
	return fail
}
