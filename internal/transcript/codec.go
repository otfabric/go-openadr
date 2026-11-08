// SPDX-License-Identifier: MIT

package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// Shape kinds: what a body holds, by the codec that reads it.
const (
	ShapeProgram       = "program"
	ShapeEvent         = "event"
	ShapeReport        = "report"
	ShapeVEN           = "ven"
	ShapeResource      = "resource"
	ShapeSubscription  = "subscription"
	ShapeResourceGroup = "resource-group"
	ShapeNotification  = "notification"
	ShapeNotifiers     = "notifiers"
	ShapeTopics        = "topics"
	ShapeProblem       = "problem"
	ShapeAuthServer    = "auth-server"
)

// Shape is what a body is: the kind of object, and whether it is a list.
type Shape struct {
	Kind string
	List bool
	// Request: the body is a create or update request.
	Request bool
}

func (s Shape) String() string {
	if s.List {
		return "[]" + s.Kind
	}
	return s.Kind
}

var shapeOfKind = map[string]string{
	"programs":        ShapeProgram,
	"events":          ShapeEvent,
	"reports":         ShapeReport,
	"vens":            ShapeVEN,
	"resources":       ShapeResource,
	"vens/resources":  ShapeResource,
	"subscriptions":   ShapeSubscription,
	"resource_groups": ShapeResourceGroup,
}

// RequestShape returns the shape of a request body, false for a body no
// codec reads (a token request is a form).
func RequestShape(s Step) (Shape, bool) {
	if s.Transport == "webhook" {
		return Shape{Kind: ShapeNotification}, true
	}
	if s.Request == nil {
		return Shape{}, false
	}
	k, ok := shapeOfKind[Kind(s.Request.Route)]
	if !ok || (s.Request.Method != http.MethodPost && s.Request.Method != http.MethodPut) {
		return Shape{}, false
	}
	return Shape{Kind: k, Request: true}, true
}

// ResponseShape returns the shape of an answer, false for one no codec
// reads (the token endpoint's, or one that is not JSON).
func ResponseShape(s Step) (Shape, bool) {
	if s.Response == nil || s.Request == nil || s.Response.Format != FormatJSON || s.Transport == "webhook" {
		return Shape{}, false
	}
	route := s.Request.Route
	if s.Response.Status/100 != 2 {
		if route == "/auth/token" {
			return Shape{}, false
		}
		if strings.Contains(s.Response.Headers["Content-Type"], "problem+json") || looksLikeProblem(s.Response.JSON) {
			return Shape{Kind: ShapeProblem}, true
		}
		return Shape{}, false
	}
	switch {
	case route == "/auth/server":
		return Shape{Kind: ShapeAuthServer}, true
	case route == "/notifiers":
		return Shape{Kind: ShapeNotifiers}, true
	case strings.HasPrefix(route, "/notifiers/mqtt/topics/"):
		return Shape{Kind: ShapeTopics}, true
	}
	k, ok := shapeOfKind[Kind(route)]
	if !ok {
		return Shape{}, false
	}
	// A collection answers GET with a list; an object route, and a create,
	// with one object.
	parts := strings.Split(strings.Trim(route, "/"), "/")
	list := s.Request.Method == http.MethodGet && (len(parts) == 1 || (len(parts) == 3 && parts[2] == "resources"))
	return Shape{Kind: k, List: list}, true
}

func looksLikeProblem(raw json.RawMessage) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	_, title := p["title"]
	_, status := p["status"]
	_, typ := p["type"]
	return title || status || typ
}

// ErrNoCodec is returned for a shape no codec reads.
var ErrNoCodec = errors.New("transcript: no codec for this shape")

// Reencode decodes a body with the codec of a revision and encodes what
// was decoded again, in the form it was decoded from. The difference
// between the two is what the codec does not carry.
//
// A request body without the objectType that names its form (the request
// schemas of programs, events, reports and subscriptions have none) is
// encoded again as a request when s.Request is set.
func Reencode(c *wire.Codec, s Shape, body []byte) ([]byte, error) {
	if s.List {
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("decoding list: %w", err)
		}
		out := make([]json.RawMessage, len(items))
		for i, item := range items {
			b, err := Reencode(c, Shape{Kind: s.Kind, Request: s.Request}, item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			out[i] = b
		}
		return json.Marshal(out)
	}
	switch s.Kind {
	case ShapeProgram:
		return reencode(c.Programs, body, s.Request, wire.Request)
	case ShapeEvent:
		return reencode(c.Events, body, s.Request, wire.Request)
	case ShapeReport:
		return reencode(c.Reports, body, s.Request, wire.Request)
	case ShapeVEN:
		return reencode(c.VENs, body, s.Request, wire.BLRequest)
	case ShapeResource:
		return reencode(c.Resources, body, s.Request, wire.BLRequest)
	case ShapeSubscription:
		return reencode(c.Subscriptions, body, s.Request, wire.Request)
	case ShapeResourceGroup:
		return reencode(c.ResourceGroups, body, s.Request, wire.Request)
	case ShapeNotification:
		n, err := c.UnmarshalNotification(body)
		if err != nil {
			return nil, err
		}
		return c.MarshalNotification(n)
	case ShapeNotifiers:
		return via(c.UnmarshalNotifiers, c.MarshalNotifiers, body)
	case ShapeTopics:
		return via(c.UnmarshalTopics, c.MarshalTopics, body)
	case ShapeProblem:
		return via(c.UnmarshalProblem, c.MarshalProblem, body)
	case ShapeAuthServer:
		return via(c.UnmarshalAuthServer, c.MarshalAuthServer, body)
	}
	return nil, ErrNoCodec
}

// reencode: a request that does not name its form is written in the form
// asForm, the superset of a ven or resource's two request forms.
func reencode[T any](oc wire.ObjectCodec[T], body []byte, request bool, asForm wire.Form) ([]byte, error) {
	if oc.Unmarshal == nil {
		return nil, ErrNoCodec
	}
	v, form, err := oc.Unmarshal(body)
	if err != nil {
		return nil, err
	}
	if request && form == wire.Object {
		form = asForm
	}
	return oc.Marshal(v, form)
}

func via[T any](dec func([]byte) (T, error), enc func(T) ([]byte, error), body []byte) ([]byte, error) {
	if dec == nil || enc == nil {
		return nil, ErrNoCodec
	}
	v, err := dec(body)
	if err != nil {
		return nil, err
	}
	return enc(v)
}

// Revision is the openadr.Version of a transcript.
func (f File) Revision() openadr.Version {
	if f.ProtocolVersion == "3.0.1" {
		return openadr.Version301
	}
	return openadr.Version310
}
