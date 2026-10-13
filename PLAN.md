# OT Fabric OpenADR 3.x plan

**Objective:** a production-quality, open-source Go implementation of
OpenADR 3.x, for both Virtual End Node (VEN) clients and Virtual Top Node
(VTN) servers, backed by an independent interoperability testing framework.

| Repository | Purpose |
|---|---|
| [otfabric/go-openadr](https://github.com/otfabric/go-openadr) | The protocol implementation: VEN client, VTN server, shared data models, authentication, notifications |
| [otfabric/openadr-interop](https://github.com/otfabric/openadr-interop) | Reference implementations as container images behind one contract, fixtures, and a conformance suite that asserts the specification. It knows no library under test; this module consumes it |

Status as of 2026-10-13: v0.0.1 released; v0.1.0 ready, which closes M1 to M3 except for what is marked open below.

## 1. Scope and version strategy

| Version | Priority | Strategy |
|---|---|---|
| OpenADR 3.1.0 | P0 | First implementation, covering both VEN and VTN |
| OpenADR 3.0.1 | P1 | Compatibility through a separate version-specific implementation |
| OpenADR 3.0.0 | P2 | If interoperability requirements justify it |
| OpenADR 2.0a/b | Excluded | Not part of either project |

3.1.0 is implemented completely before 3.0.1 is added. 3.1 is not backward
compatible with 3.0 (resource endpoints, authorization scopes, targeting,
notification capabilities), so one set of wire models cannot serve both.
The design accommodates several revisions without duplicating the client
and server: see [ARCHITECTURE.md](ARCHITECTURE.md).

**Included:** VEN client; VTN server; the Business Logic API needed to
operate the VTN; programs, events, reports, VENs, resources and
subscriptions; OAuth2 authentication and authorization; REST/JSON over
HTTP(S); webhook and MQTT notifications; configuration, validation, error
handling and observability.

**Excluded:** OpenADR 2.x; grid optimization or dispatch algorithms; Modbus,
OPC UA or other industrial protocol integrations; a web UI; a standalone
CLI; utility-specific business logic. The library implements the OpenADR
communication protocol and does not become an energy-management platform.

## 2. Principles

- **Protocol correctness.** The OpenADR OpenAPI document is the authority
  for endpoints, schemas and HTTP behaviour. The public Go API is designed
  by hand; version-specific wire models stay internal.
- **Transport and security.** Standard `net/http`, `context.Context`,
  configurable transports and TLS. OAuth2 client credentials with token
  caching, refresh and token-endpoint discovery. The VTN accepts an external
  token validator rather than requiring a bundled identity provider.
- **Storage.** An interface independent of the HTTP handlers; in-memory for
  tests, SQLite for standalone deployment, PostgreSQL later without changing
  the server API.
- **Notifications.** Webhooks and MQTT as interchangeable mechanisms; an
  external MQTT broker; reliable delivery, reconnects, deduplication and
  recovery tested.
- **Extensibility.** Unknown specification-compatible enumeration values and
  extension fields are allowed where appropriate.

## 3. Roadmap

| Phase | go-openadr | Exit criterion | Status |
|---|---|---|---|
| **M0 Foundation** | Domain models, version strategy, HTTP scaffolding | First authenticated API request against OpenLEADR | **Done.** The Go clients run against OpenLEADR v0.2.7 (`make interop`) |
| **M1 Core REST** | VEN and BL clients, VTN handlers, CRUD, filtering, pagination, storage | Complete 3.1 REST resource API exercised | **Done**: all 45 operations, exercised by `e2e/` and, where the peers can, by `interop/`. Persistence is a journal file; a SQL store is open (see section 6) |
| **M2 Security** | OAuth2, token discovery, scopes, targeting, resource ownership | Unauthorized access consistently rejected | **Done**: JWT validator, callback verification, and an authorization matrix of every operation as five kinds of client (`e2e/matrix_test.go`). The same matrix in openadr-interop's conformance suite: see that repository |
| **M3 Notifications** | Webhook and MQTT for client and server | Notifications work with external peers | Both bindings done on both sides, and a third, WebSockets, as OpenLEADR has it. MQTT and WebSockets work with an external peer: this module's subscribers receive OpenLEADR's notifications, in `make interop`. Open: webhooks, and the VTN as publisher, with an external client |
| **M4 Stabilization** | Production hardening, documentation, examples | OpenADR 3.1 release candidate | **Started** in v0.2.0: the feature gaps against the other implementations closed (section 6), failure injection and load in `e2e/`, an example per feature, the documents revised. Open: see section 6 |
| **M5 Version expansion** | OpenADR 3.0.1 | Both versions independently validated | Not started |

M0 to M4 are about 3.1.0 only. M5 must not introduce compatibility behaviour
that weakens or silently changes 3.1 functionality.

### First end-to-end milestone

Business Logic creates a program and an event on the Go VTN; a Go VEN
discovers the program, retrieves the event and submits a report; Business
Logic retrieves the report.

- Go BL and VEN clients against the Go VTN: **done** (`e2e_test.go`,
  `examples/quickstart`).
- The same workflow asserted independently, by openadr-interop against the
  Go VTN: **done** (scenario WORKFLOW-001).
- With OpenLEADR v0.2.7 as the VTN, driven by the Go BL and VEN clients:
  **done** (`interop/openleadr_test.go`, see [INTEROP.md](INTEROP.md)).
- With ElaadNL's Python client (2.4.0), as BL and as VEN, against the Go VTN:
  **done** (`interop/elaadnl_test.go`).

## 4. Release requirements

| Requirement | Acceptance criterion | Status |
|---|---|---|
| OpenADR 3.1 REST coverage | Every defined operation implemented or explicitly documented as optional | All 45 operations |
| VEN interoperability | Successful tests against at least one independent VTN | Passing locally against OpenLEADR v0.2.7; not in CI yet |
| VTN interoperability | Successful tests with at least one independent VEN | Passing locally with the ElaadNL openadr3-client 2.4.0; not in CI yet |
| Authentication | OAuth2 and authorization scenarios pass | Passing: `e2e/` matrix, JWT forgery tests, interop with both peers |
| Persistence | VTN data survives restart | Passing with `storage.File` (`e2e` restart test), and with the SQLite and PostgreSQL stores of `examples/storage` (`make storage`) |
| Notifications | Webhook and MQTT tested independently | MQTT and WebSocket subscribers: against OpenLEADR. Webhooks, and the VTN's side of MQTT and WebSockets: between this module's two sides only |
| Automated testing | Unit, integration, conformance and interoperability CI | Unit, e2e and interop in CI; the conformance suite runs in openadr-interop's CI against its references, and by hand against this VTN |
| Go quality | Race detector, `go vet`, linting and relevant fuzz tests pass | Passing (`make check`, `make fuzz`) |
| Documentation | GoDoc, runnable VEN/VTN examples and compatibility matrix | GoDoc; nine examples, six of them run in CI; comparison matrix in README.md and test matrix in INTEROP.md |
| Release management | Semantic versioning, changelog and reproducible test results | Releases v0.0.1 to v0.4.0, with notes in RELEASE.md; reference images pinned by digest |

`openadr-interop` is designed with certification preparation in mind, but a
passing result is not an official certification. The OpenADR Alliance has a
separate certification process with licensed test assets, which are not
redistributed in either repository.

## 5. Backlog

| # | Repository | Issue | Status |
|---|---|---|---|
| 1 | go-openadr | Repository architecture, public API conventions and version strategy | Done |
| 2 | openadr-interop | Pin the OpenADR 3.1.0 specification and establish the test framework | Done |
| 3 | go-openadr | Shared 3.1 data models, serialization and validation | Done, except subscriptions and notifications |
| 4 | go-openadr | Reusable HTTP client, VEN API and BL API | Done, except subscriptions |
| 5 | go-openadr | VTN REST handlers and storage abstraction | Done, except subscriptions, notifiers, SQLite |
| 6 | go-openadr | OAuth2 authentication, authorization and targeting | First cut done |
| 7 | openadr-interop | REST API contract tests and lifecycle scenarios | Started: 21 conformance scenarios, 48 cross-stack cases |
| 8 | openadr-interop | Integrate the OpenLEADR Rust VTN into automated tests | Done: reference server image released in openadr-interop v0.1.0 |
| 9 | openadr-interop | Integrate ElaadNL's Python VEN and BL clients into automated tests | Done: reference client image released in openadr-interop v0.1.0 |
| 10 | Both | First end-to-end program → event → report workflow | Done in all three pairings: Go/Go, Go clients/OpenLEADR, ElaadNL client/Go VTN |

## 6. M4 hardening

Done in v0.2.0:

- **Feature gaps closed** against OpenLEADR and the ElaadNL client, as the
  comparison in README.md showed them: a VTN without authentication,
  response compression, discovery of local VTNs by mDNS, event intervals as
  tables, and the two things outside the specification that OpenLEADR has,
  resource groups and WebSocket notifications.
- **Failure injection and load** in `e2e/` (`make chaos`): twelve clients
  at once on both stores, a store that fails, a network that cuts
  connections, webhooks and a broker that fail, tokens that expire, a
  journal cut at any byte.
- **End-to-end coverage of every feature**, and the authorization matrix
  extended to the extensions.
- **An example per feature**, and a VEN and a Business Logic for the
  command line.

Open, carried over from M1 to M3:

1. **The VTN's notifications with an external client.** The ElaadNL client
   can subscribe by MQTT and manage subscriptions; the client contract of
   openadr-interop needs subscription and notification operations for it.
   (Done in v0.2.0: the publisher and the subscriber on Mosquitto, NanoMQ,
   HiveMQ CE and EMQX. Open: with TLS and logins.)
2. **Durable delivery of notifications.** (The SQL store that was to come
   with it is there since v0.3.0, as an example in a module of its own:
   `examples/storage`, on SQLite and PostgreSQL.)

Open, M4 proper:

3. **The extensions and the new behaviour in openadr-interop.** Done in
   v0.4.0 for the reference client (openadr-interop v0.4.0): this module's
   VTN side of subscriptions, webhooks, MQTT, resource groups, a
   non-authenticating VTN, compression and TLS meets the ElaadNL client.
   Discovery against Avahi and OpenLEADR: done in v0.3.0. Open: scenarios
   for the extensions in the conformance suite. Not possible with today's
   references: a webhook receiver and a WebSocket client of another
   implementation.
4. Soak tests over hours. (Rate limiting hook, durable webhook
   notifications and notifications of `READ`: done in v0.4.0. Metrics
   hooks, logging on the client, a retry policy and error sentinels
   throughout: done in v0.3.0, in line with go-modbus and go-opcua.)
5. API review before v1.0.0. Known candidates: `storage.Store` grew a
   method in v0.2.0 and should stop doing so; resource groups will follow
   whatever the specification adopts.
6. The OpenADR Alliance's test suite, if access can be had.
7. Extra validation by plugin, which the ElaadNL client has, if a profile
   such as the Dutch Grid Aware Charging one needs it here.

## 7. References

- [OpenADR Alliance specifications](https://www.openadr.org/specification)
- [OpenADR 3.1.0 OpenAPI YAML](https://github.com/grid-coordination/openadr3-specification/blob/main/3.1.0/openadr3.yaml)
  and [Definition and User Guide](https://github.com/grid-coordination/openadr3-specification/tree/main/3.1.0)
- [OpenLEADR (Rust)](https://github.com/OpenLEADR/openleadr-rs): OpenADR 3.1 VEN library and VTN server
- [ElaadNL openadr3-client (Python)](https://github.com/ElaadNL/openadr3-client): OpenADR 3.0.1/3.1.0 VEN and BL clients
