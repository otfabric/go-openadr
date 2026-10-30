# AGENTS.md — go-openadr

## What this is

Pure Go implementation of OpenADR 3 (revision 3.1.0): VEN and Business Logic clients, a VTN as an embeddable `http.Handler`, webhook, MQTT and WebSocket notifications, `timeline` and `discovery`. The clients and the webhook handler also speak 3.0.1; the VTN serves 3.1.0 only. No dependencies outside the standard library (`make nodeps`). Profile: **protocol-library**. Nothing is certified by the OpenADR Alliance.

## Organisation standards

This repository follows **otfabric/engineering policy 1.1**, profile **protocol-library** (`.otfabric/policy.yaml`, mode `enforce`). Where this file and the organisation standards disagree, this file wins here: say so in your report.

## Where the truth lives

| Question | Source |
|---|---|
| Wire behaviour | OpenADR 3.1.0 OpenAPI, Definition and User Guide; readings of the specification in docs/CONFORMANCE.md |
| 3.0.1 differences | API.md "OpenADR 3.0.1", `internal/wire/v301` |
| What is tested against other implementations | INTEROP.md (openadr-interop images pinned by digest in the Makefile and `interop/harness_test.go`) |
| Scope not implemented, what is not verified | LIMITATIONS.md |
| Design and plan | ARCHITECTURE.md, PLAN.md |
| Errors | ERRORS.md |
| Release history | RELEASE.md (`## vX.Y.Z` … `---`) |

## Hard rules

1. Never lower the coverage minimum (`coverage-min` in `.github/workflows/ci.yml`), add lint suppressions, or skip or delete tests to make CI pass.
2. Never invent protocol semantics: cite the specification, or record the reading in docs/CONFORMANCE.md.
3. Interop claims only from passing runs against the pinned openadr-interop release.
4. No dependency in the root module: `make nodeps` fails. Database drivers stay in the `examples/storage` module.
5. What one OpenADR revision cannot carry is refused, never dropped.
6. No incidental breaking changes to exported API; this library stays on v0 or v1.

## Commands

| When you change… | Run |
|---|---|
| anything | `make check` |
| failure or concurrency behaviour | `make chaos` (a failure prints `OPENADR_CHAOS_SEED=<seed>`) |
| a parser | `make fuzz` |
| interop-relevant behaviour | `make interop` (Docker) |
| the example stores | `make storage` (PostgreSQL needs Docker; CI sets `OPENADR_STORAGE_REQUIRE=1`) |

## Definition of Done

`make check` green; tests for new behaviour including error paths; a fuzz target for a new parser of untrusted input; the document that owns the topic updated (CONTRIBUTING.md lists them); RELEASE.md section for user-visible changes.

## Git and releases

Branches and PRs. Do not push to `main`, tag or run the Release workflow unless the maintainer asks in the session.
