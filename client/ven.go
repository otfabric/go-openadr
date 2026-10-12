// SPDX-License-Identifier: MIT

package client

import (
	"context"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// VEN is the client of a virtual end node. It reads programs and events,
// reports on them, and maintains the VEN's own ven and resource objects.
//
// A VTN shows a VEN only the reports, vens and resources created with its
// own credentials; asking for another client's object fails with
// openadr.ErrNotFound.
type VEN struct {
	core *core
}

// NewVEN returns a VEN client. It does not contact the VTN.
func NewVEN(cfg Config) (*VEN, error) {
	c, err := newCore(cfg)
	if err != nil {
		return nil, err
	}
	return &VEN{core: c}, nil
}

// Programs searches the programs this VEN may see.
func (c *VEN) Programs(ctx context.Context, q ProgramQuery) ([]openadr.Program, error) {
	return listObjects(ctx, c.core, c.core.codec.Programs, "programs", q.values())
}

// Program returns the program with the given ID.
func (c *VEN) Program(ctx context.Context, id string) (openadr.Program, error) {
	return getObject(ctx, c.core, c.core.codec.Programs, "programs", id)
}

// Events searches the events this VEN may see.
func (c *VEN) Events(ctx context.Context, q EventQuery) ([]openadr.Event, error) {
	return listObjects(ctx, c.core, c.core.codec.Events, "events", q.values())
}

// Event returns the event with the given ID.
func (c *VEN) Event(ctx context.Context, id string) (openadr.Event, error) {
	return getObject(ctx, c.core, c.core.codec.Events, "events", id)
}

// Reports searches the reports this VEN created.
func (c *VEN) Reports(ctx context.Context, q ReportQuery) ([]openadr.Report, error) {
	return listObjects(ctx, c.core, c.core.codec.Reports, "reports", q.values())
}

// Report returns the report with the given ID.
func (c *VEN) Report(ctx context.Context, id string) (openadr.Report, error) {
	return getObject(ctx, c.core, c.core.codec.Reports, "reports", id)
}

// CreateReport creates a report and returns it as the VTN stored it. The VTN
// records the report under the credentials of this client.
func (c *VEN) CreateReport(ctx context.Context, rep openadr.Report) (openadr.Report, error) {
	return sendObject(ctx, c.core, c.core.codec.Reports, "reports", "", rep, wire.Request)
}

// UpdateReport replaces the report that has rep.ID with rep.
func (c *VEN) UpdateReport(ctx context.Context, rep openadr.Report) (openadr.Report, error) {
	if rep.ID == "" {
		return openadr.Report{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Reports, "reports", rep.ID, rep, wire.Request)
}

// DeleteReport deletes a report and returns it as it last was.
func (c *VEN) DeleteReport(ctx context.Context, id string) (openadr.Report, error) {
	return deleteObject(ctx, c.core, c.core.codec.Reports, "reports", id)
}

// VENs searches the VENs registered with the credentials of this client.
func (c *VEN) VENs(ctx context.Context, q VENQuery) ([]openadr.VEN, error) {
	return listObjects(ctx, c.core, c.core.codec.VENs, "vens", q.values())
}

// VEN returns the VEN with the given ID.
func (c *VEN) VEN(ctx context.Context, id string) (openadr.VEN, error) {
	return getObject(ctx, c.core, c.core.codec.VENs, "vens", id)
}

// CreateVEN creates a VEN and returns it as the VTN stored it. ClientID and
// Targets are not sent: the VTN writes the first and Business Logic the second.
func (c *VEN) CreateVEN(ctx context.Context, v openadr.VEN) (openadr.VEN, error) {
	return sendObject(ctx, c.core, c.core.codec.VENs, "vens", "", v, wire.Request)
}

// UpdateVEN replaces the VEN that has v.ID with v. Targets are
// not sent and keep the value Business Logic gave them.
func (c *VEN) UpdateVEN(ctx context.Context, v openadr.VEN) (openadr.VEN, error) {
	if v.ID == "" {
		return openadr.VEN{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.VENs, "vens", v.ID, v, wire.Request)
}

// DeleteVEN deletes a VEN and returns it as it last was.
func (c *VEN) DeleteVEN(ctx context.Context, id string) (openadr.VEN, error) {
	return deleteObject(ctx, c.core, c.core.codec.VENs, "vens", id)
}

// Resources searches the resources of this VEN.
func (c *VEN) Resources(ctx context.Context, q ResourceQuery) ([]openadr.Resource, error) {
	return listObjects(ctx, c.core, c.core.codec.Resources, "resources", q.values())
}

// Resource returns the resource with the given ID.
func (c *VEN) Resource(ctx context.Context, id string) (openadr.Resource, error) {
	return getObject(ctx, c.core, c.core.codec.Resources, "resources", id)
}

// CreateResource creates a resource and returns it as the VTN stored it. ClientID
// and Targets are not sent; see CreateVEN.
func (c *VEN) CreateResource(ctx context.Context, res openadr.Resource) (openadr.Resource, error) {
	return sendObject(ctx, c.core, c.core.codec.Resources, "resources", "", res, wire.Request)
}

// UpdateResource replaces the resource that has res.ID with res. Targets
// are not sent and keep the value Business Logic gave them.
func (c *VEN) UpdateResource(ctx context.Context, res openadr.Resource) (openadr.Resource, error) {
	if res.ID == "" {
		return openadr.Resource{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Resources, "resources", res.ID, res, wire.Request)
}

// DeleteResource deletes a resource and returns it as it last was.
func (c *VEN) DeleteResource(ctx context.Context, id string) (openadr.Resource, error) {
	return deleteObject(ctx, c.core, c.core.codec.Resources, "resources", id)
}

// Subscriptions searches the subscriptions this client made.
func (c *VEN) Subscriptions(ctx context.Context, q SubscriptionQuery) ([]openadr.Subscription, error) {
	return listObjects(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", q.values())
}

// Subscription returns the subscription with the given ID.
func (c *VEN) Subscription(ctx context.Context, id string) (openadr.Subscription, error) {
	return getObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", id)
}

// CreateSubscription subscribes this client to notifications and returns the
// subscription as the VTN stored it. Before it accepts one, a VTN sends a
// verification request to every callback URL: the webhook has to be running.
// See package notification/webhook for a handler.
func (c *VEN) CreateSubscription(ctx context.Context, sub openadr.Subscription) (openadr.Subscription, error) {
	return sendObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", "", sub, wire.Request)
}

// UpdateSubscription replaces the subscription that has sub.ID with sub.
func (c *VEN) UpdateSubscription(ctx context.Context, sub openadr.Subscription) (openadr.Subscription, error) {
	if sub.ID == "" {
		return openadr.Subscription{}, &openadr.ValidationError{Field: "id", Reason: "is required for an update"}
	}
	return sendObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", sub.ID, sub, wire.Request)
}

// DeleteSubscription ends a subscription and returns it as it last was.
func (c *VEN) DeleteSubscription(ctx context.Context, id string) (openadr.Subscription, error) {
	return deleteObject(ctx, c.core, c.core.codec.Subscriptions, "subscriptions", id)
}

// Notifiers asks the VTN how it notifies: by webhook, by MQTT, or both.
func (c *VEN) Notifiers(ctx context.Context) (openadr.Notifiers, error) {
	return c.core.notifiers(ctx)
}

// MQTTTopics asks the VTN on which MQTT topics it publishes the operations
// on a set of objects. Subscribe to them with package notification/mqtt.
func (c *VEN) MQTTTopics(ctx context.Context, of TopicSet) (openadr.Topics, error) {
	return c.core.topics(ctx, of)
}
