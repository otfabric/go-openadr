// SPDX-License-Identifier: MIT

package storage

import (
	"encoding/json"
	"fmt"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// Object is what a Store keeps: the objects of the API, and a subscription
// with what the VTN remembers of its subscriber.
type Object interface {
	openadr.Program | openadr.Event | openadr.Report | openadr.VEN | openadr.Resource |
		openadr.ResourceGroup | SubscriptionRecord
}

// Encode returns an object as JSON, for a Store that keeps its objects as
// documents, in a column of a database say. The types of package openadr
// have no JSON form of their own; this is the one the API gives them
// (OpenADR 3.1.0, as the VTN shows an object to its owner), and Decode
// reads it back to an equal value.
//
// The form is stable: what one release of this module wrote, later ones
// read. It is the form storage.File writes to its journal.
func Encode[T Object](v T) ([]byte, error) {
	switch v := any(v).(type) {
	case openadr.Program:
		return fileCodec.Programs.Marshal(v, wire.Object)
	case openadr.Event:
		return fileCodec.Events.Marshal(v, wire.Object)
	case openadr.Report:
		return fileCodec.Reports.Marshal(v, wire.Object)
	case openadr.VEN:
		return fileCodec.VENs.Marshal(v, wire.Object)
	case openadr.Resource:
		return fileCodec.Resources.Marshal(v, wire.Object)
	case openadr.ResourceGroup:
		return fileCodec.ResourceGroups.Marshal(v, wire.Object)
	case SubscriptionRecord:
		sub, err := fileCodec.Subscriptions.Marshal(v.Subscription, wire.Object)
		if err != nil {
			return nil, err
		}
		return json.Marshal(encodedSubscription{Subscription: sub, ReadsAll: v.ReadsAll})
	}
	// Not reachable: the constraint lists the cases.
	return nil, fmt.Errorf("storage: cannot encode a %T", v)
}

// encodedSubscription is a SubscriptionRecord as Encode writes it.
type encodedSubscription struct {
	Subscription json.RawMessage `json:"subscription"`
	ReadsAll     bool            `json:"readsAll"`
}

// Decode reads what Encode wrote.
func Decode[T Object](data []byte) (T, error) {
	var (
		out T
		v   any
		err error
	)
	switch any(out).(type) {
	case openadr.Program:
		v, _, err = fileCodec.Programs.Unmarshal(data)
	case openadr.Event:
		v, _, err = fileCodec.Events.Unmarshal(data)
	case openadr.Report:
		v, _, err = fileCodec.Reports.Unmarshal(data)
	case openadr.VEN:
		v, _, err = fileCodec.VENs.Unmarshal(data)
	case openadr.Resource:
		v, _, err = fileCodec.Resources.Unmarshal(data)
	case openadr.ResourceGroup:
		v, _, err = fileCodec.ResourceGroups.Unmarshal(data)
	case SubscriptionRecord:
		var enc encodedSubscription
		if err = json.Unmarshal(data, &enc); err == nil {
			var rec SubscriptionRecord
			rec.Subscription, _, err = fileCodec.Subscriptions.Unmarshal(enc.Subscription)
			rec.ReadsAll = enc.ReadsAll
			v = rec
		}
	}
	if err != nil {
		return out, fmt.Errorf("storage: decoding a stored %T: %w", out, err)
	}
	return v.(T), nil
}
