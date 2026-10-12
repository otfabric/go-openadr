// SPDX-License-Identifier: MIT

package storage

import (
	"context"
	"fmt"
	"slices"
	"sync"

	openadr "github.com/otfabric/go-openadr"
)

// Memory is a Store that keeps everything in process memory.
type Memory struct {
	mu sync.RWMutex

	programs  table[openadr.Program]
	events    table[openadr.Event]
	reports   table[openadr.Report]
	vens      table[openadr.VEN]
	resources table[openadr.Resource]
}

// NewMemory returns an empty Memory store.
func NewMemory() *Memory {
	return &Memory{
		programs:  newTable[openadr.Program](),
		events:    newTable[openadr.Event](),
		reports:   newTable[openadr.Report](),
		vens:      newTable[openadr.VEN](),
		resources: newTable[openadr.Resource](),
	}
}

// table keeps objects by ID and remembers the order they were created in.
type table[T any] struct {
	order []string
	byID  map[string]T
}

func newTable[T any]() table[T] { return table[T]{byID: make(map[string]T)} }

// any reports whether some object other than the one with ID except
// satisfies f.
func (t *table[T]) any(except string, f func(T) bool) bool {
	for id, v := range t.byID {
		if id != except && f(v) {
			return true
		}
	}
	return false
}

func conflict(format string, args ...any) error {
	return &ConstraintError{Detail: fmt.Sprintf(format, args...)}
}

// collection implements Collection on one table. The hooks run with the
// store's lock held.
type collection[T, F any] struct {
	mu    *sync.RWMutex
	table *table[T]
	id    func(T) string
	// check validates a create, or an update of old; nil if there is nothing
	// to check.
	check func(v T, old *T) error
	// canDelete vetoes a delete; nil if there is nothing to check.
	canDelete func(v T) error
	match     func(v T, f F) bool
}

func (c collection[T, F]) Create(_ context.Context, v T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.id(v)
	if _, exists := c.table.byID[id]; exists {
		return conflict("ID %s is in use", id)
	}
	if c.check != nil {
		if err := c.check(v, nil); err != nil {
			return err
		}
	}
	c.table.byID[id] = v
	c.table.order = append(c.table.order, id)
	return nil
}

func (c collection[T, F]) Get(_ context.Context, id string) (T, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.table.byID[id]
	if !ok {
		return v, openadr.ErrNotFound
	}
	return v, nil
}

func (c collection[T, F]) Update(_ context.Context, v T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.id(v)
	old, ok := c.table.byID[id]
	if !ok {
		return openadr.ErrNotFound
	}
	if c.check != nil {
		if err := c.check(v, &old); err != nil {
			return err
		}
	}
	c.table.byID[id] = v
	return nil
}

func (c collection[T, F]) Delete(_ context.Context, id string) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.table.byID[id]
	if !ok {
		return v, openadr.ErrNotFound
	}
	if c.canDelete != nil {
		if err := c.canDelete(v); err != nil {
			return v, err
		}
	}
	delete(c.table.byID, id)
	c.table.order = slices.DeleteFunc(c.table.order, func(s string) bool { return s == id })
	return v, nil
}

func (c collection[T, F]) List(_ context.Context, f F) ([]T, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []T{}
	for _, id := range c.table.order {
		if v := c.table.byID[id]; c.match(v, f) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Programs implements Store.
func (m *Memory) Programs() Collection[openadr.Program, ProgramFilter] {
	return collection[openadr.Program, ProgramFilter]{
		mu: &m.mu, table: &m.programs,
		id: func(p openadr.Program) string { return p.ID },
		check: func(p openadr.Program, _ *openadr.Program) error {
			if m.programs.any(p.ID, func(o openadr.Program) bool { return o.Name == p.Name }) {
				return conflict("program name %q is in use", p.Name)
			}
			return nil
		},
		canDelete: func(p openadr.Program) error {
			if m.events.any("", func(e openadr.Event) bool { return e.ProgramID == p.ID }) {
				return conflict("program %s still has events", p.ID)
			}
			return nil
		},
		match: func(openadr.Program, ProgramFilter) bool { return true },
	}
}

// Events implements Store.
func (m *Memory) Events() Collection[openadr.Event, EventFilter] {
	return collection[openadr.Event, EventFilter]{
		mu: &m.mu, table: &m.events,
		id: func(e openadr.Event) string { return e.ID },
		check: func(e openadr.Event, _ *openadr.Event) error {
			if _, ok := m.programs.byID[e.ProgramID]; !ok {
				return conflict("program %s does not exist", e.ProgramID)
			}
			return nil
		},
		match: func(e openadr.Event, f EventFilter) bool {
			return f.ProgramID == "" || e.ProgramID == f.ProgramID
		},
	}
}

// Reports implements Store.
func (m *Memory) Reports() Collection[openadr.Report, ReportFilter] {
	return collection[openadr.Report, ReportFilter]{
		mu: &m.mu, table: &m.reports,
		id: func(r openadr.Report) string { return r.ID },
		check: func(r openadr.Report, old *openadr.Report) error {
			// A report may be updated after its event has been deleted.
			if old != nil && old.EventID == r.EventID {
				return nil
			}
			if _, ok := m.events.byID[r.EventID]; !ok {
				return conflict("event %s does not exist", r.EventID)
			}
			return nil
		},
		match: func(r openadr.Report, f ReportFilter) bool {
			if f.ProgramID != "" {
				// A report whose event is gone belongs to no program.
				if e, ok := m.events.byID[r.EventID]; !ok || e.ProgramID != f.ProgramID {
					return false
				}
			}
			return (f.EventID == "" || r.EventID == f.EventID) &&
				(f.ClientName == "" || r.ClientName == f.ClientName) &&
				(f.ClientID == "" || r.ClientID == f.ClientID)
		},
	}
}

// VENs implements Store.
func (m *Memory) VENs() Collection[openadr.VEN, VENFilter] {
	return collection[openadr.VEN, VENFilter]{
		mu: &m.mu, table: &m.vens,
		id: func(v openadr.VEN) string { return v.ID },
		check: func(v openadr.VEN, _ *openadr.VEN) error {
			if m.vens.any(v.ID, func(o openadr.VEN) bool { return o.Name == v.Name }) {
				return conflict("ven name %q is in use", v.Name)
			}
			return nil
		},
		canDelete: func(v openadr.VEN) error {
			if m.resources.any("", func(r openadr.Resource) bool { return r.VENID == v.ID }) {
				return conflict("ven %s still has resources", v.ID)
			}
			return nil
		},
		match: func(v openadr.VEN, f VENFilter) bool {
			return (f.Name == "" || v.Name == f.Name) && (f.ClientID == "" || v.ClientID == f.ClientID)
		},
	}
}

// Resources implements Store.
func (m *Memory) Resources() Collection[openadr.Resource, ResourceFilter] {
	return collection[openadr.Resource, ResourceFilter]{
		mu: &m.mu, table: &m.resources,
		id: func(r openadr.Resource) string { return r.ID },
		check: func(r openadr.Resource, _ *openadr.Resource) error {
			if _, ok := m.vens.byID[r.VENID]; !ok {
				return conflict("ven %s does not exist", r.VENID)
			}
			if m.resources.any(r.ID, func(o openadr.Resource) bool { return o.VENID == r.VENID && o.Name == r.Name }) {
				return conflict("resource name %q is in use for ven %s", r.Name, r.VENID)
			}
			return nil
		},
		match: func(r openadr.Resource, f ResourceFilter) bool {
			return (f.Name == "" || r.Name == f.Name) &&
				(f.VENID == "" || r.VENID == f.VENID) &&
				(f.ClientID == "" || r.ClientID == f.ClientID)
		},
	}
}
