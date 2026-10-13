# Interoperability

What this module has been tested against, and how to repeat it. Nothing here
is a claim about hardware, vendors or certification: it is a record of
specific tests against specific versions.

## Matrix

| Client | VTN | Result | How |
|---|---|---|---|
| go-openadr VEN and BL | go-openadr | Passing, in CI; every feature | `make e2e` |
| go-openadr VEN and BL | [OpenLEADR](https://github.com/OpenLEADR/openleadr-rs) v0.2.7 (Rust) | Passing, run locally | `make interop` |
| [ElaadNL openadr3-client](https://github.com/ElaadNL/openadr3-client) 2.4.0 (Python), VEN and BL | go-openadr | Passing, run locally | `make interop` |
| ElaadNL client over TLS, and its answers compressed | go-openadr | Passing, run locally | `make interop` |
| ElaadNL client: subscriptions (webhook delivered), MQTT listener, resource groups, no token | go-openadr | Passing, run locally | `make interop` |
| go-openadr MQTT subscriber, on Mosquitto | OpenLEADR v0.2.7 | Passing, run locally | `make interop` |
| go-openadr VTN (MQTT publisher) and MQTT subscriber | on Eclipse Mosquitto 2.1.2, NanoMQ 0.25.6, HiveMQ CE 2026.5, EMQX 6.3.1 | Passing, run locally | `make interop` |
| go-openadr WebSocket subscriber | OpenLEADR v0.2.7 | Passing, run locally | `make interop` |
| go-openadr BL and VEN, resource groups | OpenLEADR v0.2.7 | Passing, run locally | `make interop` |
| go-openadr `discovery`, both directions | Avahi 0.8 | Passing, run locally | `make interop` |
| go-openadr `discovery`, both directions | OpenLEADR v0.2.7, VTN and client library | Passing, run locally | `make interop` |
| go-openadr `discovery`, both directions | Apple mDNSResponder (macOS) | Passing, by hand | see below |
| the specification-driven suite of openadr-interop v0.4.0 | go-openadr | 24 of 24 scenarios, run locally | see below |

Last run: 2026-10-13, on a developer machine (linux/arm64 containers on
macOS), against the published images of openadr-interop v0.4.0, pinned by
digest. The `Interop` workflow runs the same tests in CI on linux/amd64.

The reference implementations come from
[otfabric/openadr-interop](https://github.com/otfabric/openadr-interop) as
container images with one contract: a reference VTN that serves a fixture,
and a reference client that runs one operation and prints what it made of
the answer. That repository knows nothing about this module; the tests and
what they assert are here, in `interop/` (build tag `interop`).

## Running it

Needs Docker. The images are those of openadr-interop v0.4.0, pinned by
digest in the Makefile, and are pulled when they are not there:

```sh
make interop
```

`make interop` fails when an image cannot be had. Run directly with
`go test -tags=interop ./interop/...`, a test whose image is missing is
skipped, unless `OPENADR_INTEROP_REQUIRE` is set.
`OPENADR_INTEROP_OPENLEADR_IMAGE` and `OPENADR_INTEROP_ELAADNL_IMAGE` select
other images, for example the `:dev` tags that `make images` builds in a
checkout of openadr-interop.

## This module's clients against OpenLEADR

`interop/openleadr_test.go` starts the OpenLEADR reference VTN with the
baseline fixture of openadr-interop and drives it with `client.BL` and
`client.VEN`, as the fixture's clients:

- **Workflow.** Business Logic creates a program and an event with prices,
  a report request and every kind of payload value; the VEN finds the
  program by paging through all programs, reads the event, and creates,
  updates and deletes a report; Business Logic reads the report by program.
  Timestamps, durations, priorities, intervals and values are compared after
  the round trip.
- **Token handling.** Token endpoint discovery through `/auth/server` and
  the client-credentials grant against OpenLEADR's token service; a wrong
  secret surfaces as a `client.TokenError`.
- **Errors.** 404, 409 and 403 arrive as `openadr.ErrNotFound`,
  `ErrConflict` and `ErrForbidden`, with OpenLEADR's problem body decoded.
- **VENs, resources and targeting.** The fixture's vens, targets and events
  are visible to exactly the clients that should see them; a VEN client
  registers itself and a resource; Business Logic grants it a target and it
  then sees a program that carries it, while a VEN without the grant does not.

## MQTT notifications from OpenLEADR

`interop/mqtt_test.go` uses the MQTT broker in the OpenLEADR reference image
(the feature `serverMqtt`, from openadr-interop v0.2.0; with an older image
given by environment variable the test is skipped). The VEN client asks OpenLEADR where its broker is
(`Notifiers`) and for its topics (`MQTTTopics`), this module's
`mqtt.Subscriber` logs in to the Mosquitto in the image, and Business Logic
creates, updates and deletes an event: each arrives, decoded, on the topic
of all events and on the VEN's own.

One thing had to be worked around, and it is OpenLEADR's: the topics it
names for a ven (`events/vens/{id}/...`) are not the ones it publishes on
(`vens/{id}/events/...`). The test listens on both.

## MQTT on four brokers

`interop/brokers_test.go`. The MQTT client of this module is its own, so
the question is not only whether another VTN's notifications can be read,
but whether brokers other people wrote accept what it sends. For each of
Eclipse Mosquitto, NanoMQ, HiveMQ Community Edition and EMQX, the test
starts the broker in a container, as its maker ships it (Mosquitto with the
open configuration its image carries), and puts this module's VTN on it as
publisher and this module's `mqtt.Subscriber` on it twice, as a VEN and as
Business Logic, on the topics the VTN names for each:

- create, update and delete of an event arrive in that order, at Business
  Logic with all targets, at the VEN with the one it was granted;
- an event for another group does not appear on the VEN's topics;
- a burst of 200 events arrives complete and in order, at QoS 1;
- an event of a thousand intervals, about 60 kB, arrives whole;
- the broker is restarted: the subscriber connects and subscribes again,
  the publisher connects again, and what is published after arrives.

All four passed without a change to the client. The images are pinned by
digest in `interop/harness_test.go`; `OPENADR_INTEROP_MOSQUITTO_IMAGE`,
`..._NANOMQ_IMAGE`, `..._HIVEMQ_IMAGE` and `..._EMQX_IMAGE` select others.

What this does not show: TLS and logins on these brokers (the test connects
in the clear, without a password), their access control, which is what
keeps one VEN from reading another's topics (SECURITY.md), and another
implementation's client reading what this VTN publishes.

## WebSocket notifications from OpenLEADR

`interop/extensions_test.go`. WebSocket notifications are OpenLEADR's
extension, not OpenADR 3.1.0; this module shares it, and this is the test
that it is the same thing on both sides. The VEN client subscribes on
OpenLEADR with the mechanism `WEBSOCKET` and no callback URL, this module's
`websocket.Subscriber` opens `/notifiers/ws` with the client's token, and
Business Logic creates a program, creates, updates and deletes an event:
each arrives, decoded, with OpenLEADR's notification ID. A program targeted
at someone else does not arrive.

Two things about OpenLEADR that a client has to know, and that the test
pins down:

- it takes **one WebSocket per client**: a second is answered 409;
- it notices that a client has gone only when it **fails to send** to it.
  Until then the place stays taken, so a client that reconnects is answered
  409 until notifications that would have been its own have failed. This
  module's subscriber keeps trying, with its backoff.

## Resource groups on OpenLEADR

Same file. Resource groups are proposed for a later revision of OpenADR and
implemented by OpenLEADR; this module follows its representation. Business
Logic creates a group that holds the fixture's battery, with targets and
attributes, and a second group that holds the first; reads them back, by ID
and by name; updates one, which replaces its children; deletes one. The VEN
whose battery it is sees both groups, the outer one with the inner as its
only child; another VEN sees neither, and is refused the group by ID; a VEN
that tries to create a group is answered 403.

## Discovery: Avahi, OpenLEADR and Apple's mDNSResponder

Package `discovery` speaks Multicast DNS with code of its own, so it is
tested against the implementations a VEN or a VTN will meet on a network.
Multicast does not cross from a test process into a container network, so
this module's side runs in a container too: `interop/mdns_test.go` builds
`interop/mdnsprobe`, a few lines around `discovery.Advertise` and
`discovery.Browse`, for Linux and runs it on a container network of its own
beside the other implementation.

**Avahi** (0.8, as Alpine Linux 3.22 packages it), in both directions:

- this module announces a VTN, and `avahi-browse -r` finds and resolves it:
  instance name, host name, address, port and every key of the TXT record;
  `avahi-resolve` resolves the host name this module announced; and a
  browse of all service types lists `_openadr3._tcp`;
- Avahi publishes a VTN (`avahi-publish -s`), and `Browse` finds it, with
  host, port, addresses and TXT record;
- with both on the network, one `Browse` returns both.

**OpenLEADR** v0.2.7, in both directions. The reference image has OpenLEADR
built with its `mdns` feature since openadr-interop v0.3.0 (the feature
`serverMdns`), which is the one pinned here.

- OpenLEADR's VTN announces itself, and `Browse` finds it: host, port,
  address and its TXT record;
- this module announces a VTN, and OpenLEADR's client library
  (`discover_local_vtns`, run as its own example program) finds it by the
  `local_url` of its TXT record.

What OpenLEADR announces differs from the Definition in ways that needed no
change here but that a client should expect: `version` is `3.1`, not
`3.1.0`; `local_url` has the trailing dot of the host name
(`http://vtn.local.:8080/`); and there is no `requires_auth` and no
`program_names`. This module takes a VTN that does not say to require
authentication.

**Apple's mDNSResponder**, by hand, because it needs a Mac. With
`examples/discovery` or the `-advertise` flag of `examples/vtn`:

```sh
dns-sd -B _openadr3._tcp local.                     # finds what discovery.Advertise announces
dns-sd -L "<instance>" _openadr3._tcp local.        # and reads its host, port and TXT record
dns-sd -R "Some VTN" _openadr3._tcp local. 9443 version=3.1.0 base_path=oa3 requires_auth=False
                                                    # is found by discovery.Browse
```

Both directions worked on 2026-10-13. `go test ./discovery` with
`OPENADR_TEST_MDNS=1` checks this module's two halves against each other on
the machine's real sockets.

## The ElaadNL client against this module's VTN

`interop/elaadnl_test.go` runs `server.Server` in the test process with the
clients of the same fixture, loads the fixture's objects through the API,
and runs the ElaadNL reference client against it from its container:

- **Reads** (34 operations): searches and reads of programs, events, VENs,
  resources and reports as Business Logic and as three different VENs, with
  target, name, parent and paging filters. Each is checked for the client's
  exit code, error code, the HTTP status it saw and the number of objects it
  decoded — which also means the ElaadNL models accepted every object this
  VTN serialized. Errors are checked to arrive as problem objects.
- **Workflow.** The ElaadNL client creates a program, an event and a report,
  registers a VEN and a resource, has Business Logic grant a target, updates
  and deletes. After each step the test looks in the VTN's own store: the
  values that arrived, the client ID the VTN recorded, the targets.
- **Access rules** as the ElaadNL client experiences them: target hiding,
  another VEN's objects answered 404, a duplicate `venName` answered 409.

`interop/tls_test.go` does a part of that over HTTPS: the VTN has a
certificate of an authority made for the test, the client's container is
given that authority, and the token request and the API calls go over TLS.
Without the authority the client sends nothing and reports that it could
not connect, which shows that it checked.

`interop/elaadnl_client_test.go` is what the VTN offers beyond objects:

- **Compression.** The client asks for gzip, as the HTTP library under it
  does, the VTN answers a search with it, and the client reads all of it.
- **No token.** A VTN with `AllowAnonymous`, and the library's anonymous
  client: it reads the programs and events that are open to everyone, gets
  404 for a targeted program and 401 for vens and for a write. On a VTN
  that was not told to, the same client gets 401.
- **Subscriptions and the webhook they lead to.** The VEN client subscribes
  with a callback URL and a bearer token, reads its subscription back alone
  and in a search, changes and deletes it; another VEN does not see it. The
  Business Logic client then deletes an event, and the notification arrives
  at the callback URL with the token. The receiver there is this module's
  `webhook.Handler`: neither reference has one.
- **MQTT.** The client reads the broker and the topics of ten scopes from
  the VTN, and its MQTT client, on a Mosquitto broker, receives what the
  VTN publishes: as Business Logic every new program, and as a VEN, on its
  own topic, the events for a target it was granted and not those for
  another.
- **Resource groups.** Business Logic creates a group of a VEN's resource
  and a group of groups, reads, searches by name and by target, updates
  and deletes; the VEN whose resource it is sees the group, another does
  not; a VTN without the extension answers 404.

What that showed about the library, pinned down by the same tests so that a
change in either implementation is noticed:

- **It names the operations of a subscription as OpenADR 3.0.1 did**: `GET`,
  `POST`, `PUT`, `DELETE`, where 3.1.0 has `READ`, `CREATE`, `UPDATE`,
  `DELETE`. This VTN answers a subscription to `POST` with 400 and a problem
  that names the four it takes; a subscription to `CREATE` the library
  refuses to send; and a subscription this module's client made to `CREATE`
  the library cannot read. `DELETE` is the one name they share, which is why
  the webhook above is one for a deletion.
- **It requires `MQTT` in `GET /notifiers`**, which the specification leaves
  optional: the notifiers of this VTN without a broker are an answer it
  cannot decode.
- **It requires a `CREATE` topic everywhere.** The topics of one program
  have none, as the specification says, and the library cannot read them.
- Its Business Logic client reads subscriptions and cannot make one.

## What it took

Each of these was found by an interoperability run and changed this module.

- **`ven` objects without `objectType`** (OpenLEADR). The client used to
  reject them. The 3.1.0 schema for that property cannot be met as written
  (docs/CONFORMANCE.md), so the client now accepts any value there, or none.
- **The token endpoint must be an absolute URL** (ElaadNL). The VTN accepted
  a relative `TokenURL` such as `auth/token` and announced it as it was; the
  ElaadNL client hands the value to its OAuth2 library and failed. The schema
  says `format: uri`. `server.New` now requires an absolute URL.

- **A responder that answers only by multicast** (OpenLEADR). `Browse`
  asked as a one-shot resolver, from a port of its own, and waited for an
  answer to that port; Avahi and Apple answer such a query directly,
  OpenLEADR's responder answers to the whole network and nothing else, and
  was not found. `Browse` now also listens, and asks, on the Multicast DNS
  port.
- **A callback URL is absent for a WebSocket** (OpenLEADR). `callbackUrl`
  was written always, as 3.1.0 requires it; it is now left out when a
  subscription names another mechanism. For a webhook nothing changed.

Differences that needed no change: OpenLEADR writes durations in full
(`P0Y0M0DT1H0M0S` for `PT1H`) and timestamps with a `+00:00` offset and
microseconds, and allows one ven object per client. ElaadNL's client cannot
read a program that has a report payload descriptor, with any VTN; the
fixture used here has none. The findings about the reference implementations
themselves are in openadr-interop's docs/COMPATIBILITY.md.

## The specification-driven suite

openadr-interop also has a conformance suite that checks a VTN against the
specification's OpenAPI document. It is run against this module's VTN by
hand:

```sh
# here
go run ./examples/vtn -addr 127.0.0.1:8080 \
    -client utility:bl-secret:bl -client site-1:ven-secret:ven -client site-2:ven2-secret:ven

# in openadr-interop/conformance
OPENADR_INTEROP_BL_SECRET=bl-secret OPENADR_INTEROP_VEN_SECRET=ven-secret OPENADR_INTEROP_VEN2_SECRET=ven2-secret \
go run ./cmd/openadr-interop -url http://127.0.0.1:8080 \
    -bl-client-id utility -ven-client-id site-1 -ven2-client-id site-2 -target-name go-openadr
```

## Not covered

**A webhook receiver and a WebSocket client of another implementation.**
Neither reference has one: OpenLEADR does not deliver to webhooks and its
client library has no receiver, and the ElaadNL client has neither. So this
VTN's webhooks are received by this module's handler, also when another
implementation's client made the subscription, and its WebSockets are
tested between this module's two sides only (`e2e/`). **Subscriptions to
anything but `DELETE` with the ElaadNL client**, for the reason above.
**MQTT with a login or over TLS with another implementation's client**: the
ElaadNL MQTT client connects without credentials whatever the VTN says. A
client of this module without credentials against another VTN. TLS towards
the OpenLEADR reference, which serves plain HTTP on loopback. OpenADR 3.0.x. Any VTN other than OpenLEADR,
and any client other than ElaadNL's and the conformance suite.
