# Interoperability

What this module has been tested against, and how to repeat it. Nothing here
is a claim about hardware, vendors or certification: it is a record of
specific tests against specific versions.

## Matrix

| Client | VTN | Result | How |
|---|---|---|---|
| go-openadr VEN and BL | go-openadr | Passing, in CI | `e2e_test.go` |
| go-openadr VEN and BL | [OpenLEADR](https://github.com/OpenLEADR/openleadr-rs) v0.2.7 (Rust) | Passing, run locally | `make interop` |
| [ElaadNL openadr3-client](https://github.com/ElaadNL/openadr3-client) 2.4.0 (Python), VEN and BL | go-openadr | Passing, run locally | `make interop` |
| the specification-driven suite of openadr-interop v0.1.0 | go-openadr | 21 of 21 scenarios, run locally | see below |

Last run: 2026-10-12, on a developer machine (linux/arm64 containers on
macOS), against the published images of openadr-interop v0.1.0, pinned by
digest. The `Interop` workflow runs the same tests in CI on linux/amd64; it
has not completed a run yet.

The reference implementations come from
[otfabric/openadr-interop](https://github.com/otfabric/openadr-interop) as
container images with one contract: a reference VTN that serves a fixture,
and a reference client that runs one operation and prints what it made of
the answer. That repository knows nothing about this module; the tests and
what they assert are here, in `interop/` (build tag `interop`).

## Running it

Needs Docker. The images are those of openadr-interop v0.1.0, pinned by
digest in the Makefile, and are pulled when they are not there:

```sh
make interop
```

`make interop` fails when an image cannot be had. Run directly with
`go test -tags=interop ./interop/...`, a test whose image is missing is
skipped, unless `OPENADR_INTEROP_REQUIRE` is set.
`OPENADR_INTEROP_OPENLEADR_IMAGE` and `OPENADR_INTEROP_ELAADNL_IMAGE` select
other images, for example the `:dev` tags that `make images` builds in a
checkout of openadr-interop.

## This module's clients against OpenLEADR

`interop/openleadr_test.go` starts the OpenLEADR reference VTN with the
baseline fixture of openadr-interop and drives it with `client.BL` and
`client.VEN`, as the fixture's clients:

- **Workflow.** Business Logic creates a program and an event with prices,
  a report request and every kind of payload value; the VEN finds the
  program by paging through all programs, reads the event, and creates,
  updates and deletes a report; Business Logic reads the report by program.
  Timestamps, durations, priorities, intervals and values are compared after
  the round trip.
- **Token handling.** Token endpoint discovery through `/auth/server` and
  the client-credentials grant against OpenLEADR's token service; a wrong
  secret surfaces as a `client.TokenError`.
- **Errors.** 404, 409 and 403 arrive as `openadr.ErrNotFound`,
  `ErrConflict` and `ErrForbidden`, with OpenLEADR's problem body decoded.
- **VENs, resources and targeting.** The fixture's vens, targets and events
  are visible to exactly the clients that should see them; a VEN client
  registers itself and a resource; Business Logic grants it a target and it
  then sees a program that carries it, while a VEN without the grant does not.

## The ElaadNL client against this module's VTN

`interop/elaadnl_test.go` runs `server.Server` in the test process with the
clients of the same fixture, loads the fixture's objects through the API,
and runs the ElaadNL reference client against it from its container:

- **Reads** (34 operations): searches and reads of programs, events, VENs,
  resources and reports as Business Logic and as three different VENs, with
  target, name, parent and paging filters. Each is checked for the client's
  exit code, error code, the HTTP status it saw and the number of objects it
  decoded — which also means the ElaadNL models accepted every object this
  VTN serialized. Errors are checked to arrive as problem objects.
- **Workflow.** The ElaadNL client creates a program, an event and a report,
  registers a VEN and a resource, has Business Logic grant a target, updates
  and deletes. After each step the test looks in the VTN's own store: the
  values that arrived, the client ID the VTN recorded, the targets.
- **Access rules** as the ElaadNL client experiences them: target hiding,
  another VEN's objects answered 404, a duplicate `venName` answered 409.

## What it took

Each of these was found by an interoperability run and changed this module.

- **`ven` objects without `objectType`** (OpenLEADR). The client used to
  reject them. The 3.1.0 schema for that property cannot be met as written
  (docs/CONFORMANCE.md), so the client now accepts any value there, or none.
- **The token endpoint must be an absolute URL** (ElaadNL). The VTN accepted
  a relative `TokenURL` such as `auth/token` and announced it as it was; the
  ElaadNL client hands the value to its OAuth2 library and failed. The schema
  says `format: uri`. `server.New` now requires an absolute URL.

Differences that needed no change: OpenLEADR writes durations in full
(`P0Y0M0DT1H0M0S` for `PT1H`) and timestamps with a `+00:00` offset and
microseconds, and allows one ven object per client. ElaadNL's client cannot
read a program that has a report payload descriptor, with any VTN; the
fixture used here has none. The findings about the reference implementations
themselves are in openadr-interop's docs/COMPATIBILITY.md.

## The specification-driven suite

openadr-interop also has a conformance suite that checks a VTN against the
specification's OpenAPI document. It is run against this module's VTN by
hand:

```sh
# here
go run ./examples/vtn -addr 127.0.0.1:8080 \
    -client utility:bl-secret:bl -client site-1:ven-secret:ven -client site-2:ven2-secret:ven

# in openadr-interop/conformance
OPENADR_INTEROP_BL_SECRET=bl-secret OPENADR_INTEROP_VEN_SECRET=ven-secret OPENADR_INTEROP_VEN2_SECRET=ven2-secret \
go run ./cmd/openadr-interop -url http://127.0.0.1:8080 \
    -bl-client-id utility -ven-client-id site-1 -ven2-client-id site-2 -target-name go-openadr
```

## Not covered

Subscriptions, webhooks and MQTT; TLS between the implementations (the
references run over plain HTTP on loopback); OpenADR 3.0.x; any VTN other
than OpenLEADR; any client other than ElaadNL's and the conformance suite.
