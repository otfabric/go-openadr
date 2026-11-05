// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server/storage"
)

// subject is an object something happened to, as the notifications need it.
type subject struct {
	kind openadr.ObjectType
	// programID is the program the object is, or belongs to; empty for an
	// object that belongs to none.
	programID string
	// targeted says the object is a program or event: who sees it is
	// decided by targets. Otherwise it is decided by owner.
	targeted bool
	targets  []string
	// owner is the client ID an object of a VEN carries.
	owner string
	// object returns the object for a subscriber that may see the given
	// targets of it.
	object func(shown []string) any
	// id is the object's own ID, and venID the ven of a resource: where the
	// object is published by MQTT depends on them.
	id, venID string
}

// notify tells the subscribers that may see the object what happened to it.
// A subscriber is notified of what a search of its own would show it: the
// objects of its client, and programs and events by the rules for targets,
// with the targets of the subscription in the place of those of a search.
//
// It runs after the write has succeeded and cannot fail the request.
func (s *Server) notify(ctx context.Context, op openadr.Operation, sub subject) {
	// The write is done: what follows no longer depends on the client
	// staying connected.
	ctx = context.WithoutCancel(ctx)
	// The MQTT binding has topics for what changes an object, and none
	// for a read.
	if s.mqtt != nil && op != openadr.OperationRead {
		s.publish(ctx, op, sub)
	}
	if s.webhooks == nil && s.sockets == nil {
		return
	}
	records, err := s.store.Subscriptions().List(ctx, storage.SubscriptionFilter{})
	if err != nil {
		s.logNotify(sub, err)
		return
	}
	// Granted targets, per VEN client that subscribes.
	grants := map[string][]string{}
	for _, rec := range records {
		if rec.ProgramID != "" && rec.ProgramID != sub.programID {
			continue
		}
		var matching []openadr.ObjectOperation
		for _, oo := range rec.ObjectOperations {
			if slices.Contains(oo.Objects, sub.kind) && slices.Contains(oo.Operations, op) {
				matching = append(matching, oo)
			}
		}
		if len(matching) == 0 {
			continue
		}

		var shown []string
		if sub.targeted {
			view := targetView{all: rec.ReadsAll, filtered: len(rec.Targets) > 0, match: rec.Targets}
			if !view.all && view.filtered {
				granted, ok := grants[rec.ClientID]
				if !ok {
					if granted, err = s.grantedTargets(ctx, rec.ClientID); err != nil {
						s.logNotify(sub, err)
						continue
					}
					grants[rec.ClientID] = granted
				}
				view.match = intersect(rec.Targets, granted)
			}
			var visible bool
			if shown, visible = view.show(sub.targets); !visible {
				continue
			}
		} else if !rec.ReadsAll && rec.ClientID != sub.owner {
			continue
		}

		body, err := s.codec.MarshalNotification(openadr.Notification{
			ObjectType: sub.kind, Operation: op, Object: sub.object(shown), Targets: shown,
		})
		if err != nil {
			s.logNotify(sub, err)
			continue
		}
		// One request per callback, and one message to the WebSocket, even
		// when several entries of the subscription ask for it.
		sent := map[string]bool{}
		for _, oo := range matching {
			switch {
			case sent[oo.CallbackURL]:
			case oo.Mechanism == openadr.MechanismWebSocket:
				if s.sockets != nil {
					s.sockets.send(rec.ClientID, body, func() { s.notified(MechanismWebSocket, sub, op) })
				}
			case s.webhooks != nil:
				// Counted before it is handed over: a receiver is never
				// ahead of the metrics.
				s.notified(MechanismWebhook, sub, op)
				if s.outbox != nil {
					s.outbox.add(ctx, rec.ID, oo.CallbackURL, oo.BearerToken, body)
				} else {
					s.webhooks.Send(oo.CallbackURL, oo.BearerToken, body)
				}
			}
			// The callback URL of a WebSocket entry is empty.
			sent[oo.CallbackURL] = true
		}
	}
}

func (s *Server) logNotify(sub subject, err error) {
	if s.logger != nil {
		s.logger.Error("openadr: notification failed", slog.String("object", string(sub.kind)), slog.Any("error", err))
	}
}

// --- subscriptions

// subscriptionSubject describes a subscription to the notifications: it is
// an object of the client that made it.
func subscriptionSubject(sub openadr.Subscription) subject {
	return subject{kind: openadr.ObjectSubscription, id: sub.ID, programID: sub.ProgramID, owner: sub.ClientID,
		object: func([]string) any { return sub }}
}

// findSubscription loads the subscription named in the path, if the client
// may see it.
func (s *Server) findSubscription(r *http.Request, id auth.Identity) (storage.SubscriptionRecord, error) {
	objectID, err := pathID(r)
	if err != nil {
		return storage.SubscriptionRecord{}, err
	}
	rec, err := s.store.Subscriptions().Get(r.Context(), objectID)
	if err != nil {
		return rec, err
	}
	if !owns(id, rec.ClientID) {
		return storage.SubscriptionRecord{}, openadr.ErrNotFound
	}
	return rec, nil
}

// verifyCallbacks checks every callback URL of a subscription that old, the
// subscription it replaces, did not already have. A subscription names at
// most Limits.CallbackURLs distinct URLs; they are verified a few at a
// time, and the first that fails, in the order of objectOperations, is the
// one reported.
func (s *Server) verifyCallbacks(ctx context.Context, sub openadr.Subscription, old *openadr.Subscription) error {
	verified := map[string]bool{}
	if old != nil {
		for _, oo := range old.ObjectOperations {
			if oo.Mechanism != openadr.MechanismWebSocket {
				verified[oo.CallbackURL] = true
			}
		}
	}
	type check struct {
		field, url string
	}
	var checks []check
	distinct := map[string]bool{}
	for i, oo := range sub.ObjectOperations {
		field := fmt.Sprintf("objectOperations[%d]", i)
		if oo.Mechanism == openadr.MechanismWebSocket {
			if s.sockets == nil {
				return &openadr.ValidationError{Field: field + ".mechanism", Reason: "is WEBSOCKET, and this VTN does not notify by WebSocket"}
			}
			continue
		}
		if s.webhooks == nil {
			return &openadr.ValidationError{Field: field + ".mechanism", Reason: "is WEBHOOK, and this VTN does not notify by webhook"}
		}
		if !distinct[oo.CallbackURL] {
			distinct[oo.CallbackURL] = true
			if most := s.limits.callbackURLs; most > 0 && len(distinct) > most {
				return &openadr.ValidationError{Field: field + ".callbackUrl", Reason: fmt.Sprintf("is one more than the %d distinct callback URLs this VTN takes in a subscription", most)}
			}
			if !verified[oo.CallbackURL] {
				checks = append(checks, check{field, oo.CallbackURL})
			}
		}
	}
	errs := make([]error, len(checks))
	var wg sync.WaitGroup
	slots := make(chan struct{}, verifyAtOnce)
	for i, c := range checks {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			errs[i] = s.webhooks.Verify(ctx, c.url)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &openadr.ValidationError{Field: checks[i].field + ".callbackUrl", Reason: err.Error()}
		}
	}
	return nil
}

// verifyAtOnce is how many callback URLs of one subscription are verified
// at the same time.
const verifyAtOnce = 4

func (s *Server) getSubscription(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	rec, err := s.findSubscription(r, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.reads {
		s.notify(r.Context(), openadr.OperationRead, subscriptionSubject(rec.Subscription))
	}
	writeObject(s, w, r, http.StatusOK, s.codec.Subscriptions, rec.Subscription)
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	sub, _, err := readObject(s, w, r, s.codec.Subscriptions)
	if err == nil {
		err = s.verifyCallbacks(r.Context(), sub, nil)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if sub.Metadata, err = s.created(); err != nil {
		s.fail(w, r, err)
		return
	}
	// The VTN writes clientID from the token, and remembers what kind of
	// client subscribed.
	sub.ClientID = id.ClientID
	rec := storage.SubscriptionRecord{Subscription: sub, ReadsAll: id.IsBusinessLogic()}
	if err := s.storeSubscription(r.Context(), rec); err != nil {
		s.fail(w, r, err)
		return
	}
	s.notify(r.Context(), openadr.OperationCreate, subscriptionSubject(sub))
	writeObject(s, w, r, http.StatusCreated, s.codec.Subscriptions, sub)
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	old, err := s.findSubscription(r, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sub, _, err := readObject(s, w, r, s.codec.Subscriptions)
	if err == nil {
		err = s.verifyCallbacks(r.Context(), sub, &old.Subscription)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Whose subscription it is does not change.
	sub.Metadata, sub.ClientID = s.modified(old.Metadata), old.ClientID
	rec := storage.SubscriptionRecord{Subscription: sub, ReadsAll: old.ReadsAll}
	if err := s.store.Subscriptions().Update(r.Context(), rec); err != nil {
		s.fail(w, r, err)
		return
	}
	s.notify(r.Context(), openadr.OperationUpdate, subscriptionSubject(sub))
	writeObject(s, w, r, http.StatusOK, s.codec.Subscriptions, sub)
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	rec, err := s.findSubscription(r, id)
	if err == nil {
		rec, err = s.store.Subscriptions().Delete(r.Context(), rec.ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.notify(r.Context(), openadr.OperationDelete, subscriptionSubject(rec.Subscription))
	writeObject(s, w, r, http.StatusOK, s.codec.Subscriptions, rec.Subscription)
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	pg, err := parsePage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := storage.SubscriptionFilter{ClientName: q.Get("clientName"), ClientID: ownerFilter(id)}
	if f.ProgramID, err = queryID(r, "programID"); err != nil {
		s.fail(w, r, err)
		return
	}
	// The objects parameter selects subscriptions to any of the named kinds.
	var kinds []openadr.ObjectType
	for _, o := range q["objects"] {
		switch kind := openadr.ObjectType(o); kind {
		case openadr.ObjectProgram, openadr.ObjectEvent, openadr.ObjectReport,
			openadr.ObjectSubscription, openadr.ObjectVEN, openadr.ObjectResource:
			kinds = append(kinds, kind)
		default:
			s.fail(w, r, invalidParam("objects", "holds a value that is not an object type"))
			return
		}
	}
	all, err := s.store.Subscriptions().List(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]openadr.Subscription, 0, len(all))
	for _, rec := range all {
		wanted := len(kinds) == 0
		for _, oo := range rec.ObjectOperations {
			wanted = wanted || slices.ContainsFunc(oo.Objects, func(o openadr.ObjectType) bool { return slices.Contains(kinds, o) })
		}
		if wanted {
			out = append(out, rec.Subscription)
		}
	}
	if s.reads {
		for _, sub := range paginate(out, pg) {
			s.notify(r.Context(), openadr.OperationRead, subscriptionSubject(sub))
		}
	}
	writeList(s, w, r, s.codec.Subscriptions, out, pg)
}

// storeSubscription creates rec, within the client's limit of
// subscriptions.
func (s *Server) storeSubscription(ctx context.Context, rec storage.SubscriptionRecord) error {
	q := quota{max: s.limits.subscriptions, noun: "subscriptions", count: counter(s.store.Subscriptions(),
		func(c string) storage.SubscriptionFilter { return storage.SubscriptionFilter{ClientID: c} })}
	if q.max <= 0 {
		return s.store.Subscriptions().Create(ctx, rec)
	}
	s.creating.Lock()
	defer s.creating.Unlock()
	if err := q.admit(ctx, rec.ClientID); err != nil {
		return err
	}
	return s.store.Subscriptions().Create(ctx, rec)
}
