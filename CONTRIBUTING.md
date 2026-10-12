# Contributing to go-openadr

Thank you for your interest in contributing to `go-openadr`! This document covers the guidelines for contributing.

## Getting Started

1. Fork the repository on GitHub
2. Clone your fork locally:
   ```sh
   git clone git@github.com:<your-username>/go-openadr.git
   cd go-openadr
   ```
3. Create a feature branch:
   ```sh
   git checkout -b feature/my-change
   ```

## Development Setup

### Prerequisites

- Go 1.23+ (see [go.mod](go.mod)); the code must build and pass on Go 1.23, so do not use newer language or standard-library features
- [staticcheck](https://staticcheck.dev/), [golangci-lint](https://golangci-lint.run/) and [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) for the checks
- No external VTN: tests run the clients against the VTN of this module over loopback HTTP
- Docker, for `make interop` only

### Build & Test

```sh
# Run all checks (format, lint, vet, vulnerability scan, tests, coverage, examples)
make check

# Run the library tests with the race detector
make test

# Run the parser fuzz targets (FUZZTIME=30s by default)
make fuzz

# Build the examples into ./bin
make build

# Run the interop suite against the OpenLEADR and ElaadNL reference images (needs Docker)
make interop
```

Run `make` (or `make help`) for the full list of targets.

## Making Changes

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Start every Go file with `// SPDX-License-Identifier: MIT`
- No dependencies outside the standard library
- Property names of the specification appear in error messages and wire structures as the specification writes them (`programName`, `venID`); everything else follows Go naming
- Respect the package boundaries in [ARCHITECTURE.md](ARCHITECTURE.md): the root package knows neither HTTP nor JSON, the JSON of a revision stays in `internal/wire`, and `client` and `server` do not import each other

### Formatting

`make fmt` formats with the `gofmt` of the Go version in `go.mod`, which is the version CI uses, not necessarily the one you have installed. `make fmt-check` (part of `make check`) fails if either that `gofmt` or your local one would change a file. If they disagree, restructure the code (for example, move trailing comments onto their own lines) rather than picking a side.

### Tests

- A change to what goes on the wire needs a test in `internal/wire/v310` against JSON written from the specification, not from the encoder's own output
- A change to who may see or write what needs a case in the end-to-end tests (`e2e_test.go`) for the client that must be refused as well as the one that is served
- Tests must pass with `-race` and must not depend on timing beyond generous upper bounds
- A change to the client, the VTN or the wire format must keep `make interop` green (the Interop workflow runs it on every push). The `interop` package holds this repository's scenarios and assertions; the reference implementations belong to [otfabric/openadr-interop](https://github.com/otfabric/openadr-interop) and are consumed as images only
- Where the specification leaves room and the VTN makes a choice, the choice is written down in [docs/CONFORMANCE.md](docs/CONFORMANCE.md) with the test that pins it

### Documentation

Update the document that owns the topic: [API.md](API.md) for the behavioural contract, [ERRORS.md](ERRORS.md) for error behaviour, [docs/CONFORMANCE.md](docs/CONFORMANCE.md) for protocol coverage and readings of the specification, [INTEROP.md](INTEROP.md) for what was tested against what, and add an entry to [RELEASE.md](RELEASE.md).

`RELEASE.md` is read by the release automation: the newest release comes first, each release starts with `## vX.Y.Z` followed by `**Date:**` and `**Previous release:**`, and releases are separated by a line holding `---`.

## Submitting

1. Run `make check`
2. Commit with a message that says what changed and why
3. Open a pull request against `main` describing the change and how it was tested

## License

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
