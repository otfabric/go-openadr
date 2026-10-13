# go-openadr Releases

## v0.4.0

**Date:** 2026-10-13
**Previous release:** v0.3.0

## Summary

Three things the VTN left to others until now, each behind a field of its
configuration that is off by default: webhook notifications that survive a
restart, a hook for rate limiting that answers 429 with `Retry-After`, and
notifications of reads. And the VTN's side of what goes beyond reading and
writing objects has met another implementation's client: the ElaadNL
client manages subscriptions on it and gets a webhook delivered, listens on
its MQTT topics, groups resources, reads without a token, takes compressed
answers and talks to it over TLS.

**Upgrade notes**

- Nothing changes for a VTN that sets none of the new fields.
- `server.Config` has new fields (`Outbox`, `NotificationTTL`, `RedeliverEvery`, `Limiter`, `NotifyReads`). Code that builds it with unkeyed fields has to be adjusted.
- A VTN with an `Outbox` has background work of its own: call `Server.Close` when it stops, as a VTN with WebSockets already had to.
- A store of your own does not have to change: `storage.Outbox` is an interface beside `storage.Store`, not a part of it. `storagetest.Run` tests it when your store implements it.
- The reference images are those of openadr-interop v0.4.0 (contract 1.3), pinned by digest. The four tests of the VTN against the ElaadNL client's new operations need them, and skip on an older image given by environment variable.

## Changes

### Added

- **Durable webhook notifications** — `server.Config.Outbox`. The VTN records a notification before it hands it to the sender and forgets it when the webhook has confirmed it. What is recorded and not on its way is sent again when the VTN starts and every `RedeliverEvery` (a minute), until it is delivered, the webhook refuses it for good, its subscription is deleted or `NotificationTTL` (a day) has passed. One callback URL gets one notification at a time, oldest first, so the order holds across failures and restarts. `storage.Outbox` is three methods; `storage.Memory`, `storage.File` (in its journal) and the SQLite and PostgreSQL examples implement it.
- **`webhook.Sender.SendConfirmed`** — `Send` with a function that is told what became of the notification: delivered, refused for good (`*webhook.RefusedError`), or given up on for now. `server.ConfirmingSender` is the interface the outbox asks for.
- **Rate limiting hook** — `server.Config.Limiter`, one method, asked for every request of a client the VTN has identified. A refusal is answered 429 with a problem object and `Retry-After`, which this module's clients honour when they have a retry policy. `server.NewRateLimiter(perSecond, burst)` is a token bucket per client ID.
- **Notifications of `READ`** — `server.Config.NotifyReads`. A subscriber to the operation `READ` is told of every object of that type that is read, by ID or in a search, as it may see it; by webhook and over WebSockets. OpenADR 3.1.0 lists the operation and does not say what it means, so it is off by default.
- **`examples/vtn`**: `-rate` and `-notify-reads`, and with `-store` and `-webhooks` together the file is the outbox too.
- **Interoperability, the VTN's side** (`interop/elaadnl_client_test.go`, `interop/tls_test.go`): see Verification.

### Changed

- The README's comparison has rows for durable webhooks, notifications of reads and rate limiting, and says what the ElaadNL client's subscriptions are on a 3.1.0 VTN. `INTEROP.md` has the new tests, what they showed, and a shorter list of what is not covered. `docs/CONFORMANCE.md`, `API.md`, `ERRORS.md`, `OBSERVABILITY.md`, `SECURITY.md` and `ARCHITECTURE.md` say what is there now.
- The interop harness starts this module's VTN with options (TLS, a configuration, a wrapper around the handler), and skips a test whose operation the reference client of the image does not have, saying which.

### Verification

- `make check`: coverage of 91.6% of the library's statements, seven self-contained examples.
- Unit tests of the token bucket (burst, refill, one client not holding up another, the wait it names) and of the hook; of `SendConfirmed` for a delivery, a final refusal and one given up on.
- `make e2e`: 48 end-to-end tests. New: a VTN on a journal file that is stopped as a crash stops it, with notifications for a webhook that is down, and whose successor delivers them all, in order; a webhook that refuses for good, a subscription deleted while its notifications wait, and one that expires; subscribers to `READ` told of reads by ID and by search, each as it may see the object, and nobody told without `NotifyReads`; a client that asks too much answered 429 with `Retry-After` while another is not held up, and a client with a retry policy that waits as told without its caller noticing.
- `storagetest.Run` covers the outbox for the four stores: memory, journal file, SQLite, PostgreSQL (`make storage`), including what a reopened store still holds.
- **The ElaadNL openadr3-client 2.4.0 against this module's VTN**, beyond objects:
  - over **TLS**, with a certificate of an authority it was given, token request and API calls; without the authority it sends nothing;
  - **compressed** answers: it asks for gzip and reads what it gets;
  - **without a token** on a VTN with `AllowAnonymous`: it reads what is open, and gets 404 and 401 for the rest;
  - **subscriptions**: it creates, reads, searches, updates and deletes one, another VEN does not see it, and when the ElaadNL Business Logic client deletes an event the notification arrives at the callback URL of the subscription, with its bearer token;
  - **MQTT**: it reads the broker and the topics of ten scopes from the VTN, and its MQTT client receives on a Mosquitto broker what the VTN publishes, as Business Logic and as a VEN on its own topic;
  - **resource groups**: create, read, search by name and target, update, delete, a group of groups, and what a VEN sees of them.
  The last four need operations the reference client has from openadr-interop v0.4.0.
- `make interop` is green against the reference images of openadr-interop v0.4.0 (OpenLEADR v0.2.7, ElaadNL openadr3-client 2.4.0), pinned by digest, and the four MQTT brokers; everything of v0.3.0 passes unchanged on them.
- `make chaos`, five rounds. The library's tests on Linux with Go 1.23 and Go 1.26, in containers, with the race detector; the ElaadNL tests also from a Linux container with host networking, as CI runs them.

### Known limitations

- **Durable means webhooks.** Notifications by MQTT and over WebSockets are not kept: a broker has its own persistence, and a WebSocket client searches after it reconnects.
- **At least once, not exactly once.** A VTN that stops between a webhook's answer and the removal of the record sends that notification again. A webhook has to take a notification twice.
- **One VTN per outbox.** Two VTN processes on one database would each send what is recorded: the outbox has no claim on a delivery.
- **The rate limiter knows clients, not addresses.** It is asked after the token was checked; what arrives without a valid token never reaches it and is for a gateway to limit. `NewRateLimiter` keeps its buckets in memory, per process.
- **What the ElaadNL client cannot do on a 3.1.0 VTN** (this one or OpenLEADR's), all pinned down by tests: subscribe to anything but `DELETE`, because it names the operations as OpenADR 3.0.1 did; read `GET /notifiers` of a VTN without an MQTT broker; read the topics of one program or ven. See `INTEROP.md`.
- **No other implementation receives this VTN's webhooks or WebSockets**: neither reference has a webhook receiver or a WebSocket client. The webhook in the test above is received by this module's handler.
- MQTT with a login or over TLS has met no other implementation's client: the ElaadNL MQTT client connects without credentials.

---

## v0.3.0

**Date:** 2026-10-13
**Previous release:** v0.2.0

## Summary

What a library is expected to give a program that runs in production, in
line with go-modbus and go-opcua: errors that can be told apart without
reading their text, hooks for logging and for metrics on both sides, and a
retry policy for the clients. And the storage interface gets what it takes
to bring your own: a public way to serialize objects, a test suite for a
store, and two stores on a database as examples, SQLite and PostgreSQL, in
a module of their own so that this one still depends on nothing.

**Upgrade notes**

- Nothing is retried, logged or counted that was not before: every new behaviour is behind a field of a configuration that is nil or zero by default.
- **The text of some errors changed**, because they now wrap a sentinel: an error of a constructor begins with "openadr: invalid configuration:", an answer that cannot be decoded with "openadr: protocol error:", a call that ran out of time with "openadr: timeout:". Code that compared error strings has to use `errors.Is`; code that already did is unaffected, and `errors.As` finds the same types as before.
- A failed discovery of the token endpoint (`GET /auth/server` refused) now wraps an `*openadr.Error`, where it was a plain error.
- `openadr.Error` has a new field, `RetryAfter`, and `client.Config`, `server.Config`, `webhook.SenderConfig`, `mqtt.PublisherConfig`, `mqtt.SubscriberConfig` and `websocket.SubscriberConfig` have new fields. Code that builds these with unkeyed fields has to be adjusted.
- The webhook sender now waits as long as a webhook's `Retry-After` says, up to `MaxBackoff`, where it waited its own backoff only.

## Changes

### Added

- **Error sentinels** — every failure matches a category with `errors.Is`. New in package `openadr`: `ErrTooManyRequests` (429), `ErrServer` (any 5xx), `ErrNotImplemented` (501), `ErrUnavailable` (502, 503, 504), `ErrInvalid` (every `*ValidationError`, and what `timeline` refuses), `ErrConfiguration` (what a constructor refuses), `ErrTimeout`, `ErrProtocol` (an answer that is not what the protocol says) and `ErrClosed`. Also `storage.ErrReadOnly`, `discovery.ErrNoNetwork` and `discovery.ErrInvalidAdvertisement`. A `*client.TokenError` matches `ErrUnauthorized`, `ErrTooManyRequests` or `ErrServer` by its status.
- **`openadr.Error.RetryAfter`** — how long a VTN, or a gateway before it, asked the client to wait.
- **Retry policy for the clients** — `client.Config.RetryPolicy`, with `NoRetry` (the default), `ExponentialBackoff` and `NewExponentialBackoff` (backoff with jitter, a limit on attempts, optionally retrying timeouts), or an implementation of the one-method interface. The client decides what may be retried at all, and a policy cannot widen it: failures another attempt can cure (no answer, 429, 502, 503, 504), and requests that are safe to send twice. A create is repeated only when it certainly did not happen, unless `Config.RetryCreates`. `Retry-After` is honoured, the call's context bounds everything, and a token that cannot be had is retried like the call it was for.
- **Metrics hooks** — interfaces of a few callbacks, with no metrics library behind them. `client.Metrics` (and the optional `client.AttemptMetrics`) per call of a client; `server.Metrics` per request of a VTN, with the optional `server.NotificationMetrics` and `server.SocketMetrics`; `webhook.Metrics`, `mqtt.Metrics` and `websocket.Metrics` for what becomes of notifications and connections. Operations are named by route (`GET /programs/{id}`), a fixed set that is fit to be a label.
- **Logging on the client** — `client.Config.Logger`: every call at debug level, a token renewed after a 401 at info, every retry at warn. And on the VTN, one debug line per request. Never bodies, tokens or secrets, and operations, not paths.
- **For a store of your own** — `storage.Encode` and `storage.Decode` turn any object a store keeps into JSON and back, in a form that is stable across releases; the domain types have no JSON form of their own, so until now a store outside the module had no way to serialize them. `storagetest.Run` (`server/storage/storagetest`) is the suite the stores of this module pass, for yours.
- **The VTN on SQLite and on PostgreSQL** (`examples/storage`) — `storage.Store` implemented on `database/sql`, opened on a SQLite file or a PostgreSQL server, with a runnable VTN for each. **A module of its own**: the database drivers are in `examples/storage/go.mod` and nowhere else.
- **`webhook.SenderConfig.Metrics`**, and the sender honours a webhook's `Retry-After`.
- **`interop/mdnsprobe`** — a few lines around `discovery.Advertise` and `discovery.Browse`, to put them on a network beside another implementation.
- **`OBSERVABILITY.md`**, and `examples/observability`: metrics and logs of a client and a VTN, a retry policy at work against a gateway that fails, failures told apart by `errors.Is`.
- **Metrics are not behind what a client has seen.** The opening of a WebSocket is reported (`OnResponse`, `OnSocketOpened`) before the handshake is answered, a notification (`OnNotification`) before it is handed to the sender, the broker or the WebSocket, and a subscriber's `OnConnected` before its `Ready` returns. A reading taken right after an effect was observed includes it.
- `make storage`, `make nodeps` (part of `make check`: fails when `go.mod` requires anything), and a CI job for both.

### Changed

- The comparison in the README has rows for metrics hooks, client retries and error categories, the storage rows say what is there now, and its lower half lists what this module has that the others do not show: brokers and mDNS implementations tested against, failure injection, fuzzing, the authorization matrix. The README has the badges of the other OT Fabric libraries, and links to the specification.
- `PLAN.md`: the SQL store and the metrics hooks are no longer open.

### Fixed

- **`discovery.Browse` did not find responders that answer only by multicast**, which OpenLEADR's does. It asked as a one-shot resolver and waited for a direct answer, which Avahi and Apple's mDNSResponder give. It now also listens and asks on the Multicast DNS port, where that can be shared. Found by the new test against OpenLEADR.

### Verification

- `make check`: as before, with coverage of 91.8% of the library's statements and seven self-contained examples.
- Unit tests of the retry rules as a table (which method is repeated after which failure, with and without `RetryCreates`), of `Retry-After` in both its forms, of the backoff with and without jitter, of timeouts by the context and by the HTTP client, of the metrics and the log of a client call by call, and of every sentinel against every status.
- `make e2e`: 43 end-to-end tests. New: sixteen clients opening WebSockets, reading and closing at once, each checking after every step that the VTN's metrics already include what it has just seen; a client with a retry policy on a network that cuts a third of its connections, whose calls succeed without its caller doing anything and whose creates end in exactly one object each; both ends of a WebSocket reporting the same to their metrics; the sentinels across packages.
- `storagetest.Run` against four stores: memory, journal file, SQLite and PostgreSQL. It covers the rules of a store, the order of a search, concurrent writers, every property of every kind of object through a write and a read, and a restart.
- `make storage`: beside the suite, a VTN on each database driven by the module's clients across a restart, and for PostgreSQL two stores on one database, as two VTN processes would be. PostgreSQL 18 in a container; SQLite through `modernc.org/sqlite`.
- The three example programs (`vtn`, `tariff`, `ven`) against the VTN of `examples/storage` on both databases, by hand: a tariff published, the VTN restarted, the prices read and reported on.
- **Discovery against Avahi and OpenLEADR**, in both directions each, on a container network (`interop/mdns_test.go`, part of `make interop`): Avahi 0.8 finds and resolves what this module announces, with every key of the TXT record, and this module finds what Avahi publishes; this module finds the VTN OpenLEADR v0.2.7 announces, and OpenLEADR's client library finds the one this module announces. The OpenLEADR half needs a reference image built with OpenLEADR's mDNS, which openadr-interop has from v0.3.0.
- `make interop` is green against the reference images of openadr-interop v0.3.0 (OpenLEADR v0.2.7 with mDNS, ElaadNL openadr3-client 2.4.0), pinned by digest, and the four MQTT brokers; everything of v0.2.0 passes unchanged on them.
- The whole suite on Linux with Go 1.23 and Go 1.26, in containers.

### Known limitations

- **The stores in `examples/storage` are examples.** They serialize writes, have no migrations, and are not covered by the compatibility promises of the module. They need Go 1.26, which the SQLite driver decides.
- A retried create of an object without a unique name (an event, a report, a subscription) can leave two, which is why `RetryCreates` is off by default and why the policy never repeats a create whose fate is not known without it.
- Operations are named from the path of a request, so an object whose ID is a word of the API (`events`, `vens`) is named wrongly in the client's metrics. The VTN's metrics use its routes and are not affected.
- The VTN does not send `Retry-After` itself, and does no rate limiting: both belong to a gateway before it.
- No ready-made adapters for Prometheus or OpenTelemetry: the interfaces are a few lines to implement, and an adapter would be a dependency.
- Unchanged: notifications are not durable, and the VTN's side of the extensions has met no other implementation's client.

---

## v0.2.0

**Date:** 2026-10-13
**Previous release:** v0.1.0

## Summary

The gaps the comparison with OpenLEADR and the ElaadNL client showed are
closed, and the module is put under load and failure. A VTN can serve
clients that do not authenticate, compress its answers and announce itself
on the local network; intervals can be read as a table; and the two things
OpenLEADR has beyond the specification, resource groups and notifications
over a WebSocket, are here too, off unless asked for and tested against
OpenLEADR. The test suite now runs many clients at once and injects
failures: a store that fails, connections that are cut, webhooks and a
broker that do not answer, tokens that expire, a journal cut mid-write.

Still no dependencies outside the standard library: the WebSocket framing
and the Multicast DNS are this module's own, like its MQTT client.

**Upgrade notes**

- `storage.Store` has a new method, `ResourceGroups`. An implementation of the interface outside this module has to add it, also for a VTN that does not serve resource groups; it may return a collection that is always empty. `storage.Memory` and `storage.File` have it, and a journal written by v0.1.0 opens unchanged.
- The VTN now compresses answers of 1 KiB and more, with gzip or deflate, for clients that ask for it, which Go's HTTP client and most others do without being told. `server.Config.DisableCompression` turns it off.
- `openadr.ObjectOperation` has a new field, `Mechanism`, and `openadr.Notification` a new field, `ID`. Code that builds these with unkeyed fields has to be adjusted; the zero value of both means what it meant before.
- A subscription to a webhook is unchanged on the wire. One by another mechanism is written without `callbackUrl`.
- A VTN with `Config.WebSocket` has connections of its own: call `Server.Close` when it stops, before shutting down the HTTP server.
- Everything new is off by default except compression. A v0.1.0 configuration gives the same VTN.

## Changes

### Added

- **A VTN without authentication** — `server.Config.AllowAnonymous` answers requests that carry no token, as the specification provides for public information such as price lists. Such a client reads the programs and events that have no targets and nothing else; every other operation answers 401, and a request with a token is validated as before. With it, `Validator` and `TokenURL` may be left out, for a VTN that authenticates nobody. A client without `Credentials` sends no token, as it already did.
- **Response compression** — gzip or deflate for answers of 1 KiB and more, negotiated with `Accept-Encoding` and its weights, with `Vary` set. These are the two codings the standard library can write; br and zstd, which OpenLEADR also has, would each need a dependency.
- **MQTT on four brokers** (`interop/brokers_test.go`, part of `make interop`) — see Verification.
- **Discovery of local VTNs** (`discovery`) — DNS-SD over Multicast DNS as the specification describes it. `Advertise` announces a VTN as `_openadr3._tcp` with the TXT record of the specification (`version`, `base_path`, `local_url`, `program_names`, `requires_auth`, `openapi_url`), answers queries, and says goodbye when closed. `Browse` finds the VTNs on the local network and returns their name, host, port, addresses and TXT record; `VTN.URL` is where its API is.
- **Intervals as tables** (`timeline`) — `OfEvent` and `Of` lay out the intervals of an event or a report as rows with a start and an end, applying the timing rules of the User Guide: defaults of the event, intervals that follow each other or have a start of their own, a start that means "now", `P9999Y`, payloads that pack several values into sub-intervals, and an event duration that cuts the intervals short or repeats them. `Table.At` answers what applies at a time, `Table.Intervals` makes intervals of rows again, and `WriteCSV` and `ReadCSV` exchange a table with a spreadsheet.
- **Resource groups** — `openadr.ResourceGroup`, served at `/resource_groups` by a VTN with `server.Config.ResourceGroups`, and `ResourceGroups`, `ResourceGroup`, `CreateResourceGroup`, `UpdateResourceGroup` and `DeleteResourceGroup` on the clients. Business Logic writes them and reads all; a VEN reads the groups that lead to a resource of its own, and of their children only those. **Not part of OpenADR 3.1.0**: proposed for a later revision, and implemented as OpenLEADR implements them.
- **Notifications over a WebSocket** — a subscription entry can name `openadr.MechanismWebSocket` in place of a callback URL; a VTN with `server.Config.WebSocket` serves `GET /notifiers/ws` and sends each client what such entries select, as that client may see it. `(*VEN).NotificationStream` and `(*BL).NotificationStream` open the stream, and `notification/websocket.Subscriber` keeps one open, with keep-alive and reconnecting. `Notifiers.WebSocket` says whether a VTN has it. **Not part of OpenADR 3.1.0**: an extension of OpenLEADR, which this module shares.
- **`Server.Close`** — ends the WebSockets of a VTN.
- **`Notification.ID`** — the ID OpenLEADR gives the operation a notification is about, when a VTN sends one.
- **`webhook.SenderConfig.MaxBackoff`** — the longest wait between two attempts to deliver a notification; one minute by default.
- **Parallel and failure-injection tests** (`e2e/chaos_test.go`, `make chaos`) — see Verification.
- **Examples** — `prices` (a public tariff, a client without credentials, the prices as a table), `websocket`, `resourcegroups` and `discovery`, each self-contained and run in CI; `ven`, a VEN for the command line that finds a VTN, registers, prints its schedule, reports and watches; and `tariff`, Business Logic that publishes a schedule from a CSV file and reads the reports on it. Together with `examples/vtn` they are the three roles of an OpenADR system as three programs, which the README now introduces. `examples/vtn` takes `-anonymous`, `-websocket`, `-resource-groups` and `-advertise`.

### Changed

- The start of an interval period is accepted as `0001-01-01` and as `0001-01-01T00:00:00`, the two ways the User Guide writes "now", which are not date-times of the schema. v0.1.0 rejected them.
- `storage.Store` gained `ResourceGroups()`. A resource that is deleted leaves the resource groups that held it.
- `GET /notifiers` and subscriptions are served when a VTN has any way to notify: a webhook sender, an MQTT publisher or WebSockets. A subscription that asks for a mechanism the VTN has not is refused with 400.
- `callbackUrl` is left out of a subscription entry that is not to a webhook.
- The README's comparison with OpenLEADR and the ElaadNL client is brought up to date, and corrected in one row: OpenLEADR has subscriptions, which it delivers over WebSockets.
- API.md, ERRORS.md, SECURITY.md, ARCHITECTURE.md, INTEROP.md, docs/CONFORMANCE.md, PLAN.md and CONTRIBUTING.md are revised for all of the above.

### Fixed

- **The webhook sender's wait between attempts grew without bound.** It doubled after every failed attempt; with a large `Attempts` a notification could wait for hours behind a webhook that had long recovered, and after some thirty attempts the wait overflowed. It is now capped (`MaxBackoff`). With the default of four attempts nothing changes. Found by the new failure-injection test of webhooks.

- **Closing an MQTT connection could report it as lost.** A broker hangs up when it is told goodbye; when the client noticed that before it had finished closing, the connection ended with "connection lost" where it had been closed, and a publish after `Close` returned that error in place of the one for a closed connection. Seen on Linux only, and rarely.

### Verification

- `make check`: formatting for Go 1.23 and the local toolchain, staticcheck, golangci-lint, vet, govulncheck, all tests with the race detector, coverage of 91.8% of the library's statements, and the six self-contained examples.
- `make e2e`: 39 end-to-end tests of this module's clients against this module's VTN over HTTPS. Every feature above has one, through the public API of both sides. One of them builds and runs the three example programs as processes: a 24-hour tariff is published, the VTN is restarted and still has it, the VEN reads the prices and reports, and the publisher reads the report. The authorization matrix has 54 rows, the seven new ones for resource groups and WebSockets, each as five kinds of client.
- **Parallel and failure injection**, with the race detector: twelve clients at once on every kind of object, on the memory and the journal store, with the store checked for consistency after and every notification for order; a store that fails one call in five, before or after doing it (clients are told 500 and nothing of the cause, nobody is notified of a change whose request failed, the store stays consistent, everything works after); a network that cuts a third of the connections mid-request (every create is retried to exactly one object; the WebSocket subscriber reconnects); a webhook that answers 500, hangs, hangs up or loses its answer (every notification arrives, in order, while the API does not wait); a broker that drops every connection every 15 ms; tokens that expire while eight goroutines use them; and a journal cut at 44 different bytes, each of which opens to a consistent store that takes writes.
- Unit tests of the WebSocket protocol against frames written out by hand (fragments, control frames between them, masking in both directions, every violation and its close code); of the Multicast DNS codec against a compressed response as other implementations write one, and of both halves of `discovery` against each other on a network in memory, with loss, off-link queries, one-shot and multicast queriers; of `timeline` with a case per timing rule.
- The whole suite also on Linux, in containers with Go 1.23 and Go 1.26, the parallel and failure-injection tests four times over on each: they must not depend on the speed of the machine, and two of them did until this was done.
- Six fuzz targets, three of them new: WebSocket frames, DNS messages and CSV tables, beside durations, events and MQTT packets.
- `make interop` is green against the reference images of openadr-interop v0.2.0 (OpenLEADR v0.2.7, ElaadNL openadr3-client 2.4.0), pinned by digest: everything of v0.1.0, and new, against OpenLEADR, **resource groups** (create, nest, read, update, delete as Business Logic; visibility as two VENs) and **WebSocket notifications** (this module's subscriber receives the create, update and delete of an event, and nothing it may not see).
- **The MQTT client on brokers other people wrote**, which until now it had met only as a subscriber on Mosquitto: this module's VTN publishes to, and this module's subscriber receives from, Eclipse Mosquitto 2.1.2, NanoMQ 0.25.6, HiveMQ Community Edition 2026.5 and EMQX 6.3.1, each in a container as its maker ships it. On each: create, update and delete in order, to Business Logic with every target and to a VEN with its own; nothing on a VEN's topic that is not for it; a burst of 200 without loss or reordering; an event of a thousand intervals; and a restart of the broker, after which publisher and subscriber are both back. All four passed without a change to the client.
- `discovery` against the Multicast DNS responder of macOS, by hand, in both directions: the system's browser finds and resolves what `Advertise` announces, and `Browse` finds what the system registers.
- The conformance suite of openadr-interop v0.2.0: 24 of 24 against the VTN, as before, also with every new option turned on.

### Known limitations

- **The VTN's side of the new features has met no other implementation's client**: WebSockets, resource groups, anonymous access and compression are tested from this module's own clients, and, for the first two, this module's clients against OpenLEADR's VTN. The reference client of openadr-interop has no operations for them yet.
- **Resource groups and WebSocket notifications are not OpenADR 3.1.0** and may change: the first with the revision of the specification that adopts them, the second with OpenLEADR, which calls it experimental.
- A WebSocket is authenticated when it is opened and not again: a client whose token is revoked receives until it disconnects or the VTN restarts. Nothing is kept for a client that is not connected, and one that does not read fast enough is disconnected.
- An OpenLEADR VTN allows one WebSocket per client and frees the place of a closed one only when it next fails to send to it: a client that reconnects to it may be refused for a while. The subscriber keeps trying.
- A notification is lost when the store fails while the VTN looks up whom to tell. The request that caused it has succeeded.
- `discovery` speaks Multicast DNS over IPv4 only, does not probe for names that are already taken, and has been tried with one other responder, Apple's. Multicast DNS is not authenticated: a discovered VTN is an address to try, and the TLS certificate decides.
- `AllowAnonymous` makes every program and event without targets public. There is no rate limiting in the VTN.
- `timeline` cannot tell from an event whether several values in a payload are sub-intervals or belong together: the caller says (`Options.SubIntervals`). `RandomizeStart` is carried, not applied.
- Nobody is notified of changes to resource groups.
- The brokers are tested without TLS and without a login. With a login the MQTT client has met Mosquitto, in the OpenLEADR image; with TLS, this module's own test broker only.
- Unchanged from v0.1.0: webhooks have not been tested against another implementation, and the VTN's MQTT notifications have been read by this module's own subscriber only; notifications are not durable; no SQL store.

---

## v0.1.0

**Date:** 2026-10-13
**Previous release:** v0.0.1

## Summary

The rest of OpenADR 3.1.0: subscriptions, notifications by webhook and by
MQTT on both sides, and the MQTT topic operations, which brings the VTN to
all 45 operations of the specification. With it a validator for JSON Web
Tokens, a store that survives a restart, and an end-to-end test suite of
this module's clients against this module's VTN.

**Upgrade notes**

- `storage.Store` has a new method, `Subscriptions`. An implementation of the interface outside this module has to add it; `storage.Memory` and the new `storage.File` have it.
- `server.Config.TokenURL` was already required to be an absolute URL in v0.0.1; nothing else in a v0.0.1 configuration changes meaning. A VTN that is given neither `Webhooks` nor `MQTT` answers 501 for subscriptions and notifiers, as before.

## Changes

### Added

- **Subscriptions** — `openadr.Subscription`, `ObjectOperation` and `Notification`, and on both clients `Subscriptions`, `Subscription`, `CreateSubscription`, `UpdateSubscription` and `DeleteSubscription`. In the VTN: all five operations, with the `programID`, `clientName` and `objects` filters.
- **Webhook notifications** (`notification/webhook`) — `Sender` delivers a VTN's notifications: in order per callback URL, with retries and backoff, with the subscription's bearer token. By default it accepts https URLs only, follows no redirect and connects to public addresses only, checked on the address actually connected to. `Handler` is the receiving side: it answers the VTN's verification request, checks the bearer token and decodes the notification.
- **Callback verification** — before the VTN accepts a subscription it sends `GET <callbackUrl>?echo=<random>` and requires the value back, as the specification asks. A URL that fails is refused with 400.
- **MQTT notifications** (`notification/mqtt`) — `Publisher` sends a VTN's notifications to a broker at QoS 1, in order, reconnecting as needed; `Subscriber` receives them in a client and resubscribes after a lost connection. Both use this module's own MQTT 3.1.1 client: no dependency was added.
- **Notifier discovery** — `GET /notifiers` and the 12 MQTT topic operations in the VTN; `Notifiers` and `MQTTTopics` on both clients. Topics that carry every object of a kind are given to Business Logic; a VEN is given the topics of its own ven object, on which the VTN publishes what that VEN may see.
- **Who is told what** — by webhook, a subscriber is notified of what a search of its own would show it; by MQTT, each ven's topics carry what its VEN may read by ID. In both, a VEN is shown only the targets it was granted.
- **`auth.JWTValidator`** — validates JSON Web Tokens of an identity provider: RS256/384/512, ES256/384/512, EdDSA and HS256/384/512, with static keys or the provider's key set (JWKS, fetched on demand and rate-limited), and checks of expiry, `nbf`, issuer and audience. The kind of key decides the algorithms it verifies.
- **`storage.File`** (`storage.OpenFile`) — a store that survives a restart: everything in memory, every change appended to a journal file and synced before the write returns, the journal replayed and compacted at start. For a standalone VTN of modest size.
- **End-to-end tests** (`e2e/`, `make e2e`) — this module's clients against this module's VTN over HTTPS: the workflow, roles and ownership, targeting, paging, webhooks, MQTT with a broker in the test, JWTs, a restart on the journal store, and a matrix of every operation as five kinds of client.
- **Examples** — `examples/notifications`: a VEN that subscribes and is told of a new event. `examples/vtn` takes `-webhooks`, `-insecure-webhooks`, `-mqtt` and `-store`.

### Changed

- `storage.Store` gained `Subscriptions()`, which stores `storage.SubscriptionRecord`: the subscription, and whether the subscriber was Business Logic when it subscribed.
- A `server.Server` that is given `Webhooks` or `MQTT` causes background activity: the sender and the publisher have goroutines of their own, and the embedding program closes them on shutdown.
- The README has a comparison with OpenLEADR and the ElaadNL client.

### Verification

- `make e2e`: 16 end-to-end tests, among them an authorization matrix of 47 operations, each as no client, a client with a forged token, the owning VEN, another VEN and Business Logic.
- Unit tests of the webhook sender's address rules, ordering, retries and shutdown; of the MQTT client against packet bytes written out from the MQTT 3.1.1 standard, and against connection loss; of the JWT validator against forged tokens (no signature, `alg: none`, a public key used as an HMAC secret, wrong key, changed payload, wrong issuer and audience); of the journal store against restart, a crash in the middle of a write, corruption and a disk that stops taking writes.
- Three fuzz targets: the duration parser, the event decoder and the MQTT packet reader.
- `make interop` is green against the reference images of openadr-interop v0.2.0 (OpenLEADR v0.2.7 with a Mosquitto broker, ElaadNL openadr3-client 2.4.0), pinned by digest: everything of v0.0.1, and MQTT notifications from OpenLEADR. The VEN finds the broker and its topics through OpenLEADR's API, and this module's subscriber receives the create, update and delete of an event on Mosquitto.
- The conformance suite of openadr-interop v0.2.0, with its authorization scenarios: 24 of 24 against the VTN.

### Known limitations

- **Webhooks, and the VTN's side of MQTT, have not been tested against another implementation.** OpenLEADR has no webhook subscriptions, and the reference client cannot subscribe yet. The MQTT publisher has met this module's test broker only; the subscriber is tested on Mosquitto with OpenLEADR's notifications.
- Notifications are delivered at least once while the VTN runs, and are not durable: what is queued when the process stops is lost. A client that must not miss a change still polls, and does so after a reconnect.
- The VTN decides what it publishes on which MQTT topic; who may read a topic is for the broker's access control, which this module does not configure.
- No notification for `READ`, which the schema lists as an operation.
- `storage.File` is not a database: one process, all objects in memory. A SQL store is not included: the pure-Go SQLite driver needs a newer Go than this module supports and would end its freedom from dependencies. It is to follow as a separate module.
- What a webhook subscriber may be told is decided by its role when it subscribed, not by its token at the time of the notification.

---

## v0.0.1

**Date:** 2026-10-12
**Previous release:** none

## Summary

First release: an OpenADR 3.1.0 VEN client and Business Logic client
(`client`) and a VTN as an embeddable `http.Handler` (`server`) in pure Go,
on a shared domain model. Programs, events, reports, VENs and resources,
with OAuth2 client credentials, scopes, object ownership and targeting. No
dependencies outside the standard library; Go 1.23 or later.

The clients are tested against the OpenLEADR VTN v0.2.7, and the VTN with
the ElaadNL openadr3-client 2.4.0, through the reference images of
[otfabric/openadr-interop](https://github.com/otfabric/openadr-interop)
v0.1.0.

Subscriptions, webhooks and MQTT notifications are not implemented, and the
only store is in memory. The API is not frozen: until v1.0.0 a minor release
may change it.

## Changes

### Added

- **`openadr`** (the module root) — the domain model: `Program`, `Event`, `Report`, `VEN`, `Resource`, intervals, payload descriptors and report descriptors, independent of any one revision's JSON. `Value` holds a number, string, boolean or point. `Duration` is an ISO 8601 duration as the specification's pattern defines it, with calendar arithmetic (`AddTo`) and conversion to and from `time.Duration`. `Validate` on every object checks it against the 3.1.0 schema.
- **`client`** — `VEN` and `BL`, the two roles of the specification, each with the operations its scopes allow: searching and reading everything they may see, and creating, updating and deleting programs and events (BL), reports (VEN), VENs and resources (both). `context.Context` on every call; `Each` iterates over a whole search result page by page.
- **OAuth2 in the client** — the client-credentials grant, token endpoint discovery through `GET /auth/server`, a token cache that renews shortly before expiry, and one retry with a fresh token when the VTN answers 401. A VTN reached over HTTPS cannot redirect the client secret to a cleartext token endpoint. `TokenSource` plugs in any other way of getting a token.
- **`server`** — the VTN: every operation on programs, events, reports, VENs and resources, with filters and paging, the scope checks of the specification, ownership of VEN-created objects, and targeting with target hiding. Errors are problem objects. `GET /auth/server` and an optional `POST /auth/token`.
- **`auth`** — `Validator`, the interface through which the VTN resolves a bearer token to a client ID and scopes, so that it works with any identity provider; the scopes of OpenADR 3.1.0; and `Issuer`, an in-memory client-credentials token service for tests, examples and small deployments.
- **`server/storage`** — `Store`, the persistence boundary of the VTN, which enforces name uniqueness and references atomically, and `Memory`, the in-process implementation.
- **Errors** — `*openadr.Error` for a VTN's error answer, matching `ErrBadRequest`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound` and `ErrConflict` through `errors.Is`; `*openadr.ValidationError` for an object that violates the schema, raised before anything is sent; `*client.TokenError` for a refused token request. See [ERRORS.md](ERRORS.md).
- **Examples** — `examples/quickstart` runs a VTN, a Business Logic client and a VEN through the program → event → report workflow in one process; `examples/vtn` is a standalone VTN.
- **Version strategy** — the JSON of a revision lives in `internal/wire/<revision>` behind one codec interface, so that OpenADR 3.0.1 can be added without a second public model. See [ARCHITECTURE.md](ARCHITECTURE.md).

### Verification

- Unit and end-to-end tests with the race detector; 86% statement coverage of the library packages.
- Fuzz targets for the duration parser and the event decoder.
- `make interop`: the clients against the OpenLEADR VTN v0.2.7 (workflow, token handling, errors, VENs, resources, targeting), and the ElaadNL openadr3-client 2.4.0 against the VTN (34 read operations and the workflow, checked in the VTN's store). See [INTEROP.md](INTEROP.md).
- The conformance suite of openadr-interop, which asserts the specification's OpenAPI document: 21 of 21 scenarios against the VTN.

Two faults were found by these runs and fixed before the release: the client rejected `ven` objects without `objectType`, which OpenLEADR sends, and the VTN announced a relative token URL, which the ElaadNL client cannot use and the schema does not allow.

### Known limitations

- Subscriptions and the notifier endpoints answer 501: 27 of the 45 operations of OpenADR 3.1.0 are implemented. No webhook or MQTT notification delivery.
- The only `Store` is in memory: a VTN loses its objects when it stops.
- OpenADR 3.1.0 only; no 3.0.x.
- No gzip, no mDNS discovery of local VTNs, no VTN mode without authentication.
- Properties the schema does not define are ignored, not kept.
- Where the specification leaves room, the VTN has made choices that another implementation may make differently: see [docs/CONFORMANCE.md](docs/CONFORMANCE.md).
- Verified against OpenLEADR and the ElaadNL client only. Not certified by the OpenADR Alliance.

---
