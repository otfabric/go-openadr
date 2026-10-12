// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"net/http"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/internal/wire"
	"github.com/otfabric/go-openadr/server/storage"
)

func required(field string) error {
	return &openadr.ValidationError{Field: field, Reason: "is required"}
}

// --- programs

func (s *Server) programs() kind[openadr.Program, storage.ProgramFilter] {
	return kind[openadr.Program, storage.ProgramFilter]{
		codec: s.codec.Programs,
		coll:  s.store.Programs(),
		meta:  func(p *openadr.Program) *openadr.Metadata { return &p.Metadata },
		see: func(ctx context.Context, id auth.Identity, p openadr.Program) (openadr.Program, bool, error) {
			view, err := s.viewObject(ctx, id)
			if err != nil {
				return p, false, err
			}
			var ok bool
			p.Targets, ok = view.show(p.Targets)
			return p, ok, nil
		},
		subject: func(_ context.Context, p openadr.Program) subject {
			return subject{kind: openadr.ObjectProgram, id: p.ID, programID: p.ID, targets: p.Targets, targeted: true,
				object: func(shown []string) any { p.Targets = shown; return p }}
		},
	}
}

func (s *Server) getProgram(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	get(s, s.programs())(w, r, id)
}

func (s *Server) createProgram(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	create(s, s.programs())(w, r, id)
}

func (s *Server) updateProgram(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	update(s, s.programs())(w, r, id)
}

func (s *Server) deleteProgram(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	remove(s, s.programs())(w, r, id)
}

func (s *Server) listPrograms(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	pg, view, err := s.searchView(r, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all, err := s.store.Programs().List(r.Context(), storage.ProgramFilter{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]openadr.Program, 0, len(all))
	for _, p := range all {
		var ok bool
		if p.Targets, ok = view.show(p.Targets); ok {
			out = append(out, p)
		}
	}
	writeList(s, w, r, s.codec.Programs, out, pg)
}

// searchView parses what the program and event searches have in common.
func (s *Server) searchView(r *http.Request, id auth.Identity) (page, targetView, error) {
	pg, err := parsePage(r)
	if err != nil {
		return pg, targetView{}, err
	}
	targets, err := queryTargets(r.URL.Query()["targets"])
	if err != nil {
		return pg, targetView{}, err
	}
	view, err := s.viewSearch(r.Context(), id, targets)
	return pg, view, err
}

// --- events

func (s *Server) events() kind[openadr.Event, storage.EventFilter] {
	return kind[openadr.Event, storage.EventFilter]{
		codec: s.codec.Events,
		coll:  s.store.Events(),
		meta:  func(e *openadr.Event) *openadr.Metadata { return &e.Metadata },
		see: func(ctx context.Context, id auth.Identity, e openadr.Event) (openadr.Event, bool, error) {
			view, err := s.viewObject(ctx, id)
			if err != nil {
				return e, false, err
			}
			var ok bool
			e.Targets, ok = view.show(e.Targets)
			return e, ok, nil
		},
		subject: func(_ context.Context, e openadr.Event) subject {
			return subject{kind: openadr.ObjectEvent, id: e.ID, programID: e.ProgramID, targets: e.Targets, targeted: true,
				object: func(shown []string) any { e.Targets = shown; return e }}
		},
	}
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	get(s, s.events())(w, r, id)
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	create(s, s.events())(w, r, id)
}

func (s *Server) updateEvent(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	update(s, s.events())(w, r, id)
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	remove(s, s.events())(w, r, id)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	pg, view, err := s.searchView(r, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	programID, err := queryID(r, "programID")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var activeOnly bool
	switch r.URL.Query().Get("active") {
	case "", "false":
	case "true":
		activeOnly = true
	default:
		s.fail(w, r, invalidParam("active", "must be true or false"))
		return
	}
	all, err := s.store.Events().List(r.Context(), storage.EventFilter{ProgramID: programID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.now()
	out := make([]openadr.Event, 0, len(all))
	for _, e := range all {
		if activeOnly && ended(e, now) {
			continue
		}
		var ok bool
		if e.Targets, ok = view.show(e.Targets); ok {
			out = append(out, e)
		}
	}
	writeList(s, w, r, s.codec.Events, out, pg)
}

// --- reports

func (s *Server) reports() kind[openadr.Report, storage.ReportFilter] {
	return kind[openadr.Report, storage.ReportFilter]{
		codec: s.codec.Reports,
		coll:  s.store.Reports(),
		meta:  func(rep *openadr.Report) *openadr.Metadata { return &rep.Metadata },
		see: func(_ context.Context, id auth.Identity, rep openadr.Report) (openadr.Report, bool, error) {
			return rep, owns(id, rep.ClientID), nil
		},
		// The VTN writes clientID from the token; it does not change later.
		admit: func(_ context.Context, id auth.Identity, rep openadr.Report, _ wire.Form, old *openadr.Report) (openadr.Report, error) {
			rep.ClientID = id.ClientID
			if old != nil {
				rep.ClientID = old.ClientID
			}
			return rep, nil
		},
		subject: func(ctx context.Context, rep openadr.Report) subject {
			sub := subject{kind: openadr.ObjectReport, id: rep.ID, owner: rep.ClientID, object: func([]string) any { return rep }}
			// A report belongs to the program of its event, while the event exists.
			if e, err := s.store.Events().Get(ctx, rep.EventID); err == nil {
				sub.programID = e.ProgramID
			}
			return sub
		},
	}
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	get(s, s.reports())(w, r, id)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	create(s, s.reports())(w, r, id)
}

func (s *Server) updateReport(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	update(s, s.reports())(w, r, id)
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	remove(s, s.reports())(w, r, id)
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	pg, err := parsePage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := storage.ReportFilter{ClientName: r.URL.Query().Get("clientName"), ClientID: ownerFilter(id)}
	if f.ProgramID, err = queryID(r, "programID"); err == nil {
		f.EventID, err = queryID(r, "eventID")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all, err := s.store.Reports().List(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeList(s, w, r, s.codec.Reports, all, pg)
}

// --- vens

func (s *Server) vens() kind[openadr.VEN, storage.VENFilter] {
	return kind[openadr.VEN, storage.VENFilter]{
		codec: s.codec.VENs,
		coll:  s.store.VENs(),
		meta:  func(v *openadr.VEN) *openadr.Metadata { return &v.Metadata },
		see: func(_ context.Context, id auth.Identity, v openadr.VEN) (openadr.VEN, bool, error) {
			return v, owns(id, v.ClientID), nil
		},
		admit: func(_ context.Context, id auth.Identity, v openadr.VEN, form wire.Form, old *openadr.VEN) (openadr.VEN, error) {
			if form == wire.Object {
				return v, &openadr.ValidationError{Field: "objectType", Reason: "must be BL_VEN_REQUEST or VEN_VEN_REQUEST"}
			}
			if id.IsBusinessLogic() {
				if v.ClientID == "" {
					return v, required("clientID")
				}
				return v, nil
			}
			if form == wire.BLRequest {
				return v, forbidden("only Business Logic may send a BL_VEN_REQUEST")
			}
			// A VEN's object carries its own clientID and the targets
			// Business Logic gave it, whatever the request says.
			v.ClientID, v.Targets = id.ClientID, nil
			if old != nil {
				v.Targets = old.Targets
			}
			return v, nil
		},
		subject: func(_ context.Context, v openadr.VEN) subject {
			return subject{kind: openadr.ObjectVEN, id: v.ID, owner: v.ClientID, object: func([]string) any { return v }}
		},
	}
}

func (s *Server) getVEN(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	get(s, s.vens())(w, r, id)
}

func (s *Server) createVEN(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	create(s, s.vens())(w, r, id)
}

func (s *Server) updateVEN(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	update(s, s.vens())(w, r, id)
}

func (s *Server) deleteVEN(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	remove(s, s.vens())(w, r, id)
}

func (s *Server) listVENs(w http.ResponseWriter, r *http.Request, id auth.Identity) {
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
	all, err := s.store.VENs().List(r.Context(), storage.VENFilter{Name: r.URL.Query().Get("venName"), ClientID: ownerFilter(id)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]openadr.VEN, 0, len(all))
	for _, v := range all {
		if len(targets) == 0 || overlaps(v.Targets, targets) {
			out = append(out, v)
		}
	}
	writeList(s, w, r, s.codec.VENs, out, pg)
}

// --- resources

func (s *Server) resources() kind[openadr.Resource, storage.ResourceFilter] {
	return kind[openadr.Resource, storage.ResourceFilter]{
		codec: s.codec.Resources,
		coll:  s.store.Resources(),
		meta:  func(res *openadr.Resource) *openadr.Metadata { return &res.Metadata },
		see: func(_ context.Context, id auth.Identity, res openadr.Resource) (openadr.Resource, bool, error) {
			return res, owns(id, res.ClientID), nil
		},
		admit: func(ctx context.Context, id auth.Identity, res openadr.Resource, form wire.Form, old *openadr.Resource) (openadr.Resource, error) {
			if form == wire.Object {
				return res, &openadr.ValidationError{Field: "objectType", Reason: "must be BL_RESOURCE_REQUEST or VEN_RESOURCE_REQUEST"}
			}
			if id.IsBusinessLogic() {
				if res.ClientID == "" {
					return res, required("clientID")
				}
				return res, nil
			}
			if form == wire.BLRequest {
				return res, forbidden("only Business Logic may send a BL_RESOURCE_REQUEST")
			}
			// A VEN adds resources to its own ven objects only. Another
			// client's ven is reported like one that does not exist.
			ven, err := s.store.VENs().Get(ctx, res.VENID)
			if err != nil || ven.ClientID != id.ClientID {
				return res, &storage.ConstraintError{Detail: "ven " + res.VENID + " does not exist"}
			}
			res.ClientID, res.Targets = id.ClientID, nil
			if old != nil {
				res.Targets = old.Targets
			}
			return res, nil
		},
		subject: func(_ context.Context, res openadr.Resource) subject {
			return subject{kind: openadr.ObjectResource, id: res.ID, venID: res.VENID, owner: res.ClientID, object: func([]string) any { return res }}
		},
	}
}

func (s *Server) getResource(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	get(s, s.resources())(w, r, id)
}

func (s *Server) createResource(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	create(s, s.resources())(w, r, id)
}

func (s *Server) updateResource(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	update(s, s.resources())(w, r, id)
}

func (s *Server) deleteResource(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	remove(s, s.resources())(w, r, id)
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request, id auth.Identity) {
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
	f := storage.ResourceFilter{Name: r.URL.Query().Get("resourceName"), ClientID: ownerFilter(id)}
	if f.VENID, err = queryID(r, "venID"); err != nil {
		s.fail(w, r, err)
		return
	}
	all, err := s.store.Resources().List(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]openadr.Resource, 0, len(all))
	for _, res := range all {
		if len(targets) == 0 || overlaps(res.Targets, targets) {
			out = append(out, res)
		}
	}
	writeList(s, w, r, s.codec.Resources, out, pg)
}
