# Limitations

What go-openadr does not do, and what has not been verified. Operation
coverage is in [docs/CONFORMANCE.md](docs/CONFORMANCE.md), the test evidence
in [INTEROP.md](INTEROP.md), the security consequences in
[SECURITY.md](SECURITY.md).

## Scope not implemented

- OpenADR 2.x, and OpenADR 3.0.0: not implemented.
- The VTN server serves OpenADR 3.1.0 only. The VEN and Business Logic clients
  and the webhook handler also speak 3.0.1, which has no `GET /auth/server`, no
  notifiers and no MQTT: `Notifiers` returns `errors.ErrUnsupported` without a
  request, and what a revision cannot carry is refused with an
  `*openadr.ValidationError`.
- Properties the schema does not define are ignored, not retained, as the
  specification allows.
- A VTN given no way to deliver notifications has no subscriptions, and one
  given no MQTT publisher has no topic operations: those answer 501.
- Dispatch or optimization logic, integrations with physical resources, a web
  UI and a CLI are out of scope. What to do with a price or a load-shed request
  is the application's business.
- The stores in `examples/storage` are examples and are not covered by the
  support of this module.

## Limits not enforced

- VENs, resources and reports have no limit per client unless
  `server.Config.Limits` sets one. Subscriptions are limited to 100 per
  client by default, and callback URLs to 16 per subscription.
- `client.Each` stops at a VTN that returns the same full page again, which
  means it ignores `skip`. A VTN that keeps returning full pages of new
  objects is not told apart from a long result: end it with the context or
  by breaking out of the loop.

## Verification

- Tested against the images of
  [otfabric/openadr-interop](https://github.com/otfabric/openadr-interop),
  pinned by digest: OpenLEADR v0.2.8 and v0.1.4, the ElaadNL openadr3-client
  2.4.2, cB-Sigma openadr v0.2.0, Avahi 0.8, and four MQTT brokers. The matrix, with how and where each
  row last ran, is in [INTEROP.md](INTEROP.md#matrix). Mosquitto, NanoMQ,
  HiveMQ CE and EMQX were tested with this module's own publisher and
  subscriber. Apple mDNSResponder and the specification-driven conformance suite
  were run by hand.
- Not verified ([INTEROP.md](INTEROP.md#not-covered)):
  - a webhook receiver of another library, or a WebSocket client of another
    implementation: no reference library has one. cB-Sigma's VTN delivers to
    this module's handler, and this module's sender to the receiver of
    openadr-interop's `cbsigma` image, which is that repository's code;
  - subscriptions with the ElaadNL client beyond `DELETE`;
  - MQTT with a login or over TLS with another implementation's client;
  - a client of this module without credentials against another VTN;
  - TLS towards the OpenLEADR reference, which serves plain HTTP on loopback;
  - any VTN other than OpenLEADR, and any client other than ElaadNL's and the
    conformance suite;
  - hardware and vendor products: [INTEROP.md](INTEROP.md) makes no claim about
    them.
- 3.0.1 is tested against OpenLEADR v0.1.4 only, which does not serve
  subscriptions.
- Certification: none. OpenADR Alliance certification has not been attempted.
