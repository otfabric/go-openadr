// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"

	openadr "github.com/otfabric/go-openadr"
	v310 "github.com/otfabric/go-openadr/internal/wire/v310"
)

// Handler receives the notifications of a VTN: it is what a subscription's
// callback URL points at.
//
// It answers the VTN's verification request (GET with an echo parameter),
// and for every notification (POST) checks the bearer token, decodes the
// body and calls Notify. The VTN is told 200 when Notify returns nil, and
// 500 otherwise, on which it sends the notification again later.
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
}

var codec = v310.Codec()

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// The verification request of the VTN. Answering it proves that
		// this endpoint expects notifications; it gives nothing away.
		echo := r.URL.Query().Get("echo")
		if echo == "" || len(echo) > 256 {
			http.Error(w, "an echo parameter is required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, echo)
	case http.MethodPost:
		h.receive(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	if h.BearerToken != "" {
		scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		want, got := sha256.Sum256([]byte(h.BearerToken)), sha256.Sum256([]byte(strings.TrimSpace(token)))
		if !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "the body could not be read", http.StatusBadRequest)
		return
	}
	n, err := codec.UnmarshalNotification(body)
	if err != nil {
		// Sending it again would not help: 4xx tells the VTN so.
		http.Error(w, "not a notification: "+err.Error(), http.StatusBadRequest)
		return
	}
	if h.Notify == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if err := h.Notify(r.Context(), n); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
