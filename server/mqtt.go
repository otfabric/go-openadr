// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"net/http"
	"strings"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server/storage"
)

// The topics of the MQTT binding, below MQTTConfig.TopicPrefix. <op> is
// create, update or delete.
//
//	programs/<op>                  every program               Business Logic
//	programs/{id}/<op>             one program (no create)     Business Logic
//	programs/{id}/events/<op>      the events of one program   Business Logic
//	events/<op>                    every event                 Business Logic
//	reports/<op>                   every report                Business Logic
//	subscriptions/<op>             every subscription          Business Logic
//	vens/<op>                      every ven                   Business Logic
//	resources/<op>                 every resource              Business Logic
//	vens/{id}/<op>                 one ven (no create)         that VEN
//	vens/{id}/programs/<op>        programs the VEN may see    that VEN
//	vens/{id}/events/<op>          events the VEN may see      that VEN
//	vens/{id}/resources/<op>       the resources of the ven    that VEN
//
// The topics for Business Logic carry objects as Business Logic sees them,
// with all their targets. Those below vens/{id} carry what that VEN may
// see, with the targets it was granted. Who may subscribe to which topic is
// enforced by the broker, not here: the VTN only decides what it publishes
// where, and tells a client the topics it is entitled to.

func (s *Server) topics(base string, withCreate bool) openadr.Topics {
	base = s.mqtt.TopicPrefix + base
	t := openadr.Topics{Update: base + "/update", Delete: base + "/delete", All: base + "/+"}
	if withCreate {
		t.Create = base + "/create"
	}
	return t
}

func (s *Server) writeTopics(w http.ResponseWriter, r *http.Request, t openadr.Topics) {
	body, err := s.codec.MarshalTopics(t)
	s.write(w, r, http.StatusOK, body, err)
}

func (s *Server) getNotifiers(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	n := openadr.Notifiers{Webhook: s.webhooks != nil, WebSocket: s.sockets != nil}
	if s.mqtt != nil {
		n.MQTT = &openadr.MQTTBinding{URIs: s.mqtt.URIs, Serialization: "JSON", Authentication: s.mqtt.Authentication}
	}
	body, err := s.codec.MarshalNotifiers(n)
	s.write(w, r, http.StatusOK, body, err)
}

func (s *Server) mqttRoutes(op func(method, path, scope string, h handler)) {
	const root = "/notifiers/mqtt/topics/"
	// Topics that carry every object of a kind are for clients that may
	// read every object. (The document asks for a scope "read_bl" on some
	// of them, which it does not define.)
	for _, kind := range []string{"programs", "events", "reports", "subscriptions", "vens", "resources"} {
		op("GET", root+kind, auth.ScopeReadAll, func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
			s.writeTopics(w, r, s.topics(kind, true))
		})
	}
	program := func(suffix string, withCreate bool) handler {
		return func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
			id, err := pathID(r)
			if err == nil {
				_, err = s.store.Programs().Get(r.Context(), id)
			}
			if err != nil {
				s.fail(w, r, err)
				return
			}
			s.writeTopics(w, r, s.topics("programs/"+id+suffix, withCreate))
		}
	}
	op("GET", root+"programs/{id}", auth.ScopeReadAll, program("", false))
	op("GET", root+"programs/{id}/events", auth.ScopeReadAll, program("/events", true))

	// Topics of one ven are for the client the ven belongs to.
	ven := func(suffix string, withCreate bool) handler {
		return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
			venID, err := pathID(r)
			if err == nil {
				var v openadr.VEN
				if v, err = s.store.VENs().Get(r.Context(), venID); err == nil && !owns(id, v.ClientID) {
					err = openadr.ErrNotFound
				}
			}
			if err != nil {
				s.fail(w, r, err)
				return
			}
			s.writeTopics(w, r, s.topics("vens/"+venID+suffix, withCreate))
		}
	}
	op("GET", root+"vens/{id}", auth.ScopeReadVENObjects, ven("", false))
	op("GET", root+"vens/{id}/events", auth.ScopeReadVENObjects, ven("/events", true))
	op("GET", root+"vens/{id}/programs", auth.ScopeReadVENObjects, ven("/programs", true))
	op("GET", root+"vens/{id}/resources", auth.ScopeReadVENObjects, ven("/resources", true))
}

// publish sends what happened to an object to the topics it belongs on.
func (s *Server) publish(ctx context.Context, op openadr.Operation, sub subject) {
	verb := "/" + strings.ToLower(string(op))
	prefix := s.mqtt.TopicPrefix
	send := func(topic string, shown []string) {
		body, err := s.codec.MarshalNotification(openadr.Notification{
			ObjectType: sub.kind, Operation: op, Object: sub.object(shown), Targets: shown,
		})
		if err != nil {
			s.logNotify(sub, err)
			return
		}
		s.mqtt.Publisher.Publish(prefix+topic, body)
	}
	single := op != openadr.OperationCreate // an object has no topic for its own creation

	switch sub.kind {
	case openadr.ObjectProgram:
		send("programs"+verb, sub.targets)
		if single {
			send("programs/"+sub.id+verb, sub.targets)
		}
		s.publishToVENs(ctx, sub, "/programs"+verb, send)
	case openadr.ObjectEvent:
		send("events"+verb, sub.targets)
		send("programs/"+sub.programID+"/events"+verb, sub.targets)
		s.publishToVENs(ctx, sub, "/events"+verb, send)
	case openadr.ObjectReport:
		send("reports"+verb, nil)
	case openadr.ObjectSubscription:
		send("subscriptions"+verb, nil)
	case openadr.ObjectVEN:
		send("vens"+verb, nil)
		if single {
			send("vens/"+sub.id+verb, nil)
		}
	case openadr.ObjectResource:
		send("resources"+verb, nil)
		send("vens/"+sub.venID+"/resources"+verb, nil)
	case openadr.ObjectResourceGroup:
		// Not of OpenADR 3.1.0, which has no topic for it: nobody is
		// notified of resource groups.
	}
}

// publishToVENs sends a program or event to the topic of every ven that
// may see it: an object without targets to all of them, one with targets to
// those that were granted one of them, showing those.
func (s *Server) publishToVENs(ctx context.Context, sub subject, suffix string, send func(topic string, shown []string)) {
	vens, err := s.store.VENs().List(ctx, storage.VENFilter{})
	if err != nil {
		s.logNotify(sub, err)
		return
	}
	grants := map[string][]string{}
	for _, v := range vens {
		granted, ok := grants[v.ClientID]
		if !ok {
			if granted, err = s.grantedTargets(ctx, v.ClientID); err != nil {
				s.logNotify(sub, err)
				continue
			}
			grants[v.ClientID] = granted
		}
		if shown, visible := (targetView{match: granted}).show(sub.targets); visible {
			send("vens/"+v.ID+suffix, shown)
		}
	}
}
