// SPDX-License-Identifier: MIT

package client

import "time"

// Metrics receives a callback for every call a client makes to a VTN. A
// call is what a method of VEN or BL does, however many HTTP requests that
// takes: a token request before it and retries are part of the call.
//
// op names the operation as the specification does, by method and path,
// with {id} in the place of an ID: "GET /events", "PUT /programs/{id}",
// "GET /notifiers/mqtt/topics/vens/{id}/events". The set of values is
// small and fixed, so it is fit to be a label.
//
// The callbacks run on the goroutine of the call and must not block: count,
// observe, return. Implementations must be safe for concurrent use.
//
// Every call gives one OnRequest and then exactly one of the other three.
type Metrics interface {
	// OnRequest is called when a call begins.
	OnRequest(op string)
	// OnResponse is called when a call succeeded.
	OnResponse(op string, duration time.Duration)
	// OnError is called when a call failed for any reason but time. err is
	// what the method returns: test it with errors.Is and errors.As.
	OnError(op string, duration time.Duration, err error)
	// OnTimeout is called when a call ran out of time: the error matches
	// openadr.ErrTimeout.
	OnTimeout(op string, duration time.Duration)
}

// AttemptMetrics is an optional extension of Metrics: when the value given
// as Config.Metrics also implements it, it is told of every attempt of a
// call, which is where retries show.
type AttemptMetrics interface {
	// OnAttempt is called after each attempt. attempt counts from 1; err
	// is nil for the attempt that succeeded. A call that is not retried
	// has one attempt.
	OnAttempt(op string, attempt int, duration time.Duration, err error)
}
