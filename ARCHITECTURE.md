# Architecture

```text
go-openadr/
├── *.go                 package openadr: domain model, durations, validation, errors
├── client/              VEN and BL clients, OAuth2 token handling
├── server/              the VTN http.Handler and its access rules
│   └── storage/         the persistence interface and the in-memory store
├── auth/                Identity, scopes, Validator, the in-memory Issuer
├── internal/
│   ├── wire/            what a protocol revision has to provide
│   │   └── v310/        the JSON of OpenADR 3.1.0
│   └── vtntest/         an in-process VTN for this module's tests
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

`storage.Memory` is the only implementation today. Searches filter on
simple fields in the store and apply target visibility and paging in the
server, which is correct but loads every candidate; a SQL backend will want
the store interface to take paging and target filters. That is the known
cost of this cut and is to be revisited with the SQLite backend.

### Authentication

The server validates tokens through an interface and does not issue them.
`auth.Issuer` exists so that tests, examples and small deployments are
self-contained; it keeps tokens in memory and is not meant to replace an
identity provider.

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

Not implemented. The intended shape: the server emits a change event after
each successful write to a `notification` dispatcher that evaluates
subscriptions with the same `see` rules as reads, and hands deliveries to
webhook and MQTT publishers behind one interface. The VTN will use an
external MQTT broker. On the client side, webhook reception and MQTT
subscription feed the same callback type as polling.
