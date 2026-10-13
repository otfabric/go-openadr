# OpenADR 3.1.0 coverage

What this module implements of OpenADR 3.1.0 (document 20250807-X), and how
the VTN behaves where the specification leaves room. What has been tested
against other implementations is in [INTEROP.md](../INTEROP.md).

## Operations

| Resource | Operations | VTN | VEN client | BL client |
|---|---|:---:|:---:|:---:|
| Programs | search, read | yes | yes | yes |
| Programs | create, update, delete | yes | n/a | yes |
| Events | search, read | yes | yes | yes |
| Events | create, update, delete | yes | n/a | yes |
| Reports | search, read | yes | yes | yes |
| Reports | create, update, delete | yes | yes | n/a |
| VENs | search, read, create, update, delete | yes | yes | yes |
| Resources | search, read, create, update, delete | yes | yes | yes |
| Auth | `GET /auth/server` | yes | yes | yes |
| Auth | `POST /auth/token` (optional in the specification) | optional | yes | yes |
| Subscriptions | search, read, create, update, delete | yes, with a webhook sender or WebSockets | yes | yes |
| Notifiers | `GET /notifiers` | yes, with any binding | yes | yes |
| Notifiers | the 12 MQTT topic operations | yes, with an MQTT publisher | yes | yes |
| Notifications | by webhook | sender | handler | handler |
| Notifications | by MQTT | publisher | subscriber | subscriber |

"n/a" marks operations the specification's scopes do not give that role.

That is all 45 operations of the specification. A VTN that is given no way
to deliver notifications has no subscriptions, and one that is not given an
MQTT publisher has no topic operations: those answer 501.

Beside the operations, the Definition describes behaviour that has no
operation of its own:

| Behaviour | VTN | Clients |
|---|:---:|:---:|
| A VTN that does not require authentication ("Non-Authenticating VTNs") | optional (`AllowAnonymous`) | yes: a client without credentials sends no token |
| Discovery of local VTNs by mDNS and DNS-SD | `discovery.Advertise` | `discovery.Browse` |
| Event and interval timing (User Guide 7.3 and 7.4) | n/a: the VTN stores intervals as given | `timeline` |
| Compressed answers (HTTP, not the specification) | gzip, deflate | yes |

Not implemented: notifications of `READ`, and the retention of properties
the schema does not define (they are ignored, as the specification allows).

## Beyond the specification

Two things this module has are not in OpenADR 3.1.0. Both are off unless a
VTN is configured to have them, both follow OpenLEADR, and
[API.md](../API.md#beyond-openadr-310) has their wire forms.

| Extension | VTN | VEN client | BL client |
|---|:---:|:---:|:---:|
| Resource groups: `/resource_groups`, search, read | optional | yes | yes |
| Resource groups: create, update, delete | optional | n/a | yes |
| Notifications over a WebSocket: `GET /notifiers/ws`, and subscriptions with the mechanism `WEBSOCKET` | optional | yes | yes |

A VTN without them is a plain 3.1.0 VTN: the paths do not exist, the
answer to `GET /notifiers` has no `WEBSOCKET`, and a subscription that asks
for the mechanism is refused (400).

## Access rules

| Scope | Granted to | Lets the client |
|---|---|---|
| `read_all` | BL | read everything; also satisfies `read_targets` and `read_ven_objects` |
| `read_targets` | VEN | read programs and events, subject to targeting |
| `read_ven_objects` | VEN | read the reports, VENs and resources with its own client ID |
| `write_programs`, `write_events` | BL | write programs and events |
| `write_reports` | VEN | write its own reports |
| `write_vens` | BL, VEN | write VENs and resources; a VEN only its own, and never targets |

A client is treated as Business Logic when its token has `read_all`.

Resource groups have no scopes of their own in 3.1.0. Here: reading needs
`read_ven_objects` (or `read_all`), and shows a VEN the groups that lead to
one of its resources; writing needs `write_vens` and Business Logic.
OpenLEADR, which splits `write_vens` in two scopes, asks for its
Business Logic half.

`e2e/matrix_test.go` holds all of this as one table: every operation, as a
client without a token, with a forged one, as the VEN an object belongs to,
as another VEN, and as Business Logic.

## How the VTN reads the specification

The Definition leaves some behaviour open, and in places contradicts the
OpenAPI document. These are the choices made here.

**Targets match on any common target.** An object with targets `[a, b]`
matches a request or a grant that has `a`. The Definition says "target
matching" without defining it.

**A VEN that names no targets sees only untargeted programs and events.**
Following "Object Privacy": a VEN has to include matching targets in the
query, and only targets it was granted count. Named targets also work as a
filter, so a search with targets does not return untargeted objects; a VEN
that wants both makes two searches.

**Reading a program or event by ID uses the VEN's granted targets.** The
by-ID operations have no targets parameter, so the letter of the Definition
would hide every targeted object there. The VTN shows the object if any
granted target matches, and only the granted targets of it.

**An object a client may not see does not exist for it.** Reads, updates and
deletes of another client's report, VEN or resource answer 404, not 403.

**References are enforced, as 409.** An event needs an existing program, a
report an existing event, a resource an existing VEN. A violation answers
409, as the OpenAPI description of that response suggests ("a violation of a
foreign key constraint"). A program with events and a VEN with resources
cannot be deleted (409). Reports are not deleted with their event.

**`createVen` can answer 409.** The Definition requires `venName` to be
unique; the OpenAPI document defines no 409 for this one create operation.

**`ven` and `resource` objects have `objectType` `VEN` and `RESOURCE`.** The
schemas combine `objectMetadata` with the Business Logic request schema,
whose `objectType` is the request discriminator, so no value satisfies both.
The VTN follows `objectMetadata`. The clients accept whatever a VTN sends
there, including nothing: OpenLEADR v0.2.7 leaves `objectType` out of `ven`
objects. In a request the VTN requires the discriminator, as the request
schemas do.

**A Business Logic request from a VEN is refused (403).** A `BL_VEN_REQUEST`
or `BL_RESOURCE_REQUEST` carries a client ID and targets, which only
Business Logic may set. A VEN's own request never changes the targets of an
object.

**`GET /auth/server` names an absolute URL.** The schema says `format: uri`,
while the document's own `securitySchemes` uses the relative `auth/token`.
Clients of other implementations hand the value to an OAuth2 library as it
is, so the VTN requires an absolute `TokenURL`. This module's client also
resolves a relative one against the API's URL.

**`limit` defaults to its maximum, 50.** The specification gives a maximum
and no default.

**`active=true` is conservative.** An event counts as transpired only when
that can be computed from the event alone: it has a start, a finite
`duration` (or an `intervalPeriod.duration` to multiply by its intervals),
and no interval with a period of its own. Every other event is returned.

**Date-times are RFC 3339, with one exception.** The User Guide writes the
start that means "now", or "after the previous interval", as `0001-01-01`
and as `0001-01-01T00:00:00`. Neither is a date-time of the schema. Both
are accepted as the start of an interval period, and only there, and are
written back as `0001-01-01T00:00:00Z`, which is one. Every other date-time
without an offset is rejected.

**A request without a token is a client without a client ID.** The
Definition says a non-authenticating VTN answers "all incoming requests,
without the specified OAuth2 access token", for "public information". Here
that is a switch on a VTN that otherwise authenticates: a request without a
token may read programs and events, and since it has no client ID it was
granted no targets and owns nothing, so it sees what is not targeted and
nothing else. Operations that need another scope answer 401, which tells a
client that a token would help. A token that is sent is validated; a bad
one is a 401, not an anonymous request.

**`requires_auth` is announced as `True` or `False`**, capitalized as the
Definition writes it, and read without regard to case. A VTN that does not
announce it is taken to require authentication.

**A discovered VTN is addressed by `local_url`** when it announces one, and
otherwise by `https://`, its host name, its port and `base_path`. The
Definition's example announces port 883 for an HTTPS API; nothing here
assumes a port.

**Sub-intervals are a choice of the reader.** The User Guide says a payload
with several values "may" be a series of sub-intervals, and that some
payloads have several values by nature. Nothing in an event says which.
`timeline` splits when asked to (`Options.SubIntervals`), and then every
payload with more than one value except those that hold a point.

**An event's `duration` shorter than its intervals cuts them; longer, it
repeats them.** As the User Guide describes. A row that the duration ends
in is shortened, not dropped. Repetition is by the span of the intervals,
first start to last end.

**Answers are compressed with gzip or deflate on request.** The specification does not
mention it; it is plain HTTP content negotiation, and a client that does
not ask gets none.

**`/auth/token` errors carry both forms.** The OpenAPI document gives the
`problem` schema for errors of `/auth/token`, while its unreferenced
`authError` schema describes the RFC 6749 body. `auth.Issuer` returns a body
with the members of both.

**A subscriber is told of what its own search would show it.** The
Definition says to process targets for a notification "as described" for
reads, using the client ID of the subscription. Here: reports, VENs,
resources and subscriptions go to their own client and to Business Logic;
programs and events follow the rules for targets, with the subscription's
`targets` taken as the targets of a search, so a VEN's subscription without
targets is told of untargeted objects only. A subscription with a
`programID` is told of that program, its events and the reports on them.

**The role of a subscriber is the one it had when it subscribed.** A
notification is decided without a request, so the VTN remembers with the
subscription whether its client could read everything.

**Callback URLs are verified as the Definition requires, and more
strictly.** https only, and the echo challenge on create and on every URL an
update adds. In addition the default sender connects to public addresses
only and follows no redirect, which the Definition recommends. A refused URL
answers 400.

**`READ` is never notified of.** The schema lists it as an operation; the
VTN accepts it in a subscription and sends nothing for it.

**`GET /notifiers` needs a token and no particular scope.** The document
assigns it `read_all`, which a VEN does not have, while a VEN is who needs
to know how it will be notified.

**Delivery is at least once and in order per callback URL, and not
durable.** The Definition leaves retries to the VTN. A notification is also
lost when the VTN cannot read its subscriptions at the moment it would
send it.

**A subscription says how it wants to be notified, per entry.** 3.1.0 has
webhooks only, and `callbackUrl` is required. With the extension, an entry
of `objectOperations` may name the mechanism `WEBSOCKET` and then has no
`callbackUrl`; without a mechanism, or with `WEBHOOK`, it is a 3.1.0 entry.
A client is sent over its WebSocket what its `WEBSOCKET` entries select and
nothing else. (OpenLEADR sends whatever any subscription of the client
selects.)

**MQTT topics follow who may read what.** The Definition leaves topic names
to the VTN. Here, below an optional prefix: `programs/<op>`,
`programs/{id}/<op>`, `programs/{id}/events/<op>`, `events/<op>`,
`reports/<op>`, `subscriptions/<op>`, `vens/<op>`, `resources/<op>` carry
every object as Business Logic sees it; `vens/{id}/<op>`,
`vens/{id}/programs/<op>`, `vens/{id}/events/<op>` and
`vens/{id}/resources/<op>` carry what that VEN may see, with the targets it
was granted. `<op>` is `create`, `update` or `delete`, and `ALL` is the
filter `.../+`.

**On a ven's topics, targeted objects need no naming.** A search needs the
VEN to name its targets; a topic has no request to name them in. A ven's
program and event topics carry untargeted objects and those with a target
the VEN was granted, as a read by ID does.

**The topic operations for all objects of a kind need `read_all`.** The
document asks for `read_all` on some and for `read_bl`, a scope it does not
declare, on others. Both are read here as "Business Logic". The operations
for one ven need `read_ven_objects` and a ven of the client's own.

**The message of a topic is the notification object**, as for a webhook.

**The broker is not the VTN's.** The VTN publishes; who may subscribe to
which topic is the broker's access control (SECURITY.md).
