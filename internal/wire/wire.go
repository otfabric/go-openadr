// SPDX-License-Identifier: MIT

// Package wire defines what a protocol revision has to provide to the client
// and server: the JSON form of each object. One sub-package per revision
// (v310 for OpenADR 3.1.0) builds a Codec.
//
// The wire structures stay internal so that the JSON shape of one revision
// never becomes part of the public API.
package wire

import (
	"encoding/json"
	"fmt"

	openadr "github.com/otfabric/go-openadr"
)

// Form says which representation of an object a message holds.
type Form uint8

const (
	// Object is the VTN's representation, with metadata.
	Object Form = iota
	// Request is a create or update request from a VEN client.
	Request
	// BLRequest is a create or update request from Business Logic. It
	// differs from Request for VENs and resources only.
	BLRequest
)

// ObjectCodec converts one kind of object to and from JSON.
type ObjectCodec[T any] struct {
	// Marshal encodes v in the given form. Request forms leave metadata out.
	Marshal func(v T, form Form) ([]byte, error)
	// Unmarshal decodes an object or a request and reports which it was.
	// A message that does not identify itself as a request is an Object.
	// Structural faults are reported as *openadr.ValidationError; the
	// decoded value is not otherwise validated.
	Unmarshal func(data []byte) (T, Form, error)
}

// MarshalList encodes a JSON array of objects.
func (c ObjectCodec[T]) MarshalList(vs []T) ([]byte, error) {
	items := make([]json.RawMessage, len(vs))
	for i, v := range vs {
		b, err := c.Marshal(v, Object)
		if err != nil {
			return nil, err
		}
		items[i] = b
	}
	return json.Marshal(items)
}

// UnmarshalList decodes a JSON array of objects.
func (c ObjectCodec[T]) UnmarshalList(data []byte) ([]T, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("openadr: decoding list: %w", err)
	}
	vs := make([]T, len(items))
	for i, item := range items {
		v, _, err := c.Unmarshal(item)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		vs[i] = v
	}
	return vs, nil
}

// Codec is the JSON representation of one protocol revision.
type Codec struct {
	Version openadr.Version

	Programs  ObjectCodec[openadr.Program]
	Events    ObjectCodec[openadr.Event]
	Reports   ObjectCodec[openadr.Report]
	VENs      ObjectCodec[openadr.VEN]
	Resources ObjectCodec[openadr.Resource]

	MarshalProblem   func(openadr.Problem) ([]byte, error)
	UnmarshalProblem func([]byte) (openadr.Problem, error)

	// The body of GET /auth/server.
	MarshalAuthServer   func(tokenURL string) ([]byte, error)
	UnmarshalAuthServer func([]byte) (tokenURL string, err error)
}
