// SPDX-License-Identifier: MIT

// Package objectjson gives the objects of package openadr the JSON of a
// revision of OpenADR: the representation a VTN of that revision answers
// with. It is for a program that shows objects or reads them from files,
// such as a command-line tool, and has to write them as the VTN it talks
// to would.
//
// What a revision cannot carry is refused, as by the clients: string
// targets in 3.0.1, typed targets in 3.1.0. Resource groups are not of
// either revision (see openadr.ResourceGroup) and are encoded as this
// module's VTN gives them, with 3.1.0 only.
package objectjson

import (
	"fmt"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
	"github.com/otfabric/go-openadr/internal/wire/codecs"
)

// Object is what Marshal and Unmarshal take.
type Object interface {
	openadr.Program | openadr.Event | openadr.Report | openadr.VEN | openadr.Resource |
		openadr.Subscription | openadr.ResourceGroup | openadr.Notification
}

// Marshal returns obj as revision v gives it; the zero Version is 3.1.0.
// An unknown revision matches openadr.ErrConfiguration, and what v cannot
// carry is a *openadr.ValidationError, or errors.ErrUnsupported for a
// resource group in 3.0.1.
func Marshal[T Object](v openadr.Version, obj T) ([]byte, error) {
	c, err := codec(v)
	if err != nil {
		return nil, err
	}
	switch o := any(obj).(type) {
	case openadr.Program:
		return c.Programs.Marshal(o, wire.Object)
	case openadr.Event:
		return c.Events.Marshal(o, wire.Object)
	case openadr.Report:
		return c.Reports.Marshal(o, wire.Object)
	case openadr.VEN:
		return c.VENs.Marshal(o, wire.Object)
	case openadr.Resource:
		return c.Resources.Marshal(o, wire.Object)
	case openadr.Subscription:
		return c.Subscriptions.Marshal(o, wire.Object)
	case openadr.ResourceGroup:
		return c.ResourceGroups.Marshal(o, wire.Object)
	case openadr.Notification:
		return c.MarshalNotification(o)
	}
	// Not reachable: the constraint lists the cases.
	return nil, fmt.Errorf("objectjson: cannot encode a %T", obj)
}

// Unmarshal reads an object, or a create or update request for one, as
// revision v writes it. The result is not validated: call its Validate.
func Unmarshal[T Object](v openadr.Version, data []byte) (T, error) {
	var out T
	c, err := codec(v)
	if err != nil {
		return out, err
	}
	var obj any
	switch any(out).(type) {
	case openadr.Program:
		obj, _, err = c.Programs.Unmarshal(data)
	case openadr.Event:
		obj, _, err = c.Events.Unmarshal(data)
	case openadr.Report:
		obj, _, err = c.Reports.Unmarshal(data)
	case openadr.VEN:
		obj, _, err = c.VENs.Unmarshal(data)
	case openadr.Resource:
		obj, _, err = c.Resources.Unmarshal(data)
	case openadr.Subscription:
		obj, _, err = c.Subscriptions.Unmarshal(data)
	case openadr.ResourceGroup:
		obj, _, err = c.ResourceGroups.Unmarshal(data)
	case openadr.Notification:
		obj, err = c.UnmarshalNotification(data)
	}
	if err != nil {
		return out, err
	}
	return obj.(T), nil
}

func codec(v openadr.Version) (*wire.Codec, error) {
	c, err := codecs.For(v)
	if err != nil {
		return nil, fmt.Errorf("objectjson: %w: %w", openadr.ErrConfiguration, err)
	}
	return c, nil
}
