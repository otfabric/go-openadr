# go-openadr Releases

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
