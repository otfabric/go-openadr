// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"slices"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server/storage"
)

// owns reports whether the client may read and write an object that carries
// clientID: Business Logic always, a VEN only its own.
func owns(id auth.Identity, clientID string) bool {
	return id.IsBusinessLogic() || id.ClientID == clientID
}

// ownerFilter is the clientID a search is restricted to: none for Business
// Logic, its own for a VEN.
func ownerFilter(id auth.Identity) string {
	if id.IsBusinessLogic() {
		return ""
	}
	return id.ClientID
}

func intersect(a, b []string) []string {
	var out []string
	for _, s := range a {
		if slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func overlaps(a, b []string) bool {
	return slices.ContainsFunc(a, func(s string) bool { return slices.Contains(b, s) })
}

// queryTargets returns the targets parameter of a search.
func queryTargets(values []string) ([]string, error) {
	for _, t := range values {
		if t == "" || len(t) > 128 {
			return nil, invalidParam("targets", "holds a target that is empty or longer than 128 characters")
		}
	}
	return values, nil
}

// grantedTargets returns the targets Business Logic has granted a client:
// those of the ven objects with its clientID and of their resources.
func (s *Server) grantedTargets(ctx context.Context, clientID string) ([]string, error) {
	if clientID == "" {
		// A client that did not authenticate. (An empty filter would
		// select every ven.)
		return nil, nil
	}
	vens, err := s.store.VENs().List(ctx, storage.VENFilter{ClientID: clientID})
	if err != nil {
		return nil, err
	}
	var granted []string
	for _, v := range vens {
		granted = append(granted, v.Targets...)
		resources, err := s.store.Resources().List(ctx, storage.ResourceFilter{VENID: v.ID})
		if err != nil {
			return nil, err
		}
		for _, res := range resources {
			granted = append(granted, res.Targets...)
		}
	}
	return granted, nil
}

// targetView decides which programs and events a client sees, and which of
// their targets (Definition, "Object Privacy").
//
// An object matches a set of targets when it has at least one of them.
type targetView struct {
	// all is set for Business Logic, which sees everything.
	all bool
	// filtered is set when the search named targets: only objects that
	// match are wanted.
	filtered bool
	// match is what an object's targets are compared with: the targets of
	// the search for Business Logic, and for a VEN those of them it has
	// been granted.
	match []string
}

// viewSearch is the view for a search that may carry a targets parameter.
// A VEN that names no targets sees only objects without targets.
func (s *Server) viewSearch(ctx context.Context, id auth.Identity, query []string) (targetView, error) {
	v := targetView{all: id.IsBusinessLogic(), filtered: len(query) > 0, match: query}
	if v.all || !v.filtered {
		return v, nil
	}
	granted, err := s.grantedTargets(ctx, id.ClientID)
	if err != nil {
		return v, err
	}
	v.match = intersect(query, granted)
	return v, nil
}

// viewObject is the view for a read by ID, which has no targets parameter:
// a VEN sees the object if any target it was granted matches.
func (s *Server) viewObject(ctx context.Context, id auth.Identity) (targetView, error) {
	v := targetView{all: id.IsBusinessLogic()}
	if v.all {
		return v, nil
	}
	granted, err := s.grantedTargets(ctx, id.ClientID)
	v.match = granted
	return v, err
}

// show reports whether an object with the given targets is visible and
// returns the targets to present. A VEN is shown only the targets it
// matched on, so that it cannot learn targets it was not granted.
func (v targetView) show(targets []string) ([]string, bool) {
	matched := overlaps(targets, v.match)
	switch {
	case v.filtered && !matched:
		return nil, false
	case v.all:
		return targets, true
	case len(targets) == 0:
		return nil, true
	case !matched:
		return nil, false
	}
	return intersect(targets, v.match), true
}

// ended reports whether an event has transpired. It can only tell for an
// event that has a start, a finite length, and no interval with a period
// of its own; every other event counts as still active.
func ended(e openadr.Event, now time.Time) bool {
	p := e.IntervalPeriod
	// A start in year 1 means "now".
	if p == nil || p.Start == nil || p.Start.Year() <= 1 {
		return false
	}
	for _, iv := range e.Intervals {
		if iv.IntervalPeriod != nil {
			return false
		}
	}
	end := *p.Start
	switch {
	case e.Duration != nil:
		if e.Duration.IsInfinite() {
			return false
		}
		end = e.Duration.AddTo(end)
	case p.Duration != nil:
		if p.Duration.IsInfinite() {
			return false
		}
		for range max(len(e.Intervals), 1) {
			end = p.Duration.AddTo(end)
		}
	default:
		return false
	}
	return !end.After(now)
}
