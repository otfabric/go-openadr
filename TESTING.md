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

`make coverage` measures `COVER_PKGS`: the library packages, whichever package's tests exercise them. Excluded: the examples, `e2e`, the interop suite and the test helpers `internal/mqtttest` and `internal/guard/guardtest` (see the `COVER_PKGS` line of the [Makefile](Makefile)). `make coverage-check` fails below `COVER_MIN` (91), the same minimum as `coverage-min` in `.github/workflows/ci.yml`.

Each package of `COVER_PKGS` must also reach `COVER_PKG_MIN` (`make coverage-pkg-check`, in CI). Measured lowest at the time of writing: `internal/mqtt` at 83.6 %.

## Goroutine leaks

The packages that start goroutines, and `e2e`, run through `internal/leakcheck` in `TestMain`: the binary fails when a goroutine of this module is still running after the last test, and prints its stack. `internal/leakcheck` is a test helper of the standard library only, as the module has no dependencies.

## Lifecycle contract

What a type that runs goroutines or holds a connection promises about its `Close` and its callbacks, row by row of the contract matrix of otfabric/engineering (L1–L16), with the tests that hold it to that, or why a row does not apply.

| Row | What | Tests, or n/a |
|---|---|---|
| L1 Close twice | A second `Close` returns at once and changes nothing | `TestServerCloseFromSeveralGoroutines` (`server`), `TestPublisherCloseFromSeveralGoroutines`, `TestSubscriberCloseFromSeveralGoroutines` (`notification/mqtt`), `TestSenderCloseFromSeveralGoroutines` (`notification/webhook`), `TestSubscriberValidatesAndCloses`, `TestWebSocketSubscriberCloseFromSeveralGoroutines` (`e2e`), `TestAdvertiserCloseFromSeveralGoroutines` (`discovery`), `TestFileClosed` (`server/storage`), `TestCloseByClient` (`internal/ws`) |
| L2 Close from two goroutines | Eight goroutines call `Close` at once, up to 200 times over: all return, under `-race` | the tests of L1 but `TestSubscriberValidatesAndCloses` and `TestCloseByClient` |
| L3 Close during a blocked read | The read returns | `TestWebSocketShutdown` (`e2e`), `TestIdleTimeout` (`internal/ws`) |
| L4 Close during a write | `Close` does not wait for a write to a peer that reads nothing | `TestCloseWhileAWriteIsStuck` (`internal/mqtt`), `TestClientWriteToAPeerThatDoesNotRead` (`internal/ws`) |
| L5 Close during the handshake | The handshake ends and what it held is released | MQTT: `TestDialFailures` (`internal/mqtt`), `TestSubscriberCloseWhileSubscribing` (`notification/mqtt`); `websocket.Subscriber`: `TestWebSocketSubscriberCallbackCallsClose/Dial`; a VTN closed while a WebSocket is being opened: `TestServerCloseWhileAWebSocketOpens` (`server`) |
| L6 Cancel a pending request | The call returns the error of its context and is no longer waited for; the next request succeeds | `TestCancelledRequestAndLateAnswer` (`internal/mqtt`). HTTP: n/a, a call is one exchange of `net/http` and holds nothing of this module |
| L7 Late response | An answer to a request that is no longer waited for is dropped, and not counted; it completes no other request | `TestCancelledRequestAndLateAnswer` (`internal/mqtt`). HTTP: n/a, as L6 |
| L8 Response after reconnect | | n/a: an MQTT session is clean and a new `Conn` each time; HTTP has no correlation IDs |
| L9 Callback calls a public method | The call returns with its usual result: callbacks run without a lock of the type held that another of its methods waits for; `Close` is L10 | `TestOnDroppedCallsPublish`, `TestNotifyCallsMethods` (`notification/mqtt`), `TestWebSocketSubscriberNotifyCallsMethods` (`e2e`), `TestNotificationHookCallsTheVTN` (`server`); L11 has the callbacks that block. `webhook.Handler.Notify` runs on the goroutine of its request, with no lock of the Handler |
| L10 Callback calls Close | `Close` from `Notify`, `Dial`, a Metrics callback or the Logger's handler returns, also when two callbacks call it at the same time, and no `Notify` begins after; one from another goroutine still waits for a `Notify` in progress | `TestSubscriberNotifyCallsClose`, `TestSubscriberCallbackCallsClose`, `TestSubscriberCloseFromTwoCallbacks`, `TestSubscriberOnReceivedCallsClose`, `TestSubscriberCloseWaitsForNotify` (`notification/mqtt`), `TestWebSocketSubscriberNotifyCallsClose`, `TestWebSocketSubscriberCallbackCallsClose`, `TestWebSocketSubscriberOnReceivedCallsClose`, `TestWebSocketSubscriberCloseWaitsForNotify` (`e2e`). `webhook.Sender` and `mqtt.Publisher`: documented as not allowed from their own callbacks |
| L11 Slow callback | Holds up its own caller, and no other | `TestWebSocketSlowClientAndReconnect` (`e2e`), `TestPublishDoesNotWaitForAnotherPublishesCallback`, `TestNotificationHookDoesNotHoldUpOthers` |
| L12 Queue at capacity | Dropped and reported | `TestWebSocketSlowClientAndReconnect`, `TestSenderMetricsDroppedAndClosed` (`notification/webhook`), `TestMetrics` and `TestPublisherWithoutBroker` (`notification/mqtt`) |
| L13 Concurrent sessions | | the e2e matrix (`TestAuthorizationMatrix`), `TestParallelClients` (`e2e`) |
| L14 Failure during construction | A constructor that fails leaves nothing behind: each checks what can fail before it starts a goroutine or keeps a socket or a file open | `TestNewRequiresConfig` (`server`), `TestConfig` (`notification/mqtt`), `TestSubscriberValidatesAndCloses` (`e2e`), `TestNoNetwork` (`discovery`), all under the leak check; `TestFileCrashAndCorruption` (`server/storage`). `webhook.NewSender` cannot fail |
| L15 Shutdown with a context | `Close(ctx)` gives up on what is queued when ctx ends | `TestSendDoesNotBlockAndCloseGivesUp`, `TestCloseDoesNotSendWhatIsQueued` (`notification/webhook`), `TestPublisherWithoutBroker` |
| L16 Close racing a reconnect | | `TestSubscriberCloseWhileSubscribing`, `TestPublisherWithoutBroker` (`notification/mqtt`), `TestSubscriberValidatesAndCloses` (`e2e`) |

## Application code that does not return normally

A callback, a metrics hook, a `Notify`, a logger's handler or another part of the application that the module calls on a goroutine of its own, and that does not return normally, is logged with its stack and the module goes on (see [OBSERVABILITY.md](OBSERVABILITY.md#callbacks-that-do-not-return-normally)). There is a test per place it is called from, with `internal/guard/guardtest`: a function that ends this way and a log handler that records the entries, or ends its own calls the same way. `TestCallbacksThatDoNotReturnNormally` and the tests whose names end in `ThatDoNotReturnNormally` are those tests.

## Allocation budgets

What a decoder allocates for a length a peer declares, before the peer has sent what it declared, is bounded and tested: `TestReadPacketAllocationBudget` (`internal/mqtt`) and `TestReadAllocationBudget` (`internal/ws`) declare the largest length that is taken, which costs at most that length, and lengths past it, which are refused before anything is allocated for them. `TestNotificationAllocationBudget` (`internal/wire/codecs`) bounds what decoding a notification allocates per byte of it, for the shapes that cost the most for their size; its budgets are about one and a half times what Go 1.26 and 1.27 take, the releases CI tests with. The size of a notification is bounded by its transport; see the limits in [API.md](API.md#limits). The three tests measure with one processor and keep the smallest of several runs, so that what other goroutines allocate meanwhile is not counted.

## Waiting in tests

A test waits for what it needs to have happened: a channel, a metrics callback, an entry in a log, the packets a fake network has sent, with an upper bound in case it never happens. Where the waiting is on timers, the test runs in a bubble of `testing/synctest`, as `TestRateLimiter` does. A sleep remains only where a test shows that nothing more happens.

## Fuzz regressions

`make fuzz` runs every target that `go test -list '^Fuzz'` finds in a package (15 at the time of writing): a new target needs no change to the Makefile. A failing input is written to `testdata/fuzz/<target>/` of the package. Commit it with the fix: `go test` replays it from then on. The seeds
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
