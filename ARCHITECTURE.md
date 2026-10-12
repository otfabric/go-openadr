# Architecture

```text
go-openadr/
├── *.go                 package openadr: domain model, durations, validation, errors
├── client/              VEN and BL clients, OAuth2 token handling
├── server/              the VTN http.Handler and its access rules
│   └── storage/         the persistence interface; Memory and File
├── auth/                Identity, scopes, Validator; Issuer and JWTValidator
├── notification/
│   ├── webhook/         Sender (VTN side) and Handler (client side)
│   └── mqtt/            Publisher (VTN side) and Subscriber (client side)
├── internal/
│   ├── wire/            what a protocol revision has to provide
│   │   └── v310/        the JSON of OpenADR 3.1.0
│   ├── mqtt/            an MQTT 3.1.1 client
│   └── mqtttest/        a broker for tests
├── e2e/                 this module's clients against this module's VTN
├── interop/             against other implementations (build tag)
└── examples/
```

Dependencies point one way: `client` and `server` use the domain model and a
wire codec; nothing in the root package knows about HTTP or JSON.

## Domain model and wire model

The public types (`openadr.Program`, `Event`, `Report`, `VEN`, `Resource`)
are designed by hand and carry no JSON tags. The JSON of a revision lives in
`internal/wire/<revision>`, as unexported structures and the conversions to
and from the domain model.

This keeps the JSON shape of one revision out of the public API. It matters
because OpenADR 3.1 is not backward compatible with 3.0: targets changed
from `valuesMap` objects to strings, VEN resources moved from
`/vens/{venID}/resources` to `/resources`, and the scopes were redefined.
A second revision is a second `wire.Codec`, not a second set of public types.

The codec covers message bodies. Paths, query parameters and scopes are
still written directly in `client` and `server` for 3.1.0; adding 3.0.1 will
need a per-revision table for those as well. That is deliberately left
until 3.0.1 is implemented, when the real differences can shape it.

A `wire.ObjectCodec` distinguishes three forms of an object: the VTN's
representation, a request from a VEN, and a request from Business Logic. The
last two differ for VENs and resources only, where the specification uses
separate request schemas with an `objectType` discriminator.

The domain model follows 3.1 where the revisions differ (targets are
strings).

## The server

`server.Server` holds no state. A request goes through:

1. **Routing** (`net/http.ServeMux` patterns). Unknown paths and methods get
   a problem body like every other error.
2. **Authentication.** The bearer token goes to the `auth.Validator`, which
   returns an `auth.Identity`: a client ID and scopes.
3. **Scope check** against the scope the specification assigns to the
   operation. `read_all` includes the narrower read scopes.
4. **The operation**, which applies ownership and targeting and calls the
   store.

Read, create, update and delete by ID are one generic implementation
(`server/crud.go`) parameterized per object type with two rules: `see`
(may this client know of this object, and which of its targets) and `admit`
(what a client may put in a create or update). Searches are written out per
type because their filters differ.

A client that may not see an object is told it does not exist (404), so
that object IDs cannot be probed.

### Storage

`storage.Store` is the only place state lives. It enforces what has to be
decided atomically with a write: name uniqueness, references to parent
objects, and refusing to delete a program that has events or a VEN that has
resources. Everything else — who may see or write what — stays in the
server, so that every backend behaves the same.

`storage.Memory` holds everything in the process. `storage.File` is the
same with a journal: a change is applied to memory and appended to a file
as one step, under one lock, so that the file replays to the state memory
has; the objects are written in the JSON of 3.1.0 through the wire codec.
It gives a standalone VTN persistence without a dependency. It is not a
database, and a SQL store remains to be written: the pure-Go SQLite driver
needs a newer Go than this module supports, so it will be a module of its
own.

Searches filter on simple fields in the store and apply target visibility
and paging in the server, which is correct but loads every candidate; a SQL
backend will want the store interface to take paging and target filters.
That is the known cost of this cut.

### Authentication

The server validates tokens through an interface and does not issue them.
`auth.JWTValidator` is the implementation for an identity provider that
issues JSON Web Tokens. `auth.Issuer` exists so that tests, examples and
small deployments are self-contained; it keeps tokens in memory and is not
meant to replace an identity provider.

## The clients

`client.VEN` and `client.BL` share one transport (`client/transport.go`):
URL building, token handling, error mapping. They differ in the methods
they expose and in the request form they send for VENs and resources.

A 401 is retried once with a fresh token, for any method: a VTN that answers
401 has not acted on the request. Nothing else is retried; retry policy for
transport failures belongs to the application or its `http.Client`.

The token endpoint is discovered from the VTN unless configured. Because the
client secret is sent there, a VTN reached over HTTPS cannot redirect the
client to a cleartext token endpoint.

## Notifications

After a write has succeeded, the server works out who is to be told
(`server/notify.go`): it lists the subscriptions and, for each, applies the
same visibility rules as a search by that client would meet, with the
subscription's targets in the place of the search's. It does this in the
request, so that what is decided reflects the state the write left; the
delivery itself is handed to a `WebhookSender` and never waits.

A notification is decided without a request and its token, so the store
keeps with each subscription whether its client was Business Logic
(`storage.SubscriptionRecord`).

`notification/webhook.Sender` owns delivery: one queue per worker, a
callback URL always on the same worker, so that order per callback holds
across retries without a lock per URL. It also owns the rules for URLs that
clients choose, because they have to hold for the verification request and
for every delivery alike. The server sees an interface with two methods and
does not import the package.

The same package has the receiving side, `Handler`, so that both ends of
the webhook binding are tested against each other in one place.

The MQTT binding has no subscriptions to evaluate: a topic is the
subscription. The server publishes each change on the topics it belongs to
(`server/mqtt.go`): the topics of all objects of the kind, and for a
program or event the topic of every ven that may see it, with the object
cut down to what that VEN may see. That is one message per ven for an
untargeted object, which is the price of privacy on a medium without
per-message access control.

`notification/mqtt` wraps `internal/mqtt`, an MQTT 3.1.1 client written for
this module so that it stays free of dependencies: CONNECT, PUBLISH and
SUBSCRIBE at QoS 1, keep-alive, TLS. Reconnecting and resending live one
level up, in `Publisher` and `Subscriber`, where the policy is.

## Tests

- Unit tests sit next to the code.
- `e2e/` runs this module's clients against this module's VTN over HTTPS,
  with every feature, including those no other implementation can exercise
  yet. A bug both sides share is invisible there.
- `interop/` (build tag `interop`) runs each side against another
  implementation, from the images of otfabric/openadr-interop.
- That repository's conformance suite checks the VTN against the
  specification's own OpenAPI document.

Not there yet: durable delivery of notifications, which a journal of
pending deliveries in the store would give.
