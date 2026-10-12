// SPDX-License-Identifier: MIT

package client

import (
	"context"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// BL is the client of Business Logic: the utility or aggregator side that
// defines programs, issues events, reads the reports VENs send, and
// provisions VENs and their targets.
type BL struct {
	core *core
}

// NewBL returns a Business Logic client. It does not contact the VTN.
func NewBL(cfg Config) (*BL, error) {
	c, err := newCore(cfg)
	if err != nil {
		return nil, err
	}
	return &BL{core: c}, nil
}

// Programs searches the programs of the VTN.
func (c *BL) Programs(ctx context.Context, q ProgramQuery) ([]openadr.Program, error) {
	return listObjects(ctx, c.core, c.core.codec.Programs, "programs", q.values())
}

// Program returns the program with the given ID.
func (c *BL) Program(ctx context.Context, id string) (openadr.Program, error) {
	return getObject(ctx, c.core, c.core.codec.Programs, "programs", id)
}

// CreateProgram creates a program and returns it as the VTN stored it.
func (c *BL) CreateProgram(ctx context.Context, p openadr.Program) (openadr.Program, error) {
	return sendObject(ctx, c.core, c.core.codec.Programs, "programs", "", p, wire.BLRequest)
}

// UpdateProgram replaces the program that has p.ID with p.
func (c *BL) UpdateProgram(ctx context.Context, p openadr.Program) (openadr.Program, error) {
	if p.ID == "" {
		return openadr.Program{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Programs, "programs", p.ID, p, wire.BLRequest)
}

// DeleteProgram deletes a program and returns it as it last was.
func (c *BL) DeleteProgram(ctx context.Context, id string) (openadr.Program, error) {
	return deleteObject(ctx, c.core, c.core.codec.Programs, "programs", id)
}

// Events searches the events of the VTN.
func (c *BL) Events(ctx context.Context, q EventQuery) ([]openadr.Event, error) {
	return listObjects(ctx, c.core, c.core.codec.Events, "events", q.values())
}

// Event returns the event with the given ID.
func (c *BL) Event(ctx context.Context, id string) (openadr.Event, error) {
	return getObject(ctx, c.core, c.core.codec.Events, "events", id)
}

// CreateEvent creates a event and returns it as the VTN stored it.
func (c *BL) CreateEvent(ctx context.Context, e openadr.Event) (openadr.Event, error) {
	return sendObject(ctx, c.core, c.core.codec.Events, "events", "", e, wire.BLRequest)
}

// UpdateEvent replaces the event that has e.ID with e.
func (c *BL) UpdateEvent(ctx context.Context, e openadr.Event) (openadr.Event, error) {
	if e.ID == "" {
		return openadr.Event{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Events, "events", e.ID, e, wire.BLRequest)
}

// DeleteEvent deletes a event and returns it as it last was.
func (c *BL) DeleteEvent(ctx context.Context, id string) (openadr.Event, error) {
	return deleteObject(ctx, c.core, c.core.codec.Events, "events", id)
}

// Reports searches the reports of all VENs.
func (c *BL) Reports(ctx context.Context, q ReportQuery) ([]openadr.Report, error) {
	return listObjects(ctx, c.core, c.core.codec.Reports, "reports", q.values())
}

// Report returns the report with the given ID.
func (c *BL) Report(ctx context.Context, id string) (openadr.Report, error) {
	return getObject(ctx, c.core, c.core.codec.Reports, "reports", id)
}

// VENs searches the VENs of the VTN.
func (c *BL) VENs(ctx context.Context, q VENQuery) ([]openadr.VEN, error) {
	return listObjects(ctx, c.core, c.core.codec.VENs, "vens", q.values())
}

// VEN returns the VEN with the given ID.
func (c *BL) VEN(ctx context.Context, id string) (openadr.VEN, error) {
	return getObject(ctx, c.core, c.core.codec.VENs, "vens", id)
}

// CreateVEN creates a VEN and returns it as the VTN stored it. ClientID is
// required: it ties the object to the credentials of the VEN.
func (c *BL) CreateVEN(ctx context.Context, v openadr.VEN) (openadr.VEN, error) {
	return sendObject(ctx, c.core, c.core.codec.VENs, "vens", "", v, wire.BLRequest)
}

// UpdateVEN replaces the VEN that has v.ID with v.
func (c *BL) UpdateVEN(ctx context.Context, v openadr.VEN) (openadr.VEN, error) {
	if v.ID == "" {
		return openadr.VEN{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.VENs, "vens", v.ID, v, wire.BLRequest)
}

// DeleteVEN deletes a VEN and returns it as it last was.
func (c *BL) DeleteVEN(ctx context.Context, id string) (openadr.VEN, error) {
	return deleteObject(ctx, c.core, c.core.codec.VENs, "vens", id)
}

// Resources searches the resources of all VENs.
func (c *BL) Resources(ctx context.Context, q ResourceQuery) ([]openadr.Resource, error) {
	return listObjects(ctx, c.core, c.core.codec.Resources, "resources", q.values())
}

// Resource returns the resource with the given ID.
func (c *BL) Resource(ctx context.Context, id string) (openadr.Resource, error) {
	return getObject(ctx, c.core, c.core.codec.Resources, "resources", id)
}

// CreateResource creates a resource and returns it as the VTN stored it. ClientID
// is required, as for CreateVEN.
func (c *BL) CreateResource(ctx context.Context, res openadr.Resource) (openadr.Resource, error) {
	return sendObject(ctx, c.core, c.core.codec.Resources, "resources", "", res, wire.BLRequest)
}

// UpdateResource replaces the resource that has res.ID with res.
func (c *BL) UpdateResource(ctx context.Context, res openadr.Resource) (openadr.Resource, error) {
	if res.ID == "" {
		return openadr.Resource{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Resources, "resources", res.ID, res, wire.BLRequest)
}

// DeleteResource deletes a resource and returns it as it last was.
func (c *BL) DeleteResource(ctx context.Context, id string) (openadr.Resource, error) {
	return deleteObject(ctx, c.core, c.core.codec.Resources, "resources", id)
}
