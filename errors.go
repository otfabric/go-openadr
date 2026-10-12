// SPDX-License-Identifier: MIT

package openadr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Categories of failure reported by a VTN. An *Error matches the one for its
// HTTP status through errors.Is. The storage layer of the server returns
// ErrNotFound and ErrConflict directly.
var (
	ErrBadRequest   = errors.New("openadr: bad request")
	ErrUnauthorized = errors.New("openadr: unauthorized")
	ErrForbidden    = errors.New("openadr: forbidden")
	ErrNotFound     = errors.New("openadr: not found")
	ErrConflict     = errors.New("openadr: conflict")
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
	}
	return false
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
