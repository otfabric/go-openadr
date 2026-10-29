# Errors: otfabric/go-openadr

How go-openadr reports failures. For the rest of the behavioural contract see [API.md](API.md).

## Table of contents

- [Quick reference](#quick-reference)
- [Answers of the VTN (`*openadr.Error`)](#answers-of-the-vtn-openadrerror)
- [Invalid objects (`*openadr.ValidationError`)](#invalid-objects-openadrvalidationerror)
- [Tokens (`*client.TokenError`)](#tokens-clienttokenerror)
- [Transport, context and timeouts](#transport-context-and-timeouts)
- [Retries](#retries)
- [Notification streams](#notification-streams)
- [Tables (`timeline`)](#tables-timeline)
- [Discovery](#discovery)
- [In the VTN](#in-the-vtn)
- [What is not an error](#what-is-not-an-error)
- [Detection patterns](#detection-patterns)

## Quick reference

Errors a client method can return:

| Situation | Error | Was a request sent? |
|-----------|-------|---------------------|
| The VTN answered 400 | `*openadr.Error`, matches `openadr.ErrBadRequest` | yes |
| The VTN answered 401, also with a fresh token; or the client has no credentials and the operation needs a token | `*openadr.Error`, matches `openadr.ErrUnauthorized` | yes |
| The VTN answered 403: the token lacks the scope, or the client may not do this | `*openadr.Error`, matches `openadr.ErrForbidden` | yes |
| The VTN answered 404: no such object, or none the client may see | `*openadr.Error`, matches `openadr.ErrNotFound` | yes |
| The VTN answered 409: a name is taken, a referenced object is missing, or dependents exist | `*openadr.Error`, matches `openadr.ErrConflict` | yes |
| The VTN, or a gateway before it, answered 429 | `*openadr.Error`, matches `openadr.ErrTooManyRequests`; `RetryAfter` says how long to wait, if the answer did | yes; nothing was done |
| The VTN answered 501: it does not have the operation | `*openadr.Error`, matches `openadr.ErrNotImplemented` and `openadr.ErrServer` | yes |
| A gateway answered 502 or 504, or the VTN 503 | `*openadr.Error`, matches `openadr.ErrUnavailable` and `openadr.ErrServer` | yes; with 503 nothing was done |
| The VTN answered with any other 5xx | `*openadr.Error`, matches `openadr.ErrServer` | yes |
| The VTN answered with another status outside 2xx (405, 413, 426) | `*openadr.Error`; matches no sentinel | yes |
| The object, or an object ID, violates the schema | `*openadr.ValidationError`, matches `openadr.ErrInvalid` | no |
| The object, or a search, has what the client's revision of OpenADR cannot carry: string targets or a 3.0.1 report's `ProgramID` and a 3.1.0 VTN, typed targets, an event's `Duration` and a 3.0.1 VTN | `*openadr.ValidationError`, matches `openadr.ErrInvalid` | no |
| The operation is not in the client's revision: notifiers, MQTT topics, resource groups, or a resource by ID alone, with OpenADR 3.0.1 | matches `errors.ErrUnsupported` | no |
| The token endpoint refused the client | wraps `*client.TokenError`, which matches `openadr.ErrUnauthorized` | no API request |
| The token endpoint failed (429, 5xx) | wraps `*client.TokenError`, which matches `openadr.ErrTooManyRequests` or `openadr.ErrServer` | no API request |
| The token endpoint could not be discovered: `GET /auth/server` was refused | wraps an `*openadr.Error` for that request | no API request |
| The token endpoint answered nonsense, or the VTN named one that is not https | matches `openadr.ErrProtocol` | no API request |
| No connection, TLS failure, connection lost | the error of `net/http` (`*url.Error`) | unknown |
| The call ran out of time: the context's deadline, the HTTP client's timeout, a network timeout | matches `openadr.ErrTimeout`, and `context.DeadlineExceeded` where that is what happened | unknown |
| The context was cancelled | matches `context.Canceled` | unknown |
| The answer could not be decoded | matches `openadr.ErrProtocol`, and wraps a `*openadr.ValidationError` when one property is at fault | yes, and the VTN acted on it |
| The answer is larger than `Config.MaxResponseBytes` | matches `openadr.ErrProtocol` | yes, and the VTN acted on it |

Always test with `errors.Is` and `errors.As`, never by comparing strings:
the text of an error is for people, and changes.

### The sentinels

All in package `openadr`, unless a package is named.

| Sentinel | Matched by |
|---|---|
| `ErrBadRequest`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`, `ErrConflict`, `ErrTooManyRequests` | an `*Error` with status 400, 401, 403, 404, 409, 429; `ErrNotFound` and `ErrConflict` also by what a store returns, and `ErrUnauthorized` by a `*client.TokenError` for credentials that are refused |
| `ErrServer` | an `*Error` with any 5xx |
| `ErrNotImplemented`, `ErrUnavailable` | an `*Error` with 501; with 502, 503 or 504. Both also match `ErrServer` |
| `ErrInvalid` | every `*ValidationError`; and what `timeline` returns for intervals or a CSV file that cannot be laid out or read |
| `ErrConfiguration` | what a constructor returns for a configuration it cannot work with: `client.NewVEN`, `client.NewBL`, `server.New`, `auth.NewIssuer`, `auth.NewJWTValidator`, `mqtt.NewPublisher`, `mqtt.NewSubscriber`, `websocket.NewSubscriber` |
| `ErrTimeout` | a call of a client that ran out of time |
| `ErrProtocol` | an answer that is not what the protocol says it is |
| `ErrClosed` | the use of something after it was closed: `Next` on a closed notification stream, `Ready` on a closed subscriber, a write to a closed `storage.File`; and what the webhook sender reports for a notification it gave up on because it was closed |
| `auth.ErrInvalidToken` | what a `Validator` returns for a token that is unknown, malformed or expired |
| `storage.ErrCorrupt` | `storage.OpenFile` on a journal it cannot replay |
| `storage.ErrReadOnly` | every write to a `storage.File` after one the journal could not take |
| `timeline.ErrTooLarge` | a table of more than about a million rows |
| `discovery.ErrNoNetwork`, `discovery.ErrInvalidAdvertisement` | `Browse` and `Advertise` where Multicast DNS cannot be spoken; `Advertise` for an advertisement that cannot be announced. (`discovery` shares no errors with the rest: it imports nothing of the module.) |

## Answers of the VTN (`*openadr.Error`)

```go
type Error struct {
	Method     string  // of the request
	Path       string  // of the request, such as "/programs/abc"
	StatusCode int     // of the response
	Problem    Problem // the body, when it was a problem object
}
```

Any response outside 2xx becomes an `*openadr.Error`. `Problem` holds what the
VTN said (`Title`, `Status`, `Detail`, `Type`, `Instance`); it is zero when
the body was not a problem object, for instance an HTML page of a proxy.

`errors.Is` matches by HTTP status:

| Status | Sentinel |
|--------|----------|
| 400 | `openadr.ErrBadRequest` |
| 401 | `openadr.ErrUnauthorized` |
| 403 | `openadr.ErrForbidden` |
| 404 | `openadr.ErrNotFound` |
| 409 | `openadr.ErrConflict` |

Other statuses match no sentinel: use `errors.As` and `StatusCode`.

What a status means is the VTN's business. The VTN of this module answers
404 for an object the client may not see, and 409 for a duplicate name, a
missing parent and a delete that dependents prevent; another VTN may differ
where the specification allows (OpenLEADR, for one, answers 403 to a token it
did not issue).

## Invalid objects (`*openadr.ValidationError`)

```go
type ValidationError struct {
	Field  string // "intervals[0].payloads[1].type", in the naming of the specification
	Reason string
}
```

Returned by the `Validate` methods, and by every client method that sends an
object, before anything is sent. Also for an object ID that is not one (an
empty ID, or characters outside `[a-zA-Z0-9_-]`), and for an update of an
object without an ID.

The same type reports a message that cannot be decoded: a missing required
property, a wrong JSON type, a duration that is not ISO 8601. For a client
that means the *VTN's answer* was invalid; the error is then wrapped with
"decoding the response", and the request has taken effect on the VTN.

Validation checks the schema, not the meaning: an event for a program that
does not exist is valid here and a conflict at the VTN.

## Tokens (`*client.TokenError`)

```go
type TokenError struct {
	StatusCode  int    // of the token endpoint's response
	Code        string // RFC 6749 error code, such as "invalid_client"
	Description string
}
```

Returned, wrapped, when the token endpoint answers a token request with
anything but 200. `Code` and `Description` are empty when the endpoint did
not answer with an OAuth2 error body.

Other failures on the way to a token are plain errors with a message that
names the step: discovery (`GET /auth/server` failed, or named a cleartext
endpoint for a VTN reached over HTTPS), or a 200 without a usable bearer
token. No API request is made in any of these cases.

## Transport, context and timeouts

- Errors of the HTTP exchange are returned as `net/http` produces them. Whether the VTN received the request is then unknown.
- Every method takes a context that bounds the whole call, including a token request it may trigger. `context.Canceled` and `context.DeadlineExceeded` are preserved for `errors.Is`.
- The default `http.Client` has a 30 second timeout per HTTP exchange. A client passed in `Config.HTTPClient` brings its own.

## Retries

The client retries one thing: a request the VTN answered with 401 is sent
once more with a fresh token, when the token came from `Credentials` (or
from a `TokenSource` that has an `Invalidate` method). A VTN that answers 401
has not acted on the request, so this is safe for every method, writes
included.

Nothing else is retried unless the client is given a policy
(`Config.RetryPolicy`), and then only what another attempt can cure and
what is safe to send twice: [OBSERVABILITY.md](OBSERVABILITY.md#retries)
has the table. In particular a create that fails in transport may or may
not have reached the VTN: the client does not send it again, with or
without a policy, unless `Config.RetryCreates` says so, because a second
create could make a second object. Search for the object, or accept a 409
on the name, before repeating it.

A call that was retried returns the error of its last attempt. When the
context ends while it waits for another attempt, that is the error it
returns too, not the context's.

`client.Each` stops at the first error and yields it with a zero value.

The tests of this module do what this section asks of a caller, against a
network that cuts a third of the connections (`TestChaosNetwork`): a create
that failed in transport is sent again, and a 409 on its name means the
first one arrived.

## Notification streams

Opening a WebSocket (`NotificationStream` on a client) fails like any call:

| Situation | Error |
|-----------|-------|
| The VTN has no WebSockets | `*openadr.Error` with status 501 (this module's VTN) or 404, matching `openadr.ErrNotFound` |
| The token is refused, also a fresh one; or the client has none | `*openadr.Error`, matches `openadr.ErrUnauthorized` |
| The client has as many WebSockets open as the VTN allows | `*openadr.Error`, matches `openadr.ErrConflict` |
| The VTN is shutting down | `*openadr.Error` with status 503 |
| The context ended before the stream was open | `context.Canceled` or `context.DeadlineExceeded` |
| The answer is not a WebSocket | a plain error that says so |

An OpenLEADR VTN answers 409 for a second WebSocket of one client, and goes
on doing so after the first was closed, until it next fails to send to it.

`NotificationStream.Next` returns an error when the stream has ended, by
whichever side, and then it has ended for good: close it and open another.
The error wraps the reason, a close code of the VTN (1001: it is shutting
down; 1013: this client did not read fast enough) or a network error; a
program has no reason to tell them apart. A message that is not a
notification is not an error: `Next` passes over it.

`websocket.Subscriber` returns none of this: it logs the failure, if it has
a logger, and connects again. `NewSubscriber` fails only without `Dial` or
`Notify`, and `Ready` only when its context ends or the subscriber is closed.

## Tables (`timeline`)

| Situation | Error |
|-----------|-------|
| An interval has no start, and neither a default nor a previous interval gives one | a plain error naming the interval ID |
| An interval has no duration, and there is no default | a plain error naming the interval ID |
| An interval follows one that does not end | a plain error naming the interval ID |
| A negative duration | a plain error naming the interval ID |
| More than about a million rows | `timeline.ErrTooLarge`; set `Options.Until` |
| A CSV without a header line, or without `start`, `type` or `values` | a plain error that names what is missing |
| A cell that cannot be read | a plain error with the row, from 0, and the column |
| A value that is not set, or not a finite number, when writing CSV | a plain error with the row |

`Table.Intervals` cannot fail, and does not check its result: an event built
from it is validated like any other when it is sent.

## Discovery

`discovery.Browse` and `discovery.Advertise` return an error when the
machine has no network interface with multicast and an IPv4 address, or no
socket can be opened on any: for `Advertise`, typically because port 5353
cannot be shared. `Advertise` also refuses an advertisement that cannot be
written as DNS: a missing port, an instance or host name over 63 bytes, a
program name with a comma, a TXT entry over 255 bytes.

Finding no VTN is not an error: `Browse` returns an empty list. A cancelled
context is one, when nothing was found before. Packets that cannot be sent
on one interface while others work are logged by an `Advertiser` with a
logger, and are nothing a caller hears of.

## In the VTN

`storage.Store` implementations report:

| Error | Meaning | The VTN answers |
|-------|---------|-----------------|
| `openadr.ErrNotFound` | No object with that ID | 404 |
| `*storage.ConstraintError` (matches `openadr.ErrConflict`) | A rule of the store is broken: `Detail` says which | 409 with that detail |
| anything else | The store failed | 500, logged, no detail to the client |

`storage.OpenFile` returns an error that matches `storage.ErrCorrupt` for a
journal it cannot replay. A `storage.File` whose journal can no longer be
written returns an error that matches `storage.ErrReadOnly` for that write
and every later one; the VTN answers 500 for them and keeps serving reads.
A write to one that was closed matches `openadr.ErrClosed`.

`auth.Validator` implementations report:

| Error | Meaning | The VTN answers |
|-------|---------|-----------------|
| `auth.ErrInvalidToken` | Unknown, malformed or expired token | 401 |
| anything else | The validator failed | 500, logged |

`auth.JWTValidator` returns `ErrInvalidToken` for every way a token can be
wrong, without saying which: a bad signature, an algorithm that does not go
with the key, expiry, issuer, audience, a missing client ID. Its only other
error is that the provider's key set could not be fetched while no key is
known.

Every error answer of the VTN is a problem object with the HTTP status in
`status` and its name in `title`. The full list: 400 (invalid body,
parameter or ID, a callback URL that could not be verified, or a
subscription by a mechanism the VTN does not have), 401 (also for a request
without a token to an operation that needs one, on a VTN with
`AllowAnonymous`), 403, 404, 405 (with `Allow`), 409 (also for one WebSocket
too many), 413 (body over `Config.MaxBodyBytes`), 426 (an ordinary request
to `/notifiers/ws`, or a WebSocket handshake of another version), 429 (with
`Retry-After`, when `Config.Limiter` refuses the request), 500, 501
(subscriptions without `Config.Webhooks` and `Config.WebSocket`, the MQTT
topic operations without `Config.MQTT`, `/notifiers` without any of them,
`/notifiers/ws` without `Config.WebSocket`; `/auth/token` without a
`TokenHandler`, and `/auth/server` on a VTN that authenticates nobody) and
503 (the request's context ended, or a WebSocket was asked for while the
VTN shuts down).

A callback URL the VTN refuses is a 400 whose detail names the property and
the reason, for example `objectOperations[0].callbackUrl must use https` or
`... did not answer the verification request with the value of its echo
parameter`. The reason never says what the VTN's own network answered.

Delivering a notification has no caller to return an error to: a delivery
that fails for good is logged by the `webhook.Sender`, if it has a logger,
and the request that caused the notification is not affected. The same
holds for MQTT: the `mqtt.Publisher` logs a broker it cannot reach and a
notification it drops, and `mqtt.NewPublisher` and `mqtt.NewSubscriber`
return an error only for a configuration that cannot work, such as a URL
that is not `mqtt://` or `mqtts://`. A broker that refuses the login is
logged and tried again, not returned.

A notification can also be lost before it is sent: to decide whom to tell,
the VTN reads its subscriptions, and the targets of its VENs, from the
store. When that read fails, the failure is logged and nobody is told; the
request that caused the notification has succeeded and is answered so. The
reverse does not happen: nobody is notified of a change whose request was
answered with an error.

A WebSocket has no retries. A notification for a client whose queue is full
is not sent: the VTN closes that connection with code 1013, so that the
client notices and catches up, rather than leave it behind without a word.

Every constructor returns an error that matches `openadr.ErrConfiguration`
for a configuration it cannot work with, and says what is wrong with it.

## What is not an error

- A search that matches nothing returns an empty slice.
- A search does not say that objects were left out because the client may not see them.
- A property in an answer that the schema does not define is ignored.
- A `ven` or `resource` object whose `objectType` is missing or unexpected is accepted (see docs/CONFORMANCE.md).
- An interval period whose start is `0001-01-01` or `0001-01-01T00:00:00`, which are not date-times, is read as the zero time: the User Guide writes "now" that way.
- A message on a notification stream that is not a notification is passed over.
- A network without multicast finds no VTN.

## Detection patterns

```go
event, err := ven.Event(ctx, id)
switch {
case err == nil:
case errors.Is(err, openadr.ErrNotFound):
	// gone, or never visible to this VEN
case errors.Is(err, context.DeadlineExceeded):
	// out of time
default:
	var apiErr *openadr.Error
	var tokenErr *client.TokenError
	var invalid *openadr.ValidationError
	switch {
	case errors.As(err, &tokenErr):
		// credentials: tokenErr.Code is "invalid_client", ...
	case errors.As(err, &apiErr):
		// the VTN said no: apiErr.StatusCode, apiErr.Problem.Detail
	case errors.As(err, &invalid):
		// a bad ID (nothing was sent), or an answer this client cannot read: invalid.Field
	default:
		// transport
	}
}
```
