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

### A VTN without authentication

`server.Config.AllowAnonymous` answers requests that carry no token, as the specification allows for public information. Know what it opens before turning it on:

- **Everything without targets becomes public**: every program and event that has no targets can be read by anyone who can reach the VTN, with all its payloads. A program that was only "not targeted yet" is public the moment the switch is on
- Nothing else is opened. A client without a token has no client ID: it cannot read reports, vens, resources, subscriptions or resource groups, cannot write anything, cannot open a WebSocket, and names targets to no effect. These answer 401
- A request with a token is validated as always. A wrong token is not downgraded to anonymous access
- There is no rate limiting: an open VTN is as exposed as any public web server. Put a proxy in front of it
- Leaving `Validator` out as well makes a VTN on which nobody can authenticate, and so nobody can write through the API: such a VTN is filled through its `Store`

### Compression

Answers are compressed only on request and never mix a secret with text an attacker chooses in one answer in the way the attacks on compressed TLS need: the VTN does not echo request input into the bodies it compresses, and problem answers, which name what was wrong with a request, are not compressed. Turn it off with `Config.DisableCompression` if your threat model wants none.

### Discovery on the local network

Multicast DNS is not authenticated. `discovery.Browse` returns whatever answers on the link, and anyone on the link can answer:

- **What is found is an address, not a VTN to trust.** A VEN that takes its VTN from discovery must still verify the TLS certificate of what it connects to, against a certificate authority it was given, and should not send its client secret to a VTN it cannot verify. The specification lets a VEN be configured to accept unverified certificates for local gateways with self-signed ones; with discovery, that combination gives the VEN's credentials and its obedience to whoever announces first
- `requires_auth=False` in an announcement is a claim too. A VEN that expects to authenticate should not stop because an announcement says it need not
- `discovery.Advertise` tells the local network that a VTN is there, its host name, port, path, version and the program names it is given. Do not announce what the network should not know
- The package parses packets from the network. The parser is bounded and fuzzed, takes no more than 9000 bytes per packet, and answers only queries from the subnet of the interface they arrive on

### WebSockets

`server.Config.WebSocket` lets clients receive notifications on a connection they open:

- The opening handshake is an API call like any other: it needs a bearer token, in the `Authorization` header. A browser cannot set that header on a WebSocket, so a web page cannot open one with a user's credentials; there is no cookie to ride on, and so no origin check
- **The token is checked when the connection is opened, and not again.** A connection outlives the token it was opened with, and a client whose token was revoked keeps receiving until it disconnects or the VTN restarts. What each notification shows is still decided when it is sent, by the rules for the subscriber. Close the VTN's WebSockets (`Server.Close`), or restart it, to cut off a client
- Bounded: connections per client (`MaxPerClient`), notifications queued per connection (`Queue`), the size of a message a client may send (64 KiB), and the silence of a client that does not answer pings
- Use `wss`, which is to say: serve the VTN over HTTPS. A proxy in front has to pass the upgrade through, and its idle timeout has to be longer than `PingInterval`
- The WebSocket implementation is this module's own, like the MQTT client. It is fuzzed, tested against the framing rules of RFC 6455, and has met one other implementation, OpenLEADR's

### Resource groups

Business Logic writes them; a VEN reads the groups that hold a resource of its own. A VEN is shown the name, attributes and targets of such a group, and not the other members: the IDs of other clients' resources, and of groups that do not lead to one of its own, are left out. Do not put in the name or attributes of a group what its members should not read.

### Webhooks: the VTN makes requests to URLs its clients choose

A VTN with subscriptions sends HTTP requests to callback URLs that clients name. That is a server-side request forgery surface: a client could name an address inside the VTN's own network. `webhook.Sender` is built for it, and its defaults are the safe ones:

- **https only**, and no credentials in the URL
- **Public addresses only.** The check is made on the address the connection actually goes to, after name resolution, so a name that resolves to a loopback, private, link-local (the cloud metadata address among them) or shared address is refused, also when it resolves differently the second time
- **No redirects.** A 3xx is a failed delivery; the bearer token is never sent anywhere but the URL of the subscription
- **No proxy** from the environment, which would make the connection on the sender's behalf
- **Proof before use.** A subscription is accepted only after the URL has echoed a random challenge, so a client cannot aim the VTN's notifications at a server that does not expect them
- **Nothing learned from failures.** What a client is told about a refused URL does not include what the VTN's network answered
- Bounded: a timeout per request, a fixed number of attempts, a longest wait between them, bounded queues

`SenderConfig.AllowInsecure` turns the scheme and address rules off and is for tests and closed networks. A custom `SenderConfig.HTTPClient` replaces the default client and its address rule entirely: the rules are then yours to keep.

What remains with the operator: a client with `write_subscriptions` can make the VTN send requests to any public https server that echoes a challenge, at the rate objects change. Rate-limit subscriptions if that matters.

**On the receiving side**, a `webhook.Handler` without a `BearerToken` accepts notifications from anyone who can reach it, and a client that acts on events would act on forged ones. Set a token made up for the subscription, serve the handler over https, and treat a notification as a prompt to read from the VTN when the consequence of a forged one is serious. The subscription's bearer token is stored by the VTN and returned to the subscriber and to Business Logic when they read the subscription.

### MQTT: the broker decides who reads what

With `server.Config.MQTT` the VTN publishes notifications to a broker you run. The VTN decides *what goes on which topic*; it cannot decide *who subscribes*:

- Topics that carry every object of a kind (`programs/...`, `events/...`, `reports/...`, `vens/...`, `resources/...`, `subscriptions/...`) carry them as Business Logic sees them, all targets included. **The broker must allow only Business Logic to read them**
- Topics below `vens/{venID}/` carry what that VEN may see. **The broker must allow only that VEN to read them**: on a broker that lets every client subscribe to `#`, every VEN reads every other VEN's events
- Only the VTN should be allowed to publish
- The topic operations of the VTN tell a client only the topics it is entitled to, but a topic name is no secret: ven IDs are in the objects
- Use `mqtts://`, and give the VTN's publisher its own account. `GET /notifiers` announces how clients are to log in (`MQTTConfig.Authentication`); announcing `ANONYMOUS` on a broker with the rules above cannot work

Configuring the broker is outside this module, and nothing here checks that it was done.

The MQTT client in this module is its own implementation of MQTT 3.1.1. It is fuzzed, tested against the wire format of the standard, and tested on Eclipse Mosquitto, NanoMQ, HiveMQ Community Edition and EMQX, without TLS and without a login; with a login it has met Mosquitto only, and with TLS no broker but this module's own test broker.

### Tokens from an identity provider

`auth.JWTValidator` verifies the signature before it reads any claim, binds each key to the algorithms of its kind (so a public key cannot be passed off as an HMAC secret), refuses unsigned tokens, and requires an expiry. What it cannot know is your provider:

- **Set `Audiences`** when the provider also issues tokens for other services. Otherwise any of its tokens, for any service, is accepted here
- **Set `Issuer`** when the key set is shared between issuers
- Fetch the key set over https. A key set fetched in the clear lets whoever is on the path choose the keys
- The scopes in the token are believed: who gets `read_all` or `write_events` is decided at the provider

### Stored data

`storage.File` keeps the VTN's objects in one file, in the clear, including the bearer tokens of subscriptions. It is created with mode 0600. Protect the directory and the backups accordingly.

The same holds for a store on a database, such as those in `examples/storage`: the documents it keeps include the bearer tokens of subscriptions. Those examples are examples: they are not covered by the support of this module, and their database drivers are theirs to keep up to date.

### Logs and metrics

Nothing is logged or counted unless a logger or a metrics implementation is configured. When one is:

- Bodies, bearer tokens, client secrets and the query of a callback URL are never logged
- A debug line of a call or request names the operation (`GET /events/{id}`), not the path. An error line names the path, which holds an object ID; object IDs are not secrets in OpenADR, but they are in your logs then
- The labels given to metrics are operations, statuses, mechanisms and object types: a fixed set that a client cannot widen. A request to a path the VTN does not have is counted as `other`
- An error passed to a metrics callback is the error the caller gets, with its path and the VTN's detail. Label by category (`errors.Is`), do not use its text as a label

### Retries

A retry policy makes a client send a request again. The client only does so where a second request cannot do twice what was asked once, and waits as a VTN's `Retry-After` says. `Config.RetryCreates` lifts the first of those for creates: read [OBSERVABILITY.md](OBSERVABILITY.md#retries) before setting it. A policy without a limit on attempts, against a VTN that is down, is a client that never stops: give calls a context with a deadline.

### Input handling

- Request bodies are limited in size (`server.Config.MaxBodyBytes`, 1 MiB by default) and checked against the schema before they reach the store; response bodies are limited in the client (`client.Config.MaxResponseBytes`, 16 MiB by default)
- Object IDs are checked against the pattern of the specification before they are used in a URL
- Internal errors are logged, when a logger is configured, and answered with a bare 500: no detail of the failure reaches the client
- The decoders are fuzzed: of JSON objects, of ISO 8601 durations, of MQTT packets, of WebSocket frames, of DNS messages and of CSV tables
- `timeline` refuses to lay out a table of more than about a million rows, so that an event that repeats one-second intervals for years costs an error and not the memory

### What is not there

- **No rate limiting and no audit log.** Put them in front of the handler
