# go-openadr — OpenADR 3 for Go

[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-openadr.svg)](https://pkg.go.dev/github.com/otfabric/go-openadr)
[![CI](https://github.com/otfabric/go-openadr/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-openadr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A Go implementation of [OpenADR 3](https://www.openadr.org/specification),
the demand-response protocol between utilities or aggregators and the
energy resources of their customers. It provides both sides of the REST API:

- **`client`**: a VEN client and a Business Logic client for any OpenADR 3.1 VTN;
- **`server`**: a VTN as an embeddable `http.Handler`;
- **`openadr`** (the module root): the domain model both share.

**Status: pre-release.** OpenADR 3.1.0 only. All 45 operations of the
specification are implemented in the VTN, with OAuth2 client credentials or
JSON Web Tokens, scopes, object ownership, targeting, and notifications by
webhook and by MQTT. The API may still change. The clients are tested
against the OpenLEADR VTN, MQTT notifications included, and the VTN with the
ElaadNL client; webhooks, and the VTN's side of MQTT, are so far tested
between this module's own two sides only. See [INTEROP.md](INTEROP.md) for the evidence,
[docs/CONFORMANCE.md](docs/CONFORMANCE.md) for exactly what is covered and
[PLAN.md](PLAN.md) for what comes next.

Out of scope: OpenADR 2.x, dispatch or optimization logic, integrations with
physical resources (Modbus, OPC UA, ...), a web UI and a CLI. The library
implements the protocol; what to do with a price or a load-shed request is
the application's business.

## go-openadr vs OpenLEADR and the ElaadNL client

go-openadr is tested against two other open-source implementations of
OpenADR 3 (see [INTEROP.md](INTEROP.md)): the
[OpenLEADR](https://github.com/OpenLEADR/openleadr-rs) VTN and client
library, and the [ElaadNL openadr3-client](https://github.com/ElaadNL/openadr3-client).
Both are good software, and each does things this module does not. The
table shows where the three differ, so that you can pick the right one, and
where this module still has to catch up.

| Capability | otfabric/go-openadr | OpenLEADR openleadr-rs | ElaadNL openadr3-client |
|---|:---:|:---:|:---:|
| Language | Go | Rust | Python |
| License | MIT | Apache-2.0 or MIT | Apache-2.0 |
| VTN (server) | ✅ embeddable `http.Handler` | ✅ standalone service | — |
| VEN client | ✅ | ✅ | ✅ |
| Business Logic client | ✅ | ✅ same client | ✅ |
| OpenADR 3.1.0 | ✅ | ✅ | ✅ |
| OpenADR 3.0.1 | — planned | — | ✅ |
| Programs, events, reports, VENs, resources | ✅ | ✅ | ✅ |
| Subscriptions | ✅ | — | ✅ |
| Webhook notifications | ✅ sender and receiver | — | — not documented |
| MQTT notifications | ✅ publisher and subscriber ¹ | ✅ | ✅ client, optional extra |
| MQTT topic operations (12) | ✅ | ✅ 11, none for subscriptions | ✅ |
| WebSocket notifications (not in the specification) | — | ✅ | — |
| OAuth2 client credentials, token caching and renewal | ✅ | ✅ | ✅ |
| Token endpoint discovery (`/auth/server`) | ✅ | ✅ | ✅ |
| Built-in token service for the VTN | ✅ in memory, for tests and small setups | ✅ users in the database | n/a |
| JSON Web Tokens from an external identity provider | ✅ static keys and JWKS | ✅ | n/a |
| VTN without authentication | — | — not documented | ✅ anonymous client |
| Targeting with target hiding | ✅ | ✅ | n/a |
| Persistent storage | ✅ journal file ² | ✅ PostgreSQL | n/a |
| Storage behind an interface (bring your own) | ✅ | prepared, PostgreSQL only | n/a |
| Resource groups (proposed for OpenADR 3.2) | — | ✅ | ✅ |
| mDNS discovery of local VTNs | — | ✅ | — |
| Response compression | — | ✅ | — |
| Typed, validated models | ✅ | ✅ | ✅ pydantic |
| Extra validation by plugin | — | — | ✅ |
| Event intervals as tables | — | — | ✅ pandas, optional |
| Cancellation and deadlines per call | ✅ `context.Context` | ✅ async | — timeouts |
| Structured logging | ✅ `log/slog`, opt-in | ✅ | ✅ |
| Dependencies | none (standard library) | many crates; PostgreSQL to run | several packages |
| Run against the OpenADR Alliance test suite | — | ✅ 166 of 168 applicable, by its authors | — not documented |
| Cross-tested against other implementations in CI | ✅ against both | — not documented | — not documented |

¹ The MQTT client is this module's own (MQTT 3.1.1, QoS 1). The subscriber
is tested on Mosquitto with the notifications OpenLEADR publishes; the
publisher has met this module's test broker only.

² One process, all objects in memory, every change appended to a file. Not
a database: for a large VTN, implement `storage.Store` on one. A SQL store
is planned.

✅ = available · — = not available or not documented · n/a = does not apply
to a client library. For openleadr-rs v0.2.7 and openadr3-client 2.4.0, the
versions go-openadr is tested against, from their public sources and
documentation as of October 2026. If you spot an inaccuracy, please
[open an issue](https://github.com/otfabric/go-openadr/issues) and we will
correct it.

**Choose go-openadr if** you want both sides of the protocol in one Go
module without dependencies, a VTN you embed in your own service with your
own identity provider and store, or webhook notifications.

**Choose OpenLEADR if** you want a VTN to deploy as it is, backed by
PostgreSQL, that its authors have run against the Alliance's test suite, or
if you need resource groups or mDNS today.

**Choose the ElaadNL client if** you write a VEN or Business Logic in
Python, or need OpenADR 3.0.1.

## Install

```sh
go get github.com/otfabric/go-openadr
```

Go 1.23 or later. No dependencies outside the standard library.

## A VEN

```go
ven, err := client.NewVEN(client.Config{
    BaseURL:     "https://vtn.example.com/openadr3",
    Credentials: &client.Credentials{ClientID: id, ClientSecret: secret},
})
if err != nil {
    return err
}

events, err := ven.Events(ctx, client.EventQuery{ProgramID: programID, ActiveOnly: true})
if err != nil {
    return err
}
for _, event := range events {
    for _, interval := range event.Intervals {
        for _, payload := range interval.Payloads {
            if price, ok := payload.Values[0].Number(); ok && payload.Type == "PRICE" {
                fmt.Println(interval.ID, price)
            }
        }
    }
}

_, err = ven.CreateReport(ctx, openadr.Report{
    EventID:    events[0].ID,
    ClientName: "site-42",
    Resources: []openadr.ReportResource{{
        ResourceName: "main-meter",
        Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{
            {Type: "USAGE", Values: []openadr.Value{openadr.Number(1.8)}},
        }}},
    }},
})
```

The client discovers the token endpoint from the VTN (`GET /auth/server`),
obtains a token, keeps it until shortly before it expires, and fetches a new
one when the VTN answers 401. `client.BL` is the same for the Business Logic
side: it creates programs and events, provisions VENs and their targets, and
reads reports.

Errors from the VTN are `*openadr.Error` values and match sentinels:

```go
if errors.Is(err, openadr.ErrNotFound) { ... }
```

## A VTN

```go
vtn, err := server.New(server.Config{
    Store:     storage.NewMemory(),
    Validator: validator,              // resolves a bearer token to a client ID and scopes
    TokenURL:  "https://idp.example.com/oauth/token",
})
if err != nil {
    return err
}
http.Handle("/openadr3/", http.StripPrefix("/openadr3", vtn))
```

The server does not bundle an identity provider: it takes an
`auth.Validator`. For tests and small deployments, `auth.Issuer` is an
in-memory client-credentials token service that is both the `/auth/token`
handler and the validator, and `auth.JWTValidator` validates the JSON Web
Tokens of an identity provider, with static keys or its key set (JWKS).
`storage.OpenFile` is a store that survives a restart. `TokenURL` is what `GET /auth/server` tells clients
and has to be an absolute URL.

[examples/quickstart](examples/quickstart/main.go) runs a VTN, a Business
Logic client and a VEN through the program → event → report workflow in one
process:

```sh
go run ./examples/quickstart
```

[examples/vtn](examples/vtn/main.go) is a standalone VTN to point other
clients at.

## Notifications

A client that would rather be told than poll subscribes with a webhook:

```go
http.Handle("/openadr", &webhook.Handler{
    BearerToken: hookToken, // made up for this subscription
    Notify: func(ctx context.Context, n openadr.Notification) error {
        if event, ok := n.Object.(openadr.Event); ok {
            // n.Operation is CREATE, UPDATE or DELETE
        }
        return nil
    },
})

_, err = ven.CreateSubscription(ctx, openadr.Subscription{
    ClientName: "site-42",
    ObjectOperations: []openadr.ObjectOperation{{
        Objects:     []openadr.ObjectType{openadr.ObjectEvent},
        Operations:  []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate},
        CallbackURL: "https://site-42.example.com/openadr",
        BearerToken: hookToken,
    }},
})
```

The VTN verifies the callback URL before it accepts the subscription, so the
handler has to be reachable first. A VTN serves subscriptions when it is
given a sender:

```go
sender := webhook.NewSender(webhook.SenderConfig{})
defer sender.Close(ctx)
vtn, err := server.New(server.Config{ /* ... */ Webhooks: sender})
```

Delivery is at least once and not durable: a client that must not miss a
change still polls now and then.
[examples/notifications](examples/notifications/main.go) runs both sides.

A VTN with an MQTT broker publishes the same notifications there
(`server.Config.MQTT` with a `mqtt.Publisher`), and a client asks the VTN
for the broker and for its topics and subscribes:

```go
notifiers, err := ven.Notifiers(ctx)                              // where the broker is
topics, err := ven.MQTTTopics(ctx, client.EventsForVEN(venID))    // what this VEN may hear
sub, err := mqtt.NewSubscriber(mqtt.SubscriberConfig{
    Broker: mqtt.Broker{URL: notifiers.MQTT.URIs[0]},
    Topics: []string{topics.All},
    Notify: func(ctx context.Context, topic string, n openadr.Notification) { /* ... */ },
})
```

The broker is yours to run, and its access control decides who can read
which topic: see [SECURITY.md](SECURITY.md).

## Things to know

- **Concurrency.** Clients and the server are safe for concurrent use.
- **Timeouts.** Every client call takes a context that bounds the whole
  call, including a token request it may trigger. The default HTTP client
  has a 30 second timeout; pass your own `http.Client` for TLS settings,
  proxies or other limits.
- **Paging.** A VTN returns at most 50 objects per search. `client.Each`
  iterates over a whole result.
- **Values.** JSON does not distinguish integers from other numbers, so
  payload numbers are `float64`.
- **Validation.** Objects are checked against the schema before they are
  sent; a violation is an `*openadr.ValidationError` and no request is made.
  Payload types, units and other enumerations of the specification are plain
  strings and are not restricted, so private values work.
- **Logging.** The library is silent. The server logs internal errors, and
  the webhook sender and the MQTT publisher and subscriber failed
  deliveries and lost connections, to the `slog.Logger` each is given, if any.

## Documentation

- [API.md](API.md): the public API by package, and the behaviour that
  signatures do not show.
- [ERRORS.md](ERRORS.md): every error a caller can get, and how to tell them
  apart.
- [INTEROP.md](INTEROP.md): what has been tested against which other
  implementation, and how to repeat it.
- [docs/CONFORMANCE.md](docs/CONFORMANCE.md): operation coverage, and how
  the VTN reads the parts of the specification that leave room.
- [ARCHITECTURE.md](ARCHITECTURE.md): packages, the version strategy, and
  the storage and authentication boundaries.
- [SECURITY.md](SECURITY.md): reporting a vulnerability, and what to know
  before deploying.
- [RELEASE.md](RELEASE.md): release notes. [PLAN.md](PLAN.md): roadmap and
  status. [CONTRIBUTING.md](CONTRIBUTING.md): how to change things.

The reference implementations it is tested against, and a conformance suite
that asserts the specification, come from a separate repository,
[otfabric/openadr-interop](https://github.com/otfabric/openadr-interop),
which shares no code with this one.

## Development

```sh
make check    # format, lint, vet, vulnerability scan, tests with -race, coverage, examples
make fuzz     # the parser fuzz targets
make e2e      # this module's clients against this module's VTN, every feature
make interop  # against the OpenLEADR and ElaadNL reference images (needs Docker, see INTEROP.md)
```

## License

MIT, see [LICENSE](LICENSE).
