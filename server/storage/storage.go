// SPDX-License-Identifier: MIT

// Package storage is the persistence boundary of the VTN. The server keeps
// no state of its own: everything goes through a Store.
//
// Memory is the in-process implementation, for tests and for deployments
// that do not need to survive a restart.
package storage

import (
	"context"

	openadr "github.com/otfabric/go-openadr"
)

// Store gives access to the collections of a VTN. Implementations must be
// safe for concurrent use.
//
// A Store enforces what has to be decided atomically with a write:
//
//   - program names and VEN names are unique, and so is a resource name
//     within its VEN;
//   - an event refers to an existing program, a report to an existing event,
//     a resource to an existing VEN, and a subscription that names a program
//     to an existing one;
//   - a program that has events and a VEN that has resources cannot be
//     deleted.
//
// A violation is reported as a *ConstraintError. Reports outlive the event
// they refer to.
type Store interface {
	Programs() Collection[openadr.Program, ProgramFilter]
	Events() Collection[openadr.Event, EventFilter]
	Reports() Collection[openadr.Report, ReportFilter]
	VENs() Collection[openadr.VEN, VENFilter]
	Resources() Collection[openadr.Resource, ResourceFilter]
	Subscriptions() Collection[SubscriptionRecord, SubscriptionFilter]
}

// SubscriptionRecord is a subscription as the VTN keeps it: the object of
// the API, and what the VTN has to remember about the subscriber to decide
// later, without a request and its token at hand, what to notify it of.
type SubscriptionRecord struct {
	openadr.Subscription
	// ReadsAll says the subscriber could read every object when it
	// subscribed: it is Business Logic. A VEN is notified of what it could
	// read itself.
	ReadsAll bool
}

// SubscriptionFilter selects subscriptions; empty fields match everything.
type SubscriptionFilter struct {
	ProgramID  string
	ClientName string
	ClientID   string
}

// Collection stores objects of one type under their Metadata.ID, which the
// caller assigns.
//
// Values are shared between the caller and the store: neither side modifies
// a value, or anything it points to, after handing it over.
type Collection[T, F any] interface {
	// Create adds v. It returns a *ConstraintError if the ID is in use.
	Create(ctx context.Context, v T) error
	// Get returns the object with the given ID, or openadr.ErrNotFound.
	Get(ctx context.Context, id string) (T, error)
	// Update replaces the object that has v's ID, or returns
	// openadr.ErrNotFound.
	Update(ctx context.Context, v T) error
	// Delete removes and returns the object, or returns openadr.ErrNotFound.
	Delete(ctx context.Context, id string) (T, error)
	// List returns the objects that match every set field of filter, in the
	// order they were created.
	List(ctx context.Context, filter F) ([]T, error)
}

// ProgramFilter selects programs. It has no fields yet: target matching is
// done by the server, which also has to decide visibility per client.
type ProgramFilter struct{}

// EventFilter selects events; empty fields match everything.
type EventFilter struct {
	ProgramID string
}

// ReportFilter selects reports; empty fields match everything.
type ReportFilter struct {
	// ProgramID matches reports whose event belongs to the program.
	ProgramID  string
	EventID    string
	ClientName string
	ClientID   string
}

// VENFilter selects VENs; empty fields match everything.
type VENFilter struct {
	Name     string
	ClientID string
}

// ResourceFilter selects resources; empty fields match everything.
type ResourceFilter struct {
	Name     string
	VENID    string
	ClientID string
}

// ConstraintError reports a write that would break one of the rules listed
// on Store. It matches openadr.ErrConflict.
type ConstraintError struct {
	// Detail says which rule, in words fit for an API response.
	Detail string
}

func (e *ConstraintError) Error() string { return "openadr: conflict: " + e.Detail }

// Is matches openadr.ErrConflict.
func (e *ConstraintError) Is(target error) bool { return target == openadr.ErrConflict }
