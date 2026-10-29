// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"fmt"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// What the two revisions do differently beside the JSON of their objects,
// which is the codec's: where resources are, what there is to discover, and
// what a report has to name.

// only310 refuses what OpenADR 3.0.1 does not have, before anything is sent.
func (c *core) only310(what string) error {
	if c.codec.Version == openadr.Version301 {
		return fmt.Errorf("openadr: %s is not in OpenADR 3.0.1: %w", what, errors.ErrUnsupported)
	}
	return nil
}

// resources returns the collection of the resources of a VEN: /resources
// in 3.1.0, where venID is a filter, and /vens/{venID}/resources in 3.0.1.
func (c *core) resources(venID string) (string, error) {
	if c.codec.Version != openadr.Version301 {
		return "resources", nil
	}
	if venID == "" {
		return "", &openadr.ValidationError{Field: "venID", Reason: "is required by OpenADR 3.0.1, which reaches resources through their VEN"}
	}
	ven, err := objectPath("vens", venID)
	if err != nil {
		return "", err
	}
	return ven + "/resources", nil
}

// byIDOnly refuses to reach a resource by its ID alone in 3.0.1.
func (c *core) byIDOnly(method, instead string) error {
	if c.codec.Version == openadr.Version301 {
		return fmt.Errorf("openadr: %s is not in OpenADR 3.0.1, which reaches a resource through its VEN (use %s): %w", method, instead, errors.ErrUnsupported)
	}
	return nil
}

func listResources(ctx context.Context, c *core, q ResourceQuery) ([]openadr.Resource, error) {
	collection, err := c.resources(q.VENID)
	if err != nil {
		return nil, err
	}
	return listObjects(ctx, c, c.codec.Resources, collection, q)
}

func getResource(ctx context.Context, c *core, venID, id string) (openadr.Resource, error) {
	collection, err := c.resources(venID)
	if err != nil {
		return openadr.Resource{}, err
	}
	return getObject(ctx, c, c.codec.Resources, collection, id)
}

func deleteResource(ctx context.Context, c *core, venID, id string) (openadr.Resource, error) {
	collection, err := c.resources(venID)
	if err != nil {
		return openadr.Resource{}, err
	}
	return deleteObject(ctx, c, c.codec.Resources, collection, id)
}

// sendResource creates res, or with an id replaces the resource that has it.
func sendResource(ctx context.Context, c *core, id string, res openadr.Resource, form wire.Form) (openadr.Resource, error) {
	collection, err := c.resources(res.VENID)
	if err != nil {
		return openadr.Resource{}, err
	}
	return sendObject(ctx, c, c.codec.Resources, collection, id, res, form)
}

// sendReport creates rep, or with an id replaces the report that has it. OpenADR 3.0.1 requires the
// program of the report's event, which 3.1.0 does not have: when it is
// not given, it is read from the event.
func sendReport(ctx context.Context, c *core, id string, rep openadr.Report) (openadr.Report, error) {
	if c.codec.Version == openadr.Version301 && rep.ProgramID == "" && rep.EventID != "" {
		if err := rep.Validate(); err != nil {
			return openadr.Report{}, err
		}
		event, err := getObject(ctx, c, c.codec.Events, "events", rep.EventID)
		if err != nil {
			return openadr.Report{}, fmt.Errorf("openadr: reading the program of event %s, which OpenADR 3.0.1 requires in a report: %w", rep.EventID, err)
		}
		rep.ProgramID = event.ProgramID
	}
	return sendObject(ctx, c, c.codec.Reports, "reports", id, rep, wire.Request)
}

// defaultTokenURL is where the token endpoint is when it is not configured: 3.1.0
// says at GET /auth/server, and 3.0.1, which has no such operation, has
// it at /auth/token of the VTN.
func (c *core) defaultTokenURL() string {
	if c.codec.Version == openadr.Version301 {
		return c.url("auth/token", nil)
	}
	return ""
}
