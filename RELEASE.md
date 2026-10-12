# go-openadr Releases

## v0.1.0

**Date:** 2026-10-13
**Previous release:** v0.0.1

## Summary

The rest of OpenADR 3.1.0: subscriptions, notifications by webhook and by
MQTT on both sides, and the MQTT topic operations, which brings the VTN to
all 45 operations of the specification. With it a validator for JSON Web
Tokens, a store that survives a restart, and an end-to-end test suite of
this module's clients against this module's VTN.

**Upgrade notes**

- `storage.Store` has a new method, `Subscriptions`. An implementation of the interface outside this module has to add it; `storage.Memory` and the new `storage.File` have it.
- `server.Config.TokenURL` was already required to be an absolute URL in v0.0.1; nothing else in a v0.0.1 configuration changes meaning. A VTN that is given neither `Webhooks` nor `MQTT` answers 501 for subscriptions and notifiers, as before.

## Changes

### Added

- **Subscriptions** — `openadr.Subscription`, `ObjectOperation` and `Notification`, and on both clients `Subscriptions`, `Subscription`, `CreateSubscription`, `UpdateSubscription` and `DeleteSubscription`. In the VTN: all five operations, with the `programID`, `clientName` and `objects` filters.
- **Webhook notifications** (`notification/webhook`) — `Sender` delivers a VTN's notifications: in order per callback URL, with retries and backoff, with the subscription's bearer token. By default it accepts https URLs only, follows no redirect and connects to public addresses only, checked on the address actually connected to. `Handler` is the receiving side: it answers the VTN's verification request, checks the bearer token and decodes the notification.
- **Callback verification** — before the VTN accepts a subscription it sends `GET <callbackUrl>?echo=<random>` and requires the value back, as the specification asks. A URL that fails is refused with 400.
- **MQTT notifications** (`notification/mqtt`) — `Publisher` sends a VTN's notifications to a broker at QoS 1, in order, reconnecting as needed; `Subscriber` receives them in a client and resubscribes after a lost connection. Both use this module's own MQTT 3.1.1 client: no dependency was added.
- **Notifier discovery** — `GET /notifiers` and the 12 MQTT topic operations in the VTN; `Notifiers` and `MQTTTopics` on both clients. Topics that carry every object of a kind are given to Business Logic; a VEN is given the topics of its own ven object, on which the VTN publishes what that VEN may see.
- **Who is told what** — by webhook, a subscriber is notified of what a search of its own would show it; by MQTT, each ven's topics carry what its VEN may read by ID. In both, a VEN is shown only the targets it was granted.
- **`auth.JWTValidator`** — validates JSON Web Tokens of an identity provider: RS256/384/512, ES256/384/512, EdDSA and HS256/384/512, with static keys or the provider's key set (JWKS, fetched on demand and rate-limited), and checks of expiry, `nbf`, issuer and audience. The kind of key decides the algorithms it verifies.
- **`storage.File`** (`storage.OpenFile`) — a store that survives a restart: everything in memory, every change appended to a journal file and synced before the write returns, the journal replayed and compacted at start. For a standalone VTN of modest size.
- **End-to-end tests** (`e2e/`, `make e2e`) — this module's clients against this module's VTN over HTTPS: the workflow, roles and ownership, targeting, paging, webhooks, MQTT with a broker in the test, JWTs, a restart on the journal store, and a matrix of every operation as five kinds of client.
- **Examples** — `examples/notifications`: a VEN that subscribes and is told of a new event. `examples/vtn` takes `-webhooks`, `-insecure-webhooks`, `-mqtt` and `-store`.

### Changed

- `storage.Store` gained `Subscriptions()`, which stores `storage.SubscriptionRecord`: the subscription, and whether the subscriber was Business Logic when it subscribed.
- A `server.Server` that is given `Webhooks` or `MQTT` causes background activity: the sender and the publisher have goroutines of their own, and the embedding program closes them on shutdown.
- The README has a comparison with OpenLEADR and the ElaadNL client.

### Verification

- `make e2e`: 16 end-to-end tests, among them an authorization matrix of 47 operations, each as no client, a client with a forged token, the owning VEN, another VEN and Business Logic.
- Unit tests of the webhook sender's address rules, ordering, retries and shutdown; of the MQTT client against packet bytes written out from the MQTT 3.1.1 standard, and against connection loss; of the JWT validator against forged tokens (no signature, `alg: none`, a public key used as an HMAC secret, wrong key, changed payload, wrong issuer and audience); of the journal store against restart, a crash in the middle of a write, corruption and a disk that stops taking writes.
- Three fuzz targets: the duration parser, the event decoder and the MQTT packet reader.
- `make interop` is green against the reference images of openadr-interop v0.2.0 (OpenLEADR v0.2.7 with a Mosquitto broker, ElaadNL openadr3-client 2.4.0), pinned by digest: everything of v0.0.1, and MQTT notifications from OpenLEADR. The VEN finds the broker and its topics through OpenLEADR's API, and this module's subscriber receives the create, update and delete of an event on Mosquitto.
- The conformance suite of openadr-interop v0.2.0, with its authorization scenarios: 24 of 24 against the VTN.

### Known limitations

- **Webhooks, and the VTN's side of MQTT, have not been tested against another implementation.** OpenLEADR has no webhook subscriptions, and the reference client cannot subscribe yet. The MQTT publisher has met this module's test broker only; the subscriber is tested on Mosquitto with OpenLEADR's notifications.
- Notifications are delivered at least once while the VTN runs, and are not durable: what is queued when the process stops is lost. A client that must not miss a change still polls, and does so after a reconnect.
- The VTN decides what it publishes on which MQTT topic; who may read a topic is for the broker's access control, which this module does not configure.
- No notification for `READ`, which the schema lists as an operation.
- `storage.File` is not a database: one process, all objects in memory. A SQL store is not included: the pure-Go SQLite driver needs a newer Go than this module supports and would end its freedom from dependencies. It is to follow as a separate module.
- What a webhook subscriber may be told is decided by its role when it subscribed, not by its token at the time of the notification.

---

## v0.0.1

**Date:** 2026-10-12
**Previous release:** none

## Summary

First release: an OpenADR 3.1.0 VEN client and Business Logic client
(`client`) and a VTN as an embeddable `http.Handler` (`server`) in pure Go,
on a shared domain model. Programs, events, reports, VENs and resources,
with OAuth2 client credentials, scopes, object ownership and targeting. No
dependencies outside the standard library; Go 1.23 or later.

The clients are tested against the OpenLEADR VTN v0.2.7, and the VTN with
the ElaadNL openadr3-client 2.4.0, through the reference images of
[otfabric/openadr-interop](https://github.com/otfabric/openadr-interop)
v0.1.0.

Subscriptions, webhooks and MQTT notifications are not implemented, and the
only store is in memory. The API is not frozen: until v1.0.0 a minor release
may change it.

## Changes

### Added

- **`openadr`** (the module root) — the domain model: `Program`, `Event`, `Report`, `VEN`, `Resource`, intervals, payload descriptors and report descriptors, independent of any one revision's JSON. `Value` holds a number, string, boolean or point. `Duration` is an ISO 8601 duration as the specification's pattern defines it, with calendar arithmetic (`AddTo`) and conversion to and from `time.Duration`. `Validate` on every object checks it against the 3.1.0 schema.
- **`client`** — `VEN` and `BL`, the two roles of the specification, each with the operations its scopes allow: searching and reading everything they may see, and creating, updating and deleting programs and events (BL), reports (VEN), VENs and resources (both). `context.Context` on every call; `Each` iterates over a whole search result page by page.
- **OAuth2 in the client** — the client-credentials grant, token endpoint discovery through `GET /auth/server`, a token cache that renews shortly before expiry, and one retry with a fresh token when the VTN answers 401. A VTN reached over HTTPS cannot redirect the client secret to a cleartext token endpoint. `TokenSource` plugs in any other way of getting a token.
- **`server`** — the VTN: every operation on programs, events, reports, VENs and resources, with filters and paging, the scope checks of the specification, ownership of VEN-created objects, and targeting with target hiding. Errors are problem objects. `GET /auth/server` and an optional `POST /auth/token`.
- **`auth`** — `Validator`, the interface through which the VTN resolves a bearer token to a client ID and scopes, so that it works with any identity provider; the scopes of OpenADR 3.1.0; and `Issuer`, an in-memory client-credentials token service for tests, examples and small deployments.
- **`server/storage`** — `Store`, the persistence boundary of the VTN, which enforces name uniqueness and references atomically, and `Memory`, the in-process implementation.
- **Errors** — `*openadr.Error` for a VTN's error answer, matching `ErrBadRequest`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound` and `ErrConflict` through `errors.Is`; `*openadr.ValidationError` for an object that violates the schema, raised before anything is sent; `*client.TokenError` for a refused token request. See [ERRORS.md](ERRORS.md).
- **Examples** — `examples/quickstart` runs a VTN, a Business Logic client and a VEN through the program → event → report workflow in one process; `examples/vtn` is a standalone VTN.
- **Version strategy** — the JSON of a revision lives in `internal/wire/<revision>` behind one codec interface, so that OpenADR 3.0.1 can be added without a second public model. See [ARCHITECTURE.md](ARCHITECTURE.md).

### Verification

- Unit and end-to-end tests with the race detector; 86% statement coverage of the library packages.
- Fuzz targets for the duration parser and the event decoder.
- `make interop`: the clients against the OpenLEADR VTN v0.2.7 (workflow, token handling, errors, VENs, resources, targeting), and the ElaadNL openadr3-client 2.4.0 against the VTN (34 read operations and the workflow, checked in the VTN's store). See [INTEROP.md](INTEROP.md).
- The conformance suite of openadr-interop, which asserts the specification's OpenAPI document: 21 of 21 scenarios against the VTN.

Two faults were found by these runs and fixed before the release: the client rejected `ven` objects without `objectType`, which OpenLEADR sends, and the VTN announced a relative token URL, which the ElaadNL client cannot use and the schema does not allow.

### Known limitations

- Subscriptions and the notifier endpoints answer 501: 27 of the 45 operations of OpenADR 3.1.0 are implemented. No webhook or MQTT notification delivery.
- The only `Store` is in memory: a VTN loses its objects when it stops.
- OpenADR 3.1.0 only; no 3.0.x.
- No gzip, no mDNS discovery of local VTNs, no VTN mode without authentication.
- Properties the schema does not define are ignored, not kept.
- Where the specification leaves room, the VTN has made choices that another implementation may make differently: see [docs/CONFORMANCE.md](docs/CONFORMANCE.md).
- Verified against OpenLEADR and the ElaadNL client only. Not certified by the OpenADR Alliance.

---
