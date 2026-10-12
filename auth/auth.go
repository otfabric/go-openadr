// SPDX-License-Identifier: MIT

// Package auth holds what the VTN needs to authenticate and authorize a
// request: the identity behind a bearer token and the OAuth2 scopes of
// OpenADR 3.
//
// A VTN is given a Validator and does not issue tokens itself. Issuer is a
// small in-memory OAuth2 client-credentials token service for tests,
// examples and self-contained deployments; a production VTN would normally
// put a Validator in front of its own identity provider instead.
package auth

import (
	"context"
	"errors"
	"slices"
)

// OAuth2 scopes defined by OpenADR 3.1.0.
const (
	// ScopeReadAll lets Business Logic read every object.
	ScopeReadAll = "read_all"
	// ScopeReadTargets lets a VEN read programs and events, those with
	// targets only when the VEN has been granted matching targets.
	ScopeReadTargets = "read_targets"
	// ScopeReadVENObjects lets a VEN read the objects whose clientID is its own.
	ScopeReadVENObjects     = "read_ven_objects"
	ScopeWritePrograms      = "write_programs"
	ScopeWriteEvents        = "write_events"
	ScopeWriteReports       = "write_reports"
	ScopeWriteSubscriptions = "write_subscriptions"
	ScopeWriteVENs          = "write_vens"
)

// BusinessLogicScopes returns the scopes the specification gives Business Logic.
func BusinessLogicScopes() []string {
	return []string{ScopeReadAll, ScopeWritePrograms, ScopeWriteEvents, ScopeWriteSubscriptions, ScopeWriteVENs}
}

// VENScopes returns the scopes the specification gives a VEN.
func VENScopes() []string {
	return []string{ScopeReadTargets, ScopeReadVENObjects, ScopeWriteReports, ScopeWriteSubscriptions, ScopeWriteVENs}
}

// ErrInvalidToken is returned by a Validator for a token that is unknown,
// malformed or expired. The VTN answers it with 401; any other error from a
// Validator is answered with 500.
var ErrInvalidToken = errors.New("auth: invalid token")

// Identity is the client a bearer token belongs to.
type Identity struct {
	// ClientID is the OAuth2 client identifier. The VTN writes it into the
	// objects a VEN creates and uses it to decide what a VEN may see.
	ClientID string
	Scopes   []string
}

// HasScope reports whether the identity was granted scope.
func (id Identity) HasScope(scope string) bool {
	return slices.Contains(id.Scopes, scope)
}

// IsBusinessLogic reports whether the identity may read every object, which
// is what distinguishes Business Logic from a VEN.
func (id Identity) IsBusinessLogic() bool {
	return id.HasScope(ScopeReadAll)
}

// Allows reports whether the identity may perform an operation that requires
// scope. ScopeReadAll includes the narrower read scopes.
func (id Identity) Allows(scope string) bool {
	if id.HasScope(scope) {
		return true
	}
	switch scope {
	case ScopeReadTargets, ScopeReadVENObjects:
		return id.HasScope(ScopeReadAll)
	}
	return false
}

// Validator resolves a bearer token to the identity it was issued to.
// Implementations must be safe for concurrent use.
type Validator interface {
	Validate(ctx context.Context, token string) (Identity, error)
}

// ValidatorFunc adapts a function to a Validator.
type ValidatorFunc func(ctx context.Context, token string) (Identity, error)

// Validate calls f.
func (f ValidatorFunc) Validate(ctx context.Context, token string) (Identity, error) {
	return f(ctx, token)
}
