# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-openadr`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.com**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-openadr/security) tab → **Advisories** → **Report a vulnerability**), with:

- A description of the vulnerability
- Steps to reproduce the issue, ideally a minimal program or test
- The affected version(s)
- Any potential impact you've identified

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | Best effort |

## Security Considerations

### What OpenADR messages do

Events carry prices and limits that equipment acts on, and reports carry metering data. Whoever can write events to a VTN can move load; whoever can read reports learns how a site uses energy.

### Transport: TLS is the embedding program's job

OpenADR 3.1 requires HTTPS. The library speaks HTTP over whatever it is given and configures no TLS itself:

- **Client:** pass an `http.Client` with the TLS settings you need in `client.Config.HTTPClient`; the default client uses Go's defaults and the system's trust store. The client does not refuse an `http://` URL: do not give it one outside tests, since the bearer token and the client secret travel in the clear
- **VTN:** `server.Server` is an `http.Handler`. Serve it with `http.Server.ListenAndServeTLS` or behind a proxy that terminates TLS. `examples/vtn` serves plain HTTP and is for local use only

### Client credentials and tokens

- The client sends its secret only to the token endpoint. That endpoint is either configured (`Credentials.TokenURL`) or named by the VTN (`GET /auth/server`). A VTN reached over HTTPS cannot name a cleartext token endpoint: the client refuses and sends nothing. A VTN can still name any HTTPS endpoint, so configure `TokenURL` when the VTN is not trusted to choose it
- Tokens are kept in memory only, and neither tokens nor secrets are logged or put in error messages
- `auth.Issuer` is a token service for tests, examples and small deployments: clients and their secrets are held in memory in the clear, tokens are opaque random values lost on restart, and there is no rate limiting of failed attempts. Put an identity provider behind an `auth.Validator` for anything else

### Authorization in the VTN

- Every operation requires the scope the specification assigns it. A token with `read_all` is treated as Business Logic and may read everything
- A VEN reads, changes and deletes only the reports, VENs and resources that carry its own client ID, which the VTN writes from the token and a VEN cannot set. Another client's object answers 404, so that IDs cannot be probed
- Programs and events with targets are shown to a VEN only for targets Business Logic has granted it, and only those targets are shown. Only Business Logic can set targets on VENs and resources
- The VTN trusts its `auth.Validator` completely: the client ID and scopes it returns decide everything above. A validator that accepts tokens it should not is a hole no check here can close
- [docs/CONFORMANCE.md](docs/CONFORMANCE.md) lists where these rules are a reading of text the specification leaves open

### Input handling

- Request bodies are limited in size (`server.Config.MaxBodyBytes`, 1 MiB by default) and checked against the schema before they reach the store; response bodies are limited in the client (`client.Config.MaxResponseBytes`, 16 MiB by default)
- Object IDs are checked against the pattern of the specification before they are used in a URL
- Internal errors are logged, when a logger is configured, and answered with a bare 500: no detail of the failure reaches the client
- The decoders are fuzzed

### What is not there

- **No webhook delivery.** Subscriptions are not implemented, so the VTN makes no outbound requests. When they are added, callback URLs become an SSRF surface; the specification's rules for it (HTTPS only, proof of ownership of the URL, no redirects, no private addresses) are part of that work
- **No rate limiting and no audit log.** Put them in front of the handler
- **No persistence**, so no data at rest to protect: `storage.Memory` is lost when the process stops
