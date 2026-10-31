# Feature status

Levels: **implemented**, **tested** (in this repository, `make test` and `make e2e`), **independently verified** (against an implementation not written here). Nothing is certified. Operation by operation, [CONFORMANCE.md](CONFORMANCE.md) has what is implemented, and [INTEROP.md](../INTEROP.md#matrix) has the runs behind every "independently verified", with [what it does not cover](../INTEROP.md#not-covered).

| Feature | VEN and BL clients | VTN server | Evidence |
|---|---|---|---|
| OpenADR 3.1.0 operations | independently verified | independently verified | OpenLEADR v0.2.7 VTN; ElaadNL openadr3-client 2.4.0; openadr-interop conformance suite, run by hand |
| OpenADR 3.0.1 | independently verified | not implemented | OpenLEADR v0.1.4; the VTN serves 3.1.0 only |
| OpenADR 2.x, 3.0.0 | not implemented | not implemented | [LIMITATIONS.md](../LIMITATIONS.md#scope-not-implemented) |
| Resource groups | independently verified | independently verified | OpenLEADR v0.2.7; ElaadNL client |
| Subscriptions, webhook delivery | tested | independently verified, ElaadNL beyond `DELETE` not | No other webhook receiver in the references |
| MQTT notifications | independently verified, no login or TLS with others | independently verified, same | Mosquitto, NanoMQ, HiveMQ CE, EMQX, with this module's publisher and subscriber; OpenLEADR over Mosquitto; ElaadNL listener |
| WebSocket notifications | independently verified | tested | OpenLEADR v0.2.7 for the subscriber; no other WebSocket client in the references |
| TLS | tested | independently verified | ElaadNL client; the OpenLEADR reference serves plain HTTP on loopback |
| DNS-SD discovery (`discovery`) | independently verified | independently verified | Avahi 0.8; OpenLEADR v0.2.7; Apple mDNSResponder by hand |
| Time series helper (`timeline`) | tested | n/a | `timeline` tests and `FuzzReadCSV` |

Wire decoders are fuzzed: MQTT packets, WebSocket frames, DNS messages, events, durations, CSV. `make fuzz` runs them; the Nightly workflow runs each for 5 minutes.

## Platforms

CI runs the tests on the Go versions in `.github/workflows/ci.yml`; the module is pure Go with no dependencies (`make nodeps`, `make cross`).
