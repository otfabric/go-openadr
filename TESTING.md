# Testing go-openadr

How to run each tier. No external VTN is needed: tests run the clients against this module's VTN over loopback. Conventions are in [CONTRIBUTING.md](CONTRIBUTING.md#tests); peers and results in [INTEROP.md](INTEROP.md).

## Tiers

| Tier | Command | Prerequisites | In CI |
|---|---|---|---|
| Unit | `make test` | none | yes, Go 1.26 and 1.27, with `-race` |
| End-to-end | `make e2e` | none | yes |
| Parallel, failure injection | `make chaos` (`CHAOS_COUNT`, default 5) | none | once, as part of the tests |
| Fuzz | `make fuzz` (`FUZZTIME`, default 30s) | none | yes, 10s per target |
| Benchmarks | `make bench` | none | no |
| Example stores | `make storage` | Docker for PostgreSQL | yes, `storage` job |
| Recorded transcripts | `make transcripts-verify` | none | yes, `transcripts` job; the replays also run in `make test` |
| Interop | `make interop` | Docker | Interop workflow |
| Cross-compile | `make cross` | none | no |
| No dependencies | `make nodeps` | none | yes |

## Environment variables

| Variable | Effect |
|---|---|
| `OPENADR_CHAOS_SEED` | Replays one chaos seed |
| `OPENADR_INTEROP_REQUIRE` | A missing prerequisite fails the interop suite instead of skipping it (`make interop` sets it) |
| `OPENADR_INTEROP_<STACK>_IMAGE` | Replaces the pinned image of one stack |
| `OPENADR_STORAGE_REQUIRE`, `OPENADR_POSTGRES_DSN`, `OPENADR_POSTGRES_IMAGE` | The PostgreSQL store test |
| `OPENADR_TEST_MDNS` | Enables the multicast DNS test |

## Recorded transcripts

[testdata/transcripts](testdata/transcripts/README.md) holds exchanges
recorded against OpenLEADR v0.2.8 and v0.1.4, cB-Sigma v0.2.0 and the
ElaadNL client 2.4.2 (level D, software testbed), and cases built from the
OpenAPI documents of 3.1.0 and 3.0.1 (level E). The tests over them:

| Test | Package | What it does |
|---|---|---|
| `TestCorpusDecode` | `internal/transcript` | Decodes every JSON body (1263, 706 of them sent by another implementation) with the codec of its revision, encodes it again and compares |
| `TestSpecCases` | `internal/transcript` | The same for the specification cases (37 of 3.1.0, 13 of 3.0.1) and its example values |
| `TestTranscriptClientReplay` | `client` | Replays 515 recorded VTN answers through the VEN and BL clients: the request must be the recorded one, the result or error the recorded answer |
| `TestTranscriptServerReplay` | `server` | Replays 643 requests (498 of another implementation's client) to a new VTN set up as the recorded one; every answer must match |
| `TestTranscriptDifferential` | `server` | Sends the 312 requests of the scenarios recorded against OpenLEADR and cB-Sigma to this module's VTN; every different answer is classified |
| `TestTranscriptWebhooks` | `notification/webhook` | Replays cB-Sigma's webhook requests to `webhook.Handler` |
| `TestCorpusManifest`, `TestKnownTableUsed` | `internal/transcript` | The manifest and SHA256SUMS agree with the files; no entry of a table of known differences is stale |

`make transcripts-record` records them again (Docker), `make transcripts-spec`
rebuilds the cases (yq, python3), `make transcripts-manifest` the manifest.
`internal/transcript` is test infrastructure and is not in `COVER_PKGS`, as
`internal/mqtttest` and `internal/leakcheck` are not.

## Coverage

`make coverage` measures `COVER_PKGS`: the library packages, whichever package's tests exercise them. Excluded: the examples, `e2e`, the interop suite and the test helper `internal/mqtttest` (see the `COVER_PKGS` line of the [Makefile](Makefile)). `make coverage-check` fails below `COVER_MIN` (91), the same minimum as `coverage-min` in `.github/workflows/ci.yml`.

Each package of `COVER_PKGS` must also reach `COVER_PKG_MIN` (`make coverage-pkg-check`, in CI). Measured lowest at the time of writing: `internal/mqtt` at 83.6 %.

## Goroutine leaks

The packages that start goroutines, and `e2e`, run through `internal/leakcheck` in `TestMain`: the binary fails when a goroutine of this module is still running after the last test, and prints its stack. `internal/leakcheck` is a test helper of the standard library only, as the module has no dependencies.

## Fuzz regressions

`make fuzz` lists its six targets by name. A failing input is written to `testdata/fuzz/<target>/` of the package. Commit it with the fix: `go test` replays it from then on. The seeds
`transcript-*` of `FuzzCodec` are the recorded bodies
(`scripts/transcripts-fuzz-seeds.py`).

## When CI runs

| Event | Workflow | What runs |
|---|---|---|
| Pull request | `pr.yml` | `docs.yml` (docsync, otfabric-policy) always; `ci.yml`, `interop.yml` unless every changed file is documentation; then `OT Fabric Required Checks` |
| Push to a branch | none | nothing; dispatch `ci.yml`, `interop.yml` to test a branch without a pull request |
| Push to `main` | `docs.yml` | the light checks; the merged commit was tested as the pull request's merge commit |
| Schedule | `interop.yml`, `nightly.yml` | the scheduled runs, on `main` |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
