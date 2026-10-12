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

**Status: pre-release.** OpenADR 3.1.0 only. Programs, events, reports, VENs
and resources work end to end, with OAuth2 client credentials, scopes,
object ownership and targeting. Subscriptions, webhooks and MQTT
notifications are not implemented yet, and storage is in-memory only. The
API may still change. The clients are tested against the OpenLEADR VTN, and
the VTN with the ElaadNL client. See
[INTEROP.md](INTEROP.md) for the evidence, [docs/CONFORMANCE.md](docs/CONFORMANCE.md)
for exactly what is covered and [PLAN.md](PLAN.md) for what comes next.

Out of scope: OpenADR 2.x, dispatch or optimization logic, integrations with
physical resources (Modbus, OPC UA, ...), a web UI and a CLI. The library
implements the protocol; what to do with a price or a load-shed request is
the application's business.

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
handler and the validator. `TokenURL` is what `GET /auth/server` tells clients
and has to be an absolute URL.

[examples/quickstart](examples/quickstart/main.go) runs a VTN, a Business
Logic client and a VEN through the program → event → report workflow in one
process:

```sh
go run ./examples/quickstart
```

[examples/vtn](examples/vtn/main.go) is a standalone VTN to point other
clients at.

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
- **Logging.** The library is silent. The server logs internal errors to
  the `slog.Logger` it is given, if any.

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
make interop  # against the OpenLEADR and ElaadNL reference images (needs Docker, see INTEROP.md)
```

## License

MIT, see [LICENSE](LICENSE).
