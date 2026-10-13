// SPDX-License-Identifier: MIT

package v310

import (
	"encoding/json"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// Resource groups are not part of OpenADR 3.1.0. The representation is the
// one of OpenLEADR, the implementation they are being proposed from, so
// that the two interoperate; it lives beside the 3.1.0 codec because that
// is the revision the rest of such a VTN speaks.

type resourceGroup struct {
	metadata
	ResourceGroupName string      `json:"resourceGroupName"`
	Attributes        []valuesMap `json:"attributes,omitempty"`
	// Written even when empty, as arrays: OpenLEADR sends them so.
	Targets  []string             `json:"targets"`
	Children []resourceGroupChild `json:"children"`
}

type resourceGroupChild struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func marshalResourceGroup(g openadr.ResourceGroup, form wire.Form) ([]byte, error) {
	w := resourceGroup{ResourceGroupName: g.Name, Targets: g.Targets, Children: make([]resourceGroupChild, len(g.Children))}
	if form == wire.Object {
		w.metadata = metaTo(g.Metadata, openadr.ObjectResourceGroup)
	}
	if w.Targets == nil {
		w.Targets = []string{}
	}
	for i, c := range g.Children {
		w.Children[i] = resourceGroupChild{Type: string(c.Type), ID: c.ID}
	}
	var err error
	if w.Attributes, err = valuesMapsTo(g.Attributes); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func unmarshalResourceGroup(data []byte) (openadr.ResourceGroup, wire.Form, error) {
	var w resourceGroup
	if err := decode(data, &w); err != nil {
		return openadr.ResourceGroup{}, 0, err
	}
	g := openadr.ResourceGroup{Metadata: metaFrom(w.metadata), Name: w.ResourceGroupName, Targets: w.Targets}
	if len(g.Targets) == 0 {
		g.Targets = nil
	}
	for _, c := range w.Children {
		g.Children = append(g.Children, openadr.ResourceGroupChild{Type: openadr.ResourceGroupChildType(c.Type), ID: c.ID})
	}
	var err error
	if g.Attributes, err = valuesMapsFrom("attributes", w.Attributes); err != nil {
		return openadr.ResourceGroup{}, 0, err
	}
	// A request does not identify itself; only the VTN writes an ID.
	form := wire.Request
	if w.ID != "" {
		form = wire.Object
	}
	return g, form, nil
}
