# Errors: otfabric/go-openadr

How go-openadr reports failures. For the rest of the behavioural contract see [API.md](API.md).

## Table of contents

- [Quick reference](#quick-reference)
- [Answers of the VTN (`*openadr.Error`)](#answers-of-the-vtn-openadrerror)
- [Invalid objects (`*openadr.ValidationError`)](#invalid-objects-openadrvalidationerror)
- [Tokens (`*client.TokenError`)](#tokens-clienttokenerror)
- [Transport, context and timeouts](#transport-context-and-timeouts)
- [Retries](#retries)
- [In the VTN](#in-the-vtn)
- [What is not an error](#what-is-not-an-error)
- [Detection patterns](#detection-patterns)

## Quick reference

Errors a client method can return:

| Situation | Error | Was a request sent? |
|-----------|-------|---------------------|
| The VTN answered 400 | `*openadr.Error`, matches `openadr.ErrBadRequest` | yes |
| The VTN answered 401, also with a fresh token | `*openadr.Error`, matches `openadr.ErrUnauthorized` | yes |
| The VTN answered 403: the token lacks the scope, or the client may not do this | `*openadr.Error`, matches `openadr.ErrForbidden` | yes |
| The VTN answered 404: no such object, or none the client may see | `*openadr.Error`, matches `openadr.ErrNotFound` | yes |
| The VTN answered 409: a name is taken, a referenced object is missing, or dependents exist | `*openadr.Error`, matches `openadr.ErrConflict` | yes |
| The VTN answered with any other status outside 2xx (500, 501, ...) | `*openadr.Error`; matches no sentinel | yes |
| The object, or an object ID, violates the schema | `*openadr.ValidationError` | no |
| The token endpoint refused the client | wraps `*client.TokenError` | no API request |
| The token endpoint could not be discovered or answered nonsense | an error that says so | no API request |
| No connection, TLS failure, connection lost | the error of `net/http` (`*url.Error`) | unknown |
| The context ended | wraps `context.Canceled` or `context.DeadlineExceeded` | unknown |
| The answer could not be decoded | an error starting "openadr: decoding the response", wrapping a `*openadr.ValidationError` when one property is at fault | yes, and the VTN acted on it |
| The answer is larger than `Config.MaxResponseBytes` | an error that says so | yes, and the VTN acted on it |

Always test with `errors.Is` and `errors.As`, never by comparing strings.

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

Nothing else is retried. In particular a create that fails in transport may
or may not have reached the VTN: the client does not send it again, because
a second create could make a second object. Search for the object, or accept
a 409 on the name, before repeating it.

`client.Each` stops at the first error and yields it with a zero value.

## In the VTN

`storage.Store` implementations report:

| Error | Meaning | The VTN answers |
|-------|---------|-----------------|
| `openadr.ErrNotFound` | No object with that ID | 404 |
| `*storage.ConstraintError` (matches `openadr.ErrConflict`) | A rule of the store is broken: `Detail` says which | 409 with that detail |
| anything else | The store failed | 500, logged, no detail to the client |

`storage.OpenFile` returns an error that matches `storage.ErrCorrupt` for a
journal it cannot replay. A `storage.File` whose journal can no longer be
written returns a plain error for that write and every later one ("the
store is read-only until restarted"); the VTN answers 500 for them and keeps
serving reads.

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
parameter or ID, or a callback URL that could not be verified), 401, 403,
404, 405 (with `Allow`), 409, 413 (body over `Config.MaxBodyBytes`), 500,
501 (subscriptions without `Config.Webhooks`, the MQTT topic operations
without `Config.MQTT`, `/notifiers` without either; `/auth/token` without a
`TokenHandler`) and 503 (the
request's context ended).

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

`server.New`, `client.NewVEN`, `client.NewBL` and `auth.NewIssuer` return a
plain error for an invalid configuration.

## What is not an error

- A search that matches nothing returns an empty slice.
- A search does not say that objects were left out because the client may not see them.
- A property in an answer that the schema does not define is ignored.
- A `ven` or `resource` object whose `objectType` is missing or unexpected is accepted (see docs/CONFORMANCE.md).

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
