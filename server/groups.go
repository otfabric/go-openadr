// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"net/http"
	"slices"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server/storage"
)

// Resource groups are not part of OpenADR 3.1.0; see openadr.ResourceGroup.
// The rules are those of OpenLEADR: Business Logic writes them and reads
// them all; a VEN reads the groups that lead to a resource of its own, and
// of their children only those resources and such groups. Nobody is
// notified of them: a 3.1.0 subscription cannot name them.

// businessLogicOnly refuses a client that is not Business Logic. The scope
// of the operation, write_vens, is one a VEN has too.
func businessLogicOnly(s *Server, h handler) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		if !id.IsBusinessLogic() {
			s.problem(w, r, http.StatusForbidden, "only Business Logic may write resource groups")
			return
		}
		h(w, r, id)
	}
}

// groupView decides which resource groups a client sees, and which of
// their children.
type groupView struct {
	// all is set for Business Logic.
	all bool
	// resources are the IDs of the client's own resources, and groups
	// those of the groups that hold one, directly or through other groups.
	resources, groups map[string]bool
}

func (s *Server) viewGroups(ctx context.Context, id auth.Identity) (groupView, error) {
	if id.IsBusinessLogic() {
		return groupView{all: true}, nil
	}
	v := groupView{resources: map[string]bool{}, groups: map[string]bool{}}
	own, err := s.store.Resources().List(ctx, storage.ResourceFilter{ClientID: id.ClientID})
	if err != nil || len(own) == 0 || id.ClientID == "" {
		return v, err
	}
	for _, res := range own {
		v.resources[res.ID] = true
	}
	all, err := s.store.ResourceGroups().List(ctx, storage.ResourceGroupFilter{})
	if err != nil {
		return v, err
	}
	// Groups may hold each other in circles, so: until nothing changes.
	for changed := true; changed; {
		changed = false
		for _, g := range all {
			if !v.groups[g.ID] && slices.ContainsFunc(g.Children, v.holds) {
				v.groups[g.ID], changed = true, true
			}
		}
	}
	return v, nil
}

func (v groupView) holds(c openadr.ResourceGroupChild) bool {
	if c.Type == openadr.ChildResource {
		return v.resources[c.ID]
	}
	return v.groups[c.ID]
}

// show returns the group as the client may see it.
func (v groupView) show(g openadr.ResourceGroup) (openadr.ResourceGroup, bool) {
	if v.all {
		return g, true
	}
	if !v.groups[g.ID] {
		return g, false
	}
	var children []openadr.ResourceGroupChild
	for _, c := range g.Children {
		if v.holds(c) {
			children = append(children, c)
		}
	}
	g.Children = children
	return g, true
}

func (s *Server) resourceGroups() kind[openadr.ResourceGroup, storage.ResourceGroupFilter] {
	return kind[openadr.ResourceGroup, storage.ResourceGroupFilter]{
		codec: s.codec.ResourceGroups,
		coll:  s.store.ResourceGroups(),
		meta:  func(g *openadr.ResourceGroup) *openadr.Metadata { return &g.Metadata },
		see: func(ctx context.Context, id auth.Identity, g openadr.ResourceGroup) (openadr.ResourceGroup, bool, error) {
			view, err := s.viewGroups(ctx, id)
			if err != nil {
				return g, false, err
			}
			g, ok := view.show(g)
			return g, ok, nil
		},
	}
}

func (s *Server) getResourceGroup(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	get(s, s.resourceGroups())(w, r, id)
}

func (s *Server) createResourceGroup(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	create(s, s.resourceGroups())(w, r, id)
}

func (s *Server) updateResourceGroup(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	update(s, s.resourceGroups())(w, r, id)
}

func (s *Server) deleteResourceGroup(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	remove(s, s.resourceGroups())(w, r, id)
}

func (s *Server) listResourceGroups(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	pg, err := parsePage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	targets, err := queryTargets(r.URL.Query()["targets"])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view, err := s.viewGroups(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all, err := s.store.ResourceGroups().List(r.Context(), storage.ResourceGroupFilter{Name: r.URL.Query().Get("resourceGroupName")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]openadr.ResourceGroup, 0, len(all))
	for _, g := range all {
		if len(targets) > 0 && !overlaps(g.Targets, targets) {
			continue
		}
		if g, ok := view.show(g); ok {
			out = append(out, g)
		}
	}
	writeList(s, w, r, s.codec.ResourceGroups, out, pg)
}
