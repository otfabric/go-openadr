// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"fmt"

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
		return nil, fmt.Errorf("%w: %w", openadr.ErrConfiguration, err)
	}
	return &BL{core: c}, nil
}

// Programs searches the programs of the VTN.
func (c *BL) Programs(ctx context.Context, q ProgramQuery) ([]openadr.Program, error) {
	return listObjects(ctx, c.core, c.core.codec.Programs, "programs", q)
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
	return listObjects(ctx, c.core, c.core.codec.Events, "events", q)
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
	return listObjects(ctx, c.core, c.core.codec.Reports, "reports", q)
}

// Report returns the report with the given ID.
func (c *BL) Report(ctx context.Context, id string) (openadr.Report, error) {
	return getObject(ctx, c.core, c.core.codec.Reports, "reports", id)
}

// VENs searches the VENs of the VTN.
func (c *BL) VENs(ctx context.Context, q VENQuery) ([]openadr.VEN, error) {
	return listObjects(ctx, c.core, c.core.codec.VENs, "vens", q)
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
	return listResources(ctx, c.core, q)
}

// Resource returns the resource with the given ID. OpenADR 3.0.1 reaches
// a resource through its VEN: use ResourceOf.
func (c *BL) Resource(ctx context.Context, id string) (openadr.Resource, error) {
	if err := c.core.byIDOnly("Resource", "ResourceOf"); err != nil {
		return openadr.Resource{}, err
	}
	return getResource(ctx, c.core, "", id)
}

// ResourceOf returns the resource with the given ID of the VEN with the
// given ID. It works with both revisions; in 3.1.0, where a resource is
// reached by its ID alone, venID is not sent.
func (c *BL) ResourceOf(ctx context.Context, venID, id string) (openadr.Resource, error) {
	return getResource(ctx, c.core, venID, id)
}

// CreateResource creates a resource and returns it as the VTN stored it. ClientID
// is required, as for CreateVEN.
func (c *BL) CreateResource(ctx context.Context, res openadr.Resource) (openadr.Resource, error) {
	return sendResource(ctx, c.core, "", res, wire.BLRequest)
}

// UpdateResource replaces the resource that has res.ID with res.
func (c *BL) UpdateResource(ctx context.Context, res openadr.Resource) (openadr.Resource, error) {
	if res.ID == "" {
		return openadr.Resource{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendResource(ctx, c.core, res.ID, res, wire.BLRequest)
}

// DeleteResource deletes a resource and returns it as it last was.
//
// OpenADR 3.0.1 reaches a resource through its VEN: use DeleteResourceOf.
func (c *BL) DeleteResource(ctx context.Context, id string) (openadr.Resource, error) {
	if err := c.core.byIDOnly("DeleteResource", "DeleteResourceOf"); err != nil {
		return openadr.Resource{}, err
	}
	return deleteResource(ctx, c.core, "", id)
}

// DeleteResourceOf deletes the resource with the given ID of the VEN with
// the given ID, and returns it as it last was. venID is used as in
// ResourceOf.
func (c *BL) DeleteResourceOf(ctx context.Context, venID, id string) (openadr.Resource, error) {
	return deleteResource(ctx, c.core, venID, id)
}

// Subscriptions searches the subscriptions of all clients.
func (c *BL) Subscriptions(ctx context.Context, q SubscriptionQuery) ([]openadr.Subscription, error) {
	return listObjects(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", q)
}

// Subscription returns the subscription with the given ID.
func (c *BL) Subscription(ctx context.Context, id string) (openadr.Subscription, error) {
	return getObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", id)
}

// CreateSubscription subscribes this client to notifications and returns the
// subscription as the VTN stored it. Before it accepts one, a VTN sends a
// verification request to every callback URL: the webhook has to be running.
// See package notification/webhook for a handler.
func (c *BL) CreateSubscription(ctx context.Context, sub openadr.Subscription) (openadr.Subscription, error) {
	return sendObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", "", sub, wire.Request)
}

// UpdateSubscription replaces the subscription that has sub.ID with sub.
func (c *BL) UpdateSubscription(ctx context.Context, sub openadr.Subscription) (openadr.Subscription, error) {
	if sub.ID == "" {
		return openadr.Subscription{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", sub.ID, sub, wire.Request)
}

// DeleteSubscription ends a subscription and returns it as it last was.
func (c *BL) DeleteSubscription(ctx context.Context, id string) (openadr.Subscription, error) {
	return deleteObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", id)
}

// Notifiers asks the VTN how it notifies: by webhook, by MQTT, or both.
func (c *BL) Notifiers(ctx context.Context) (openadr.Notifiers, error) {
	return c.core.notifiers(ctx)
}

// MQTTTopics asks the VTN on which MQTT topics it publishes the operations
// on a set of objects. Subscribe to them with package notification/mqtt.
func (c *BL) MQTTTopics(ctx context.Context, of TopicSet) (openadr.Topics, error) {
	return c.core.topics(ctx, of)
}

// --- resource groups
//
// Resource groups are not part of OpenADR 3.1.0: see openadr.ResourceGroup.
// A VTN that does not have them answers these calls with openadr.ErrNotFound.

// ResourceGroups searches the resource groups.
func (c *BL) ResourceGroups(ctx context.Context, q ResourceGroupQuery) ([]openadr.ResourceGroup, error) {
	if err := c.core.only310("resource groups"); err != nil {
		return nil, err
	}
	return listObjects(ctx, c.core, c.core.codec.ResourceGroups, "resource_groups", q)
}

// ResourceGroup returns the resource group with the given ID.
func (c *BL) ResourceGroup(ctx context.Context, id string) (openadr.ResourceGroup, error) {
	if err := c.core.only310("resource groups"); err != nil {
		return openadr.ResourceGroup{}, err
	}
	return getObject(ctx, c.core, c.core.codec.ResourceGroups, "resource_groups", id)
}

// CreateResourceGroup creates a resource group and returns it as the VTN
// stored it. Its children have to exist.
func (c *BL) CreateResourceGroup(ctx context.Context, g openadr.ResourceGroup) (openadr.ResourceGroup, error) {
	if err := c.core.only310("resource groups"); err != nil {
		return openadr.ResourceGroup{}, err
	}
	return sendObject(ctx, c.core, c.core.codec.ResourceGroups, "resource_groups", "", g, wire.Request)
}

// UpdateResourceGroup replaces the resource group that has g.ID with g.
func (c *BL) UpdateResourceGroup(ctx context.Context, g openadr.ResourceGroup) (openadr.ResourceGroup, error) {
	if err := c.core.only310("resource groups"); err != nil {
		return openadr.ResourceGroup{}, err
	}
	if g.ID == "" {
		return openadr.ResourceGroup{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.ResourceGroups, "resource_groups", g.ID, g, wire.Request)
}

// DeleteResourceGroup deletes a resource group and returns it as it last
// was. Its children stay.
func (c *BL) DeleteResourceGroup(ctx context.Context, id string) (openadr.ResourceGroup, error) {
	if err := c.core.only310("resource groups"); err != nil {
		return openadr.ResourceGroup{}, err
	}
	return deleteObject(ctx, c.core, c.core.codec.ResourceGroups, "resource_groups", id)
}
