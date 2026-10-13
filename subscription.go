// SPDX-License-Identifier: MIT

package openadr

import "fmt"

// Operation is something that happens to an object and that a client can
// subscribe to.
type Operation string

// Operations defined by OpenADR 3.1.0. A VTN notifies of CREATE, UPDATE and
// DELETE; READ is in the schema and is never notified of by this module.
const (
	OperationCreate Operation = "CREATE"
	OperationRead   Operation = "READ"
	OperationUpdate Operation = "UPDATE"
	OperationDelete Operation = "DELETE"
)

// Subscription asks a VTN to notify a client when objects are created,
// updated or deleted.
type Subscription struct {
	Metadata

	// ClientID is written by the VTN from the creating client's token.
	ClientID string
	// ClientName is chosen by the subscribing client.
	ClientName string
	// ProgramID, when set, limits the subscription to that program, its
	// events and the reports on them.
	ProgramID string
	// ObjectOperations say what to notify of, and where.
	ObjectOperations []ObjectOperation
	// Targets are the targets the subscriber names, as in a search: see
	// the access rules for programs and events.
	Targets []string
}

// Mechanism is a way for a VTN to deliver the notifications of a
// subscription.
type Mechanism string

const (
	// MechanismWebhook delivers to the callback URL. It is what OpenADR
	// 3.1.0 defines, and what the empty Mechanism means.
	MechanismWebhook Mechanism = "WEBHOOK"
	// MechanismWebSocket delivers over the WebSocket the subscriber has
	// open with the VTN, and needs no callback URL. It is not part of
	// OpenADR 3.1.0: it is an extension of OpenLEADR that this module
	// shares. A VTN says in GET /notifiers whether it has it.
	MechanismWebSocket Mechanism = "WEBSOCKET"
)

// ObjectOperation selects operations on kinds of objects and says how the
// subscriber is to be told of them: by default, at a webhook.
type ObjectOperation struct {
	Objects    []ObjectType
	Operations []Operation
	// Mechanism is how to notify; empty for a webhook.
	Mechanism Mechanism
	// CallbackURL is the webhook. It has to use https, and the VTN checks
	// that it answers before it accepts the subscription. It is required
	// for a webhook and has no use otherwise.
	CallbackURL string
	// BearerToken, when set, is sent by the VTN with every notification so
	// that the webhook can tell the VTN from anyone else.
	BearerToken string
}

// Notification is what a VTN sends to a webhook, publishes by MQTT or
// writes to a WebSocket.
type Notification struct {
	// ID identifies the operation the notification is about, for a VTN
	// that sends one: OpenADR 3.1.0 has no such property, OpenLEADR does.
	// A client that is notified over several channels can use it to tell
	// that two notifications are the same.
	ID         string
	ObjectType ObjectType
	Operation  Operation
	// Object is the object the operation happened to, as the subscriber
	// may see it: a Program, Event, Report, Subscription, VEN, Resource or
	// ResourceGroup value, according to ObjectType.
	Object any
	// Targets are the targets of the object that the subscriber may see.
	Targets []string
}

// Validate checks the client-provided properties against the schema. It does
// not look at Metadata or ClientID, which the VTN writes.
func (s Subscription) Validate() error {
	if err := name("clientName", s.ClientName, true); err != nil {
		return err
	}
	if s.ProgramID != "" && !ValidID(s.ProgramID) {
		return invalid("programID", "is not an object ID")
	}
	for i, oo := range s.ObjectOperations {
		f := fmt.Sprintf("objectOperations[%d]", i)
		for j, o := range oo.Objects {
			switch o {
			case ObjectProgram, ObjectEvent, ObjectReport, ObjectSubscription, ObjectVEN, ObjectResource:
			default:
				return invalid(fmt.Sprintf("%s.objects[%d]", f, j), "is %q, not an object type", o)
			}
		}
		for j, op := range oo.Operations {
			switch op {
			case OperationCreate, OperationRead, OperationUpdate, OperationDelete:
			default:
				return invalid(fmt.Sprintf("%s.operations[%d]", f, j), "is %q, not CREATE, READ, UPDATE or DELETE", op)
			}
		}
		switch oo.Mechanism {
		case "", MechanismWebhook:
			if n := len(oo.CallbackURL); n < minURLLen || n > maxURLLen {
				return invalid(f+".callbackUrl", "must be %d to %d characters", minURLLen, maxURLLen)
			}
		case MechanismWebSocket:
			if oo.CallbackURL != "" {
				return invalid(f+".callbackUrl", "has no use with the mechanism %s", MechanismWebSocket)
			}
		default:
			return invalid(f+".mechanism", "is %q, not %s or %s", oo.Mechanism, MechanismWebhook, MechanismWebSocket)
		}
	}
	return targets("targets", s.Targets)
}

// Notifiers says how a VTN notifies its clients: the answer to
// GET /notifiers.
type Notifiers struct {
	// Webhook says the VTN serves subscriptions with callback URLs.
	Webhook bool
	// WebSocket says the VTN serves subscriptions with MechanismWebSocket,
	// which is not part of OpenADR 3.1.0.
	WebSocket bool
	// MQTT describes the VTN's MQTT broker; nil when it has none.
	MQTT *MQTTBinding
}

// MQTTBinding is what a client needs to receive notifications by MQTT.
type MQTTBinding struct {
	// URIs are the addresses of the broker, such as mqtts://broker.example.
	URIs []string
	// Serialization is the format of the messages; always "JSON" so far.
	Serialization  string
	Authentication MQTTAuthentication
}

// MQTT authentication methods defined by OpenADR 3.1.0.
const (
	MQTTAnonymous   = "ANONYMOUS"
	MQTTBearerToken = "OAUTH2_BEARER_TOKEN"
	MQTTCertificate = "CERTIFICATE"
)

// MQTTAuthentication says how a client authenticates with the broker.
type MQTTAuthentication struct {
	// Method is MQTTAnonymous, MQTTBearerToken or MQTTCertificate.
	Method string
	// Username goes with MQTTBearerToken: the user name to present, with
	// the client's OAuth2 token as the password. The value "{clientID}"
	// stands for the client's own ID.
	Username string
	// CACert, ClientCert and ClientKey go with MQTTCertificate, in PEM.
	CACert     string
	ClientCert string
	ClientKey  string
}

// Topics are the MQTT topics on which a VTN publishes the operations on a
// set of objects. A field is empty where the VTN has no such topic: there
// is no Create for a single object, and All is optional.
type Topics struct {
	Create string
	Update string
	Delete string
	// All is a topic filter that matches the other three.
	All string
}
