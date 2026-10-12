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
| Subscriptions | all five | 501 | no | no |
| Notifiers | `GET /notifiers` and the 12 MQTT topic operations | 501 | no | no |

"n/a" marks operations the specification's scopes do not give that role.

Also not implemented: webhook and MQTT notification delivery, persistent
storage, gzip, mDNS discovery of local VTNs, a VTN mode without
authentication, and the retention of properties the schema does not define
(they are ignored, as the specification allows).

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

**Date-times are RFC 3339.** The Definition mentions `0001-01-01T00:00:00`,
without an offset, as a start that may mean "now"; the schema says RFC 3339,
and that form is rejected.

**`/auth/token` errors carry both forms.** The OpenAPI document gives the
`problem` schema for errors of `/auth/token`, while its unreferenced
`authError` schema describes the RFC 6749 body. `auth.Issuer` returns a body
with the members of both.

**`read_bl`.** Six MQTT topic operations require a scope `read_bl` that the
document does not declare. Not relevant until notifiers are implemented;
it will be treated as `read_all`.
