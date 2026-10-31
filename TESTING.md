# Testing go-openadr

How to run each tier. No external VTN is needed: tests run the clients against this module's VTN over loopback. Conventions are in [CONTRIBUTING.md](CONTRIBUTING.md#tests); peers and results in [INTEROP.md](INTEROP.md).

## Tiers

| Tier | Command | Prerequisites | In CI |
|---|---|---|---|
| Unit | `make test` | none | yes, Go 1.23 to 1.26, with `-race` |
| End-to-end | `make e2e` | none | yes |
| Parallel, failure injection | `make chaos` (`CHAOS_COUNT`, default 5) | none | once, as part of the tests |
| Fuzz | `make fuzz` (`FUZZTIME`, default 30s) | none | yes, 10s per target |
| Benchmarks | `make bench` | none | no |
| Example stores | `make storage` | Docker for PostgreSQL | yes, `storage` job |
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

## Coverage

`make coverage` measures `COVER_PKGS`: the library packages, whichever package's tests exercise them. Excluded: the examples, `e2e`, the interop suite and the test helper `internal/mqtttest` (see the `COVER_PKGS` line of the [Makefile](Makefile)). `make coverage-check` fails below `COVER_MIN` (91), the same minimum as `coverage-min` in `.github/workflows/ci.yml`.

Each package of `COVER_PKGS` must also reach `COVER_PKG_MIN` (`make coverage-pkg-check`, in CI). Measured lowest at the time of writing: `internal/mqtt` at 83.6 %.

## Goroutine leaks

The packages that start goroutines, and `e2e`, run through `internal/leakcheck` in `TestMain`: the binary fails when a goroutine of this module is still running after the last test, and prints its stack. `internal/leakcheck` is a test helper of the standard library only, as the module has no dependencies.

## Fuzz regressions

`make fuzz` lists its six targets by name. A failing input is written to `testdata/fuzz/<target>/` of the package. Commit it with the fix: `go test` replays it from then on.
