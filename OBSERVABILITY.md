# Observability: otfabric/go-openadr

What a program can know about a client and a VTN beside what their calls
return: logs, metrics, and the errors to tell failures apart by. Retries are
here too, because they are what the logs and metrics are most often read
for. Signatures are in [API.md](API.md), the errors in [ERRORS.md](ERRORS.md);
[examples/observability](examples/observability/main.go) runs all of it.

Everything here is off by default and costs nothing when off: a client or a
VTN that is given no logger, no metrics and no retry policy behaves as it
did before they existed.

## Logging (silent by default)

Logging uses `*slog.Logger`, given in the configuration of whatever is to
log. Without one, nothing is written anywhere: not to `slog.Default()`, not
to standard error.

| Who | Configuration | Debug | Info | Warn | Error |
|---|---|---|---|---|---|
| Client | `client.Config.Logger` | every call: operation, duration, error | a token renewed because the VTN refused the old one | every retry: operation, attempt, wait, error | a callback that did not return normally |
| VTN | `server.Config.Logger` | every request: operation, status, duration | | with an `Outbox`: a recorded notification given up (too old, its subscription gone or no longer naming its callback URL, refused for good), the notifications recorded for the subscriptions of a client at their limit (once, until one is recorded again) | a failure answered with 500: method, path, error; a notification that could not be decided; a callback that did not return normally |
| Webhook sender | `webhook.SenderConfig.Logger` | | | a notification dropped or not delivered | a callback that did not return normally |
| Webhook handler | `webhook.Handler.Logger` | | | a request refused (below) | a notification answered 500 (below); a `Notify` that did not return normally |
| MQTT publisher, subscriber | `mqtt.PublisherConfig.Logger`, `mqtt.SubscriberConfig.Logger` | | | broker unreachable, connection lost, notification dropped, a notification MQTT cannot carry (dropped), a message that is not a notification (topic and error cut to 256 bytes) | a callback that did not return normally |
| WebSocket subscriber | `websocket.SubscriberConfig.Logger` | | | VTN unreachable, connection lost | a callback that did not return normally |
| Advertiser | `discovery.Advertisement.Logger` | | | a packet that could not be sent or received | a handler of the Logger that did not return normally |

```go
logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
ven, err := client.NewVEN(client.Config{BaseURL: url, Credentials: creds, Logger: logger})
```

**What is never logged:** request and response bodies, bearer tokens, client
secrets, and the query of a callback URL, which may hold a secret of its
subscriber; the webhook sender takes that query out of the error of a
failed request, before it is logged or given to `Metrics.OnFailed` or the
done function of `SendConfirmed`. The line of a call or a request names the *operation*
(`GET /events/{id}`), not the path, so object IDs stay out of the debug log;
an error names its path, as the error itself does.

The context of a call is passed to the logger (`DebugContext`, ...), so a
handler that reads trace IDs from the context sees them.

### The webhook handler

`webhook.Handler` writes one entry for each request it refuses or fails
on, and none for a request it answers 200; the context of the request goes
to the logger. Every entry has the fields `status` and `reason`:

| Level | Message | `reason` | Other fields |
|---|---|---|---|
| Warn | `webhook: request refused` | `echo parameter missing or longer than 256 bytes` (400), `method not allowed` (405), `bearer token missing or wrong` (401), `body longer than MaxBodyBytes` (413), `body could not be read` (400), `not a notification` (400) | `method`; `error` for a body that could not be read or is not a notification |
| Error | `webhook: notification failed` | `Notify returned an error`, `no Notify`, `Version not supported` (500) | `error`, the error of `Notify` or of `Version` |

The handler never logs a token, the body, the path or the query of the
request (the callback URL may hold a secret of the subscription). What the
request chose, the `method` and an `error` that may quote the body, and the
error of `Notify`, are cut to 256 bytes at a character boundary and quoted
as a Go string, so that no line break or control character of the peer's
reaches the log: `"PUT"`, and for one that was cut its first 256 bytes and
its length, `"XX…XX"... (300 bytes)`.

## Metrics

Metrics are interfaces of a few callbacks, given in the configuration. The
module has no metrics library of its own and depends on none: an
implementation is a few lines that feed whatever the program uses, a
Prometheus counter and histogram, OpenTelemetry instruments, `expvar`.

The callbacks run on the path of the call or request, without a lock of the
module held: one that is slow holds up the call it is made for, and no other.
They must not block: count, observe, return. They are called from many
goroutines.

### A client's calls

`client.Config.Metrics` takes a `client.Metrics`:

| Callback | When |
|---|---|
| `OnRequest(op)` | a call begins |
| `OnResponse(op, duration)` | it succeeded |
| `OnError(op, duration, err)` | it failed for any reason but time |
| `OnTimeout(op, duration)` | it ran out of time (`openadr.ErrTimeout`) |

Every call gives one `OnRequest` and exactly one of the other three. A call
is what a method of `VEN` or `BL` does, however many HTTP requests that
takes: the token request before it, the one retry with a fresh token after a
401, and the retries of the policy are all inside it. So these numbers are
what the *caller* experienced.

A value that also has `OnAttempt(op, attempt, duration, err)`
(`client.AttemptMetrics`) is told of every attempt as well, which is where
retries show: attempts minus calls is the number of retries.

`op` is the operation as the specification names it, by method and path,
with `{id}` where an ID stands: `GET /events`, `PUT /programs/{id}`,
`GET /notifiers/mqtt/topics/vens/{id}/events`, `GET /notifiers/ws`. The set
is small and fixed, so it is fit to be a label. `err` is the error the
method returns: label by `errors.Is` against the sentinels of
[ERRORS.md](ERRORS.md), never by its text.

A call that is refused before anything is sent, an invalid object or ID, is
not a call to the VTN and is not reported.

### A VTN's requests

`server.Config.Metrics` takes a `server.Metrics`:

| Callback | When |
|---|---|
| `OnRequest(op)` | a request arrives |
| `OnResponse(op, status, duration)` | it has been answered |

`op` is the route, in the same form as on the client; a request that is no
operation of the VTN (an unknown path, a method the path does not have) is
`other`, so that a scanner cannot make labels. A request that opens a
WebSocket is answered when the WebSocket is open, with status 101: its
duration is that of the handshake, not of the connection.

The same value may implement two extensions:

| Interface | Callback | When |
|---|---|---|
| `server.NotificationMetrics` | `OnNotification(mechanism, object, operation)` | a notification is handed to a mechanism: `WEBHOOK`, `MQTT` or `WEBSOCKET`; once per webhook request, per message to the broker, per WebSocket |
| `server.SocketMetrics` | `OnSocketOpened()`, `OnSocketClosed(reason)` | a client's WebSocket opens, and ends: `closed` (by the client, or lost), `slow` (its queue was full), `shutdown` |

`OnNotification` says what the VTN *decided* to send. What became of it is
for the sender to say:

### Deliveries

| Who | Configuration | Callbacks |
|---|---|---|
| Webhook sender | `webhook.SenderConfig.Metrics` | `OnDelivered(attempts, duration)`, `OnFailed(attempts, duration, err)`, `OnDropped()`: every notification ends in exactly one |
| MQTT publisher | `mqtt.PublisherConfig.Metrics` | `OnPublished()`, `OnDropped()` (a full queue, or a notification MQTT cannot carry: a topic with a wildcard or NUL, a topic or body too long), `OnConnected()`, `OnDisconnected(err)`; `OnLateAck()` with `mqtt.LateAckMetrics` |
| MQTT subscriber | `mqtt.SubscriberConfig.Metrics` | `OnReceived()`, `OnConnected()`, `OnDisconnected(err)`; `OnLateAck()` with `mqtt.LateAckMetrics` |
| WebSocket subscriber | `websocket.SubscriberConfig.Metrics` | `OnReceived()`, `OnConnected()`, `OnDisconnected(err)` |

A `Metrics` of the MQTT publisher or subscriber that also implements
`mqtt.LateAckMetrics` is told by `OnLateAck()` of every acknowledgement
from the broker (PUBACK, SUBACK, UNSUBACK) that nothing waits for: the
answer to a publish or a subscription that was given up on (its 30 seconds
ran out, or `Close` gave it up), which came after all, or one that answers
nothing. It is dropped and completes no other request. A few after a
timeout are expected; a steady count is a broker that answers late or
answers what was not asked. It runs on the goroutine that reads the
connection and, like the other callbacks, must not block.

The ones to alert on: `OnDropped` anywhere (a queue was full: a webhook or
the broker has been away too long, and notifications are being lost; for
the webhook sender the queue is that of the webhook's host),
`OnFailed` of the webhook sender, `slow` WebSockets, and a subscriber whose
`OnDisconnected` keeps coming without an `OnConnected` between.

## Callbacks that do not return normally

Application code that the module calls and that does not return normally
(a callback such as `Notify` or `Dial`, a metrics hook, the handler of a
logger, and, on the goroutines of a VTN, `Verify` of its `WebhookSender` and,
with an `Outbox`, the outbox, the store, `SendConfirmed` and `Config.Now`)
is stopped where it was called. Once the module has released its locks, the
logger of the component writes, at error level:

| Field | Value |
|---|---|
| message | `<prefix>: callback did not return normally`; the prefix is `openadr` for the client and the VTN, `webhook`, `mqtt`, `websocket` or `discovery` |
| `callback` | what was called: `Notify`, `Dial`, `Metrics.OnReceived`, `SendConfirmed done`, `WebhookSender.Verify`, `Outbox.Deliveries`, `Logger`, ... |
| `value` | what it ended with |
| `stack` | the stack of the goroutine where it ended |

The module then goes on: as if a callback or a metrics hook had returned,
and as if the other code had failed (a callback URL that could not be
verified, a notification that stays recorded for the next attempt). A
handler of the logger that does not return normally loses that entry and is
reported once more in the same way, with `callback` `Logger`; when it does
not return normally for that entry either, the entry is dropped. Without a
logger nothing is written, and the module goes on all the same.

A `Notify` of `webhook.Handler` runs in an HTTP request instead: one that
does not return normally is answered 500. With a `Logger` it is reported
there as above; without one it is written with its stack where the
`http.Server` that serves the Handler writes errors (its `ErrorLog`, or the
standard logger), as `net/http` writes a handler that does not return
normally. `http.ErrAbortHandler` is passed on to `net/http`.

## Retries

A client retries one thing by itself: a request answered with 401 is sent
once more with a fresh token. Everything else is for a policy,
`client.Config.RetryPolicy`, and without one nothing else is retried.

```go
ven, err := client.NewVEN(client.Config{
    BaseURL:     url,
    Credentials: creds,
    RetryPolicy: client.ExponentialBackoff(200*time.Millisecond, 30*time.Second, 4),
})
```

`ExponentialBackoff(base, max, attempts)` waits `base` before the second
attempt and twice as long before each one after, up to `max`, randomized
between half and all of that so that clients that failed together do not
come back together, and gives up after `attempts` attempts.
`NewExponentialBackoff` has the rest: no jitter, no limit, retrying
timeouts. A policy of your own is one method.

**What a policy is asked about** is decided by the client, and a policy
cannot widen it:

| Failure | Read, update, delete | Create (POST) |
|---|---|---|
| The connection could not be made | retried | retried: nothing was sent |
| 429, 503 | retried | retried: refused before anything was done |
| The connection was lost, or timed out ¹ | retried | **not** retried ² |
| 502, 504 | retried | **not** retried ² |
| A token could not be had (the token endpoint failed the same way) | retried | retried: nothing was sent |
| 400, 401 after a fresh token, 403, 404, 409, 500, 501 | never | never |
| The context was cancelled | never | never |

¹ A timeout only with `RetryOnTimeout`: a call that used up its time once
often does again, and a caller that set a short timeout meant it.

² The VTN may have created the object, and a second create would make a
second one, or fail on its name. `Config.RetryCreates` lets the policy
retry these too, for a caller that takes that on: where names are unique
(programs, VENs, resources within a VEN) the second attempt is answered
`openadr.ErrConflict`, which then means "your first attempt arrived".
Events, reports and subscriptions have no such guard, and a retried create
of one can leave two.

**`Retry-After`.** When the VTN, or a gateway before it, says how long to
wait, the client waits at least that long, whatever the policy computed,
and the wait is in `openadr.Error.RetryAfter` for a caller without a
policy. The webhook sender does the same with a webhook's `Retry-After`,
up to its `MaxBackoff`.

**The context still rules.** A call's context bounds all its attempts and
the waits between them. When it ends during a wait, the call returns the
failure it was going to retry, not the context's error: that is what went
wrong. A wait that would outlast the context's deadline is not begun: the
call returns that failure at once.

**A delete that is retried** after the first attempt arrived finds nothing
and is answered 404. The object is gone either way.

**What the specification says.** OpenADR 3.1.0 does not speak of client
retries. It asks a VTN to back off when a webhook fails, which the webhook
sender does (`Attempts`, `Backoff`, `MaxBackoff`), and expects rate limiting
to be done by a gateway before the VTN, which is one place the 429 and the
`Retry-After` handled here come from. The other is this module's VTN when
it is given a `server.Config.Limiter`: it answers 429 with `Retry-After`
itself, for a client that has gone wrong. A limited request is counted like
any other, as a response with status 429 for its operation.

The other things that reconnect by themselves have a backoff of their own,
doubling to a minute: `mqtt.Publisher`, `mqtt.Subscriber` and
`websocket.Subscriber` (`Backoff` in their configuration). The MQTT ones
also wait it out after a connection that was lost, so that a broker that
drops every connection at once is not dialled again at once.
