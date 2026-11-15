// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"fmt"

	"github.com/otfabric/go-openadr/server/storage"
)

// Limits bounds what one client keeps in the VTN. OpenADR 3.1.0 sets no
// such limits; a VTN that serves clients it does not fully trust needs
// them, because every write is checked against every subscription, and a
// subscription's callback URLs are each called to be verified.
//
// A create that would pass a limit is answered 409 with a problem that
// says which: the client can delete some of what it keeps and try again.
// A negative limit is no limit.
type Limits struct {
	// Subscriptions is the most subscriptions one client keeps; 100 when
	// zero.
	Subscriptions int
	// CallbackURLs is the most distinct callback URLs one subscription
	// names; 16 when zero. They are verified, when the subscription is
	// created or an update adds them, a few at a time.
	CallbackURLs int
	// VENs, Resources and Reports are the most of each that one client
	// keeps; no limit when zero. A VEN that reports for as long as it runs
	// makes ever more reports, so a limit on them needs the application to
	// delete old ones.
	VENs, Resources, Reports int
	// Deliveries is the most webhook notifications the subscriptions of
	// one client have recorded in the Outbox and not yet delivered, and
	// DeliveryBytes the most bytes they take (bodies, callback URLs and
	// tokens); 10000 and 64 MiB when zero. They apply with Config.Outbox
	// only. A notification past either is not recorded and not sent, and
	// a warning is logged: what one client's subscriptions cost the store
	// stays bounded while their webhooks do not take what they are sent.
	Deliveries, DeliveryBytes int
}

// Defaults of Limits.
const (
	defaultMaxSubscriptions = 100
	defaultMaxCallbackURLs  = 16
	defaultMaxDeliveries    = 10000
	defaultMaxDeliveryBytes = 64 << 20
)

// limits are the Limits of a Server, each zero when there is none.
type limits struct {
	subscriptions, callbackURLs, vens, resources, reports int
	deliveries, deliveryBytes                             int
}

func newLimits(l Limits) limits {
	pick := func(v, zero int) int {
		switch {
		case v < 0:
			return 0
		case v == 0:
			return zero
		}
		return v
	}
	return limits{
		subscriptions: pick(l.Subscriptions, defaultMaxSubscriptions),
		callbackURLs:  pick(l.CallbackURLs, defaultMaxCallbackURLs),
		vens:          pick(l.VENs, 0),
		resources:     pick(l.Resources, 0),
		reports:       pick(l.Reports, 0),
		deliveries:    pick(l.Deliveries, defaultMaxDeliveries),
		deliveryBytes: pick(l.DeliveryBytes, defaultMaxDeliveryBytes),
	}
}

// quota is a limit on the objects of one kind that a client keeps.
type quota struct {
	// max is the limit, 0 for none.
	max int
	// noun names the objects in a problem: "subscriptions".
	noun string
	// count returns how many the client with the given ID keeps.
	count func(ctx context.Context, clientID string) (int, error)
}

// admit reports whether the client with the given ID may keep one more.
// The caller holds Server.creating, so that two creates do not both take
// the last place.
func (q quota) admit(ctx context.Context, clientID string) error {
	if q.max <= 0 || clientID == "" {
		return nil
	}
	n, err := q.count(ctx, clientID)
	if err != nil {
		return err
	}
	if n >= q.max {
		return &storage.ConstraintError{Detail: fmt.Sprintf("the client keeps %d %s, the most this VTN keeps for one client", n, q.noun)}
	}
	return nil
}

// counter counts the objects of a collection that a filter selects.
func counter[T, F any](coll storage.Collection[T, F], filter func(clientID string) F) func(context.Context, string) (int, error) {
	return func(ctx context.Context, clientID string) (int, error) {
		all, err := coll.List(ctx, filter(clientID))
		return len(all), err
	}
}
