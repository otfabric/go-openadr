// SPDX-License-Identifier: MIT

package openadr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Categories of failure reported by a VTN. An *Error matches the one for its
// HTTP status through errors.Is. The storage layer of the server returns
// ErrNotFound and ErrConflict directly.
//
// Test for these with errors.Is, and for the types with errors.As; the
// text of an error is for people and may change.
var (
	ErrBadRequest   = errors.New("openadr: bad request")
	ErrUnauthorized = errors.New("openadr: unauthorized")
	ErrForbidden    = errors.New("openadr: forbidden")
	ErrNotFound     = errors.New("openadr: not found")
	ErrConflict     = errors.New("openadr: conflict")

	// ErrTooManyRequests is a 429: the VTN, or a gateway before it, wants
	// the client to slow down. Error.RetryAfter says for how long, when
	// the answer did.
	ErrTooManyRequests = errors.New("openadr: too many requests")
	// ErrServer is any 5xx: the VTN failed, and the request may or may
	// not have been carried out. ErrNotImplemented and ErrUnavailable are
	// two of them, and an error that matches one of those matches this too.
	ErrServer = errors.New("openadr: server error")
	// ErrNotImplemented is a 501: the VTN does not have the operation,
	// such as subscriptions on a VTN that cannot notify.
	ErrNotImplemented = errors.New("openadr: not implemented")
	// ErrUnavailable is a 502, 503 or 504: the VTN cannot be reached
	// through its gateway, or cannot answer now. The request was not
	// carried out when the status is 503.
	ErrUnavailable = errors.New("openadr: unavailable")
)

// Categories of failure that are not an answer of a VTN.
var (
	// ErrInvalid is matched by every *ValidationError: an object, an ID or
	// a message does not satisfy the schema.
	ErrInvalid = errors.New("openadr: invalid")
	// ErrConfiguration is matched by what a constructor of this module
	// returns for a configuration it cannot work with.
	ErrConfiguration = errors.New("openadr: invalid configuration")
	// ErrTimeout is matched by a call that ran out of time: the deadline
	// of its context, the timeout of the HTTP client, or a network
	// timeout below them. Such an error still matches
	// context.DeadlineExceeded where that is what happened.
	ErrTimeout = errors.New("openadr: timeout")
	// ErrProtocol is matched by an answer that is not what the protocol
	// says it is: a body that cannot be decoded, one that is too large, a
	// token endpoint that issues no token. The peer acted on the request.
	ErrProtocol = errors.New("openadr: protocol error")
	// ErrClosed is matched by the use of something after it was closed.
	ErrClosed = errors.New("openadr: closed")
)

// Problem is the error body a VTN returns with a 4xx or 5xx response.
type Problem struct {
	Type     string
	Title    string
	Status   int
	Detail   string
	Instance string
}

// Error is a non-success response from a VTN. Problem is zero when the
// response body was not a problem object.
type Error struct {
	// Method and Path identify the request.
	Method string
	Path   string
	// StatusCode is the HTTP status of the response.
	StatusCode int
	Problem    Problem
	// RetryAfter is how long the VTN asked the client to wait before it
	// tries again, in a Retry-After header; zero when it did not say.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "openadr: %s %s: %d", e.Method, e.Path, e.StatusCode)
	title := e.Problem.Title
	if title == "" {
		title = http.StatusText(e.StatusCode)
	}
	if title != "" {
		b.WriteString(" " + title)
	}
	if e.Problem.Detail != "" {
		b.WriteString(": " + e.Problem.Detail)
	}
	return b.String()
}

// Is matches the category sentinel for the response's status code.
func (e *Error) Is(target error) bool {
	switch e.StatusCode {
	case http.StatusBadRequest:
		return target == ErrBadRequest
	case http.StatusUnauthorized:
		return target == ErrUnauthorized
	case http.StatusForbidden:
		return target == ErrForbidden
	case http.StatusNotFound:
		return target == ErrNotFound
	case http.StatusConflict:
		return target == ErrConflict
	case http.StatusTooManyRequests:
		return target == ErrTooManyRequests
	case http.StatusNotImplemented:
		return target == ErrNotImplemented || target == ErrServer
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return target == ErrUnavailable || target == ErrServer
	}
	return e.StatusCode >= 500 && target == ErrServer
}

// ValidationError reports a property that violates the OpenADR schema. It is
// returned by the Validate methods, by the clients before a request is sent,
// and by the wire codecs for malformed messages.
type ValidationError struct {
	// Field is the path of the property in the JSON naming of the
	// specification, such as "intervals[0].payloads[1].type".
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return "openadr: invalid " + e.Field + ": " + e.Reason
}

// Is matches ErrInvalid.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }
