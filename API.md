# API Reference: otfabric/go-openadr

The public API by package, and the behaviour that signatures do not show.
The godoc on [pkg.go.dev](https://pkg.go.dev/github.com/otfabric/go-openadr)
is the authoritative text for each symbol; this document is the map.

## Table of contents

- [Package `openadr`](#package-openadr)
- [Package `client`](#package-client)
- [Package `server`](#package-server)
- [Package `server/storage`](#package-serverstorage)
- [Package `notification/webhook`](#package-notificationwebhook)
- [Package `notification/mqtt`](#package-notificationmqtt)
- [Package `notification/websocket`](#package-notificationwebsocket)
- [Package `timeline`](#package-timeline)
- [Package `discovery`](#package-discovery)
- [Package `auth`](#package-auth)
- [Beyond OpenADR 3.1.0](#beyond-openadr-310)
- [Concurrency](#concurrency)
- [Limits](#limits)

Logging, metrics and retries have a document of their own:
[OBSERVABILITY.md](OBSERVABILITY.md).

## Package `openadr`

`import openadr "github.com/otfabric/go-openadr"`

The domain model both sides share. Nothing here does I/O.

### Objects

```go
type Program  struct { Metadata; Name string; IntervalPeriod *IntervalPeriod; Descriptions []ProgramDescription
                       PayloadDescriptors []PayloadDescriptor; Attributes []ValuesMap; Targets []string }
type Event    struct { Metadata; ProgramID, Name string; Duration *Duration; Priority *int; Targets []string
                       ReportDescriptors []ReportDescriptor; PayloadDescriptors []EventPayloadDescriptor
                       IntervalPeriod *IntervalPeriod; Intervals []Interval }
type Report   struct { Metadata; ClientID, EventID, ClientName, Name string
                       PayloadDescriptors []ReportPayloadDescriptor; Resources []ReportResource }
type VEN      struct { Metadata; ClientID, Name string; Attributes []ValuesMap; Targets []string }
type Resource struct { Metadata; ClientID, VENID, Name string; Attributes []ValuesMap; Targets []string }

type Metadata struct { ID string; Created, Modified time.Time }
```

`ResourceGroup` is a seventh kind of object, which is not part of OpenADR
3.1.0: see [Beyond OpenADR 3.1.0](#beyond-openadr-310).

- **One type per object**, for requests and answers alike. `Metadata` is the VTN's: leave it zero in an object to create, and expect the VTN to ignore whatever a request says there. An update names its object by `Metadata.ID`.
- **`ClientID`** of a report, and of a VEN or resource a VEN creates, is written by the VTN from the token. Business Logic sets it when it creates a VEN or resource.
- **`Targets`** of a VEN or resource are set by Business Logic only. A VEN's own request does not send them, and its update keeps what Business Logic set.
- **Absent and empty.** An optional string is absent when empty, an optional object or number when its pointer is nil, an optional list when nil. `IntervalPeriod.Start` is a pointer because the zero `time.Time` is year 1, which the specification gives a meaning ("now").
- **No JSON tags.** These types are not meant to be marshalled directly: the JSON of a protocol revision is the business of `client` and `server`.
- **Strings stay open.** Payload types, units, reading types and targets are plain strings: the enumerations of the specification are suggestions, and private values are allowed.

### Values

```go
type ValuesMap struct { Type string; Values []Value }

func Number(float64) Value
func String(string) Value
func Bool(bool) Value
func PointValue(x, y float64) Value

func (Value) Kind() ValueKind            // KindNumber, KindString, KindBool, KindPoint; 0 for the zero Value
func (Value) Number() (float64, bool)    // likewise Text, Bool, Point
```

A `Value` is one of the four kinds a `valuesMap` may hold. Values are
comparable with `==`. JSON does not distinguish integers from other numbers,
so both are a `float64`; integers beyond 2^53 lose precision. The zero
`Value` is invalid and is refused by `Validate`.

### Durations

```go
type Duration struct { Negative bool; Years, Months, Weeks, Days, Hours, Minutes int64; Seconds float64 }

func ParseDuration(string) (Duration, error)   // "PT1H", "P1DT12H", "-PT15M", "P2W"
func DurationOf(time.Duration) Duration        // hours, minutes and seconds only
func (Duration) String() string                // canonical; the zero Duration is "PT0S"
func (Duration) TimeDuration() (time.Duration, bool)
func (Duration) AddTo(time.Time) time.Time
func (Duration) IsInfinite() bool              // P9999Y, also var Infinite
```

Years and months are kept as written, because their length depends on the
date: `TimeDuration` reports false for a duration that has either (and takes
a day as 24 hours otherwise), and `AddTo` does calendar arithmetic in the
time's location. `ParseDuration` accepts exactly what the pattern of the
specification accepts, including the forms other implementations write
(`P0Y0M0DT1H0M0S`); two durations that mean the same may therefore differ as
structs. Compare their `String()` only after knowing both were written the
same way, or compare `TimeDuration()`.

### Report descriptors

`NewReportDescriptor(payloadType)` returns a descriptor with the defaults of
the specification (`StartInterval` -1, `NumIntervals` -1, `Historical` true,
`Frequency` -1, `Repeat` 1, `ReportIntervals` `INTERVALS`). **The zero
`ReportDescriptor` does not have them** and fails `Validate`: always start
from the constructor.

### Validation and errors

`Validate()` on `Program`, `Event`, `Report`, `VEN` and `Resource` checks the
client-provided properties against the 3.1.0 schema and returns a
`*ValidationError`. `ValidID` checks an object ID. `Error`, `Problem` and the
sentinels are described in [ERRORS.md](ERRORS.md).

Every failure the module reports matches a sentinel of this package with
`errors.Is`: the categories of a VTN's answers (`ErrNotFound`,
`ErrConflict`, `ErrUnavailable`, ...) and those that are not an answer
(`ErrInvalid`, `ErrConfiguration`, `ErrTimeout`, `ErrProtocol`,
`ErrClosed`).

## Package `client`

`import "github.com/otfabric/go-openadr/client"`

```go
func NewVEN(Config) (*VEN, error)
func NewBL(Config) (*BL, error)

type Config struct {
	BaseURL          string        // required: what /programs is appended to
	HTTPClient       *http.Client  // default: a client with a 30 s timeout
	Credentials      *Credentials  // OAuth2 client credentials, or
	Tokens           TokenSource   // ... any other source of bearer tokens
	UserAgent        string
	MaxResponseBytes int64         // default 16 MiB
	RetryPolicy      RetryPolicy   // default NoRetry()
	RetryCreates     bool          // let the policy retry a create whose fate is not known
	Metrics          Metrics       // callbacks per call
	Logger           *slog.Logger  // calls at debug, token renewals at info, retries at warn
}
```

With neither `Credentials` nor `Tokens` the client sends no token, which is
how the specification has a VEN talk to a VTN that does not authenticate.

### Lifecycle

`NewVEN` and `NewBL` validate the configuration and return; they do not
contact the VTN. There is nothing to close: a client holds no connection of
its own, only what its `http.Client` pools. The first call that needs a
token fetches one.

### Operations

| | `VEN` | `BL` |
|---|:---:|:---:|
| `Programs`, `Program`, `Events`, `Event` | yes | yes |
| `CreateProgram`, `UpdateProgram`, `DeleteProgram`, and the same for events | | yes |
| `Reports`, `Report` | own reports | all reports |
| `CreateReport`, `UpdateReport`, `DeleteReport` | yes | |
| `VENs`, `VEN`, `Resources`, `Resource` | own objects | all objects |
| `CreateVEN`, `UpdateVEN`, `DeleteVEN`, and the same for resources | VEN request | Business Logic request |
| `Subscriptions`, `Subscription` | own subscriptions | all subscriptions |
| `CreateSubscription`, `UpdateSubscription`, `DeleteSubscription` | yes | yes |
| `Notifiers` | yes | yes |
| `MQTTTopics` | the topics of its own ven | all topics |

The two types differ in what they offer, following the scopes of the
specification, and in the request they send for VENs and resources: a `BL`
sends the client ID and the targets, a `VEN` sends neither. What a client
may actually do is decided by its token: a `BL` built with a VEN's
credentials gets 403.

- Every method takes a `context.Context` that bounds the whole call.
- A create returns the object as the VTN stored it; a delete returns the object as it last was.
- An update replaces the whole object: read it, change it, send it back.
- An object is validated before it is sent; nothing is sent for one that is invalid.

### Searches and paging

Each search takes a query struct (`ProgramQuery`, `EventQuery`, `ReportQuery`,
`VENQuery`, `ResourceQuery`) with the filters the specification defines and
an embedded `Page{Skip, Limit}`. Filters are combined: an object has to
match all of them. A zero `Limit` leaves the page size to the VTN, which
returns at most 50 objects.

```go
for event, err := range client.Each(ctx, func(ctx context.Context, p client.Page) ([]openadr.Event, error) {
	return ven.Events(ctx, client.EventQuery{ProgramID: id, Page: p})
}) { ... }
```

`Each` fetches a whole result, 50 at a time, and stops at the first error. It
is not a snapshot: objects created or deleted while it runs may be skipped
or seen twice.

**Targets.** A VEN is shown a program or event that has targets only for
targets Business Logic granted it, and many VTNs — this module's included —
only when the search names them. A VEN that polls for everything meant for
it searches once without targets and once with its own.

### Tokens

With `Credentials`, the client performs the OAuth2 client-credentials grant:

- The token endpoint is `Credentials.TokenURL`, or, when that is empty, what the VTN names with `GET /auth/server`. A relative name is resolved against `BaseURL`. For an `https` VTN an `http` token endpoint is refused.
- The client ID and secret are sent in the form body, as the OpenADR schema defines the request.
- A token is reused until 10 seconds before it expires; one without a stated lifetime until the VTN rejects it. Concurrent calls wait for one token request instead of each making their own.
- A 401 from the VTN discards the token and the request is sent once more with a fresh one.

### Retries, metrics and logging

```go
type RetryPolicy interface { ShouldRetry(attempt int, err error) (retry bool, wait time.Duration) }

func NoRetry() RetryPolicy
func ExponentialBackoff(base, maxDelay time.Duration, maxAttempts int) RetryPolicy
func NewExponentialBackoff(ExponentialBackoffConfig) RetryPolicy   // {BaseDelay, MaxDelay, MaxAttempts, RetryOnTimeout, NoJitter}

type Metrics interface {
	OnRequest(op string)
	OnResponse(op string, duration time.Duration)
	OnError(op string, duration time.Duration, err error)
	OnTimeout(op string, duration time.Duration)
}
type AttemptMetrics interface { OnAttempt(op string, attempt int, duration time.Duration, err error) }   // optional
```

- **Nothing is retried by default.** With a policy, the client asks it only about failures another attempt can cure (no answer, 429, 502, 503, 504) and only for requests that are safe to send twice: a read, an update and a delete always; a create only when it certainly did not happen, unless `RetryCreates`. A policy cannot widen that. The table is in [OBSERVABILITY.md](OBSERVABILITY.md#retries).
- **`Retry-After`** of a 429 or 503 is honoured: the client waits at least that long. It is in `openadr.Error.RetryAfter`.
- **A call's context bounds all its attempts** and the waits between them.
- **`Metrics`** is told of every call, once when it begins and once when it ends; a call is what a method does, token request and retries included. `op` is method and path with `{id}` for an ID, a small fixed set. A value that is also an `AttemptMetrics` is told of every attempt.
- **`Logger`** gets one debug line per call, an info line when a token is renewed after a 401, and a warning per retry. Never bodies, tokens or secrets.
- All of it applies to opening a notification stream as well.

`Tokens` takes any `TokenSource` instead; `StaticToken` is one that always
returns the same token. With neither, requests carry no `Authorization`
header.

## Package `server`

`import "github.com/otfabric/go-openadr/server"`

```go
func New(Config) (*Server, error)
func (*Server) ServeHTTP(http.ResponseWriter, *http.Request)

type Config struct {
	Store        storage.Store   // required
	Validator    auth.Validator  // required
	TokenURL     string          // required: absolute URL, announced by GET /auth/server
	TokenHandler http.Handler    // optional: serves POST /auth/token
	Webhooks     WebhookSender   // optional: subscriptions and notifications
	MQTT         *MQTTConfig     // optional: notifications by MQTT
	Logger       *slog.Logger    // optional: internal errors
	MaxBodyBytes int64           // default 1 MiB
	Now          func() time.Time

	Metrics            Metrics          // callbacks per request, and for notifications and WebSockets
	AllowAnonymous     bool             // answer requests without a token
	DisableCompression bool             // no gzip, no deflate
	WebSocket          *WebSocketConfig // optional, not of 3.1.0: notifications over WebSockets
	ResourceGroups     bool             // optional, not of 3.1.0: serve /resource_groups
}

func (*Server) Close() error
```

A `Server` is an `http.Handler` for the root of the API. It owns no
listener and no state besides its `Store`: starting, TLS, timeouts and
shutdown belong to the `http.Server` it is given to. To serve it under a
path, wrap it in `http.StripPrefix`. The one thing it does own is the
WebSockets of its clients, when it has `WebSocket`: `Close` ends them, and
is to be called when the VTN stops, before the `http.Server` is shut down,
which no longer knows of those connections.

- **`AllowAnonymous`** serves clients that do not authenticate, for public information. A request without an `Authorization` header is treated as a client with no ID that may read programs and events: it sees those without targets, and whatever it names in `targets`, nothing with them. Every operation on objects that belong to a client, every write and the WebSocket answer 401. A request *with* a token is validated as always, so a wrong token is still a 401. With `AllowAnonymous`, `Validator` and `TokenURL` may be left out, for a VTN that authenticates nobody: `GET /auth/server` then answers 501.
- **Compression.** An answer of 1 KiB or more is compressed for a client whose `Accept-Encoding` allows gzip or deflate: the one the client gives the higher weight, gzip when they weigh the same, with `*` standing for whichever is not named. `deflate` is the zlib format, as HTTP defines it. br and zstd are not offered: the standard library cannot write them. Answers carry `Vary: Accept-Encoding`. Problem answers and the token endpoint are not compressed.

- **`Webhooks`** turns subscriptions to webhooks on. Without it and without `WebSocket`, `/subscriptions` and `GET /notifiers` answer 501. It is an interface (`Verify`, `Send`) that `*webhook.Sender` implements; the sender has workers of its own, and the embedding program closes it on shutdown, after the `http.Server`.

- **`TokenURL`** is what clients are told, so it is the address as *they* see it, proxies and path prefixes included.
- **Without `TokenHandler`**, `POST /auth/token` answers 501, for a VTN whose tokens come from elsewhere.
- **IDs and timestamps** are the VTN's: 32 hexadecimal characters from `crypto/rand`, and `Now()` in UTC.
- **Logging** is silent without a `Logger`. With one, failures that become a 500 are logged, with method and path, never bodies or tokens; and at debug level one line per request, with its operation, status and duration.
- **`Metrics`** is `OnRequest(op)` and `OnResponse(op, status, duration)`, with `op` the route (`GET /programs/{id}`), or `other` for a request that is no operation. A value that also implements `NotificationMetrics` (`OnNotification(mechanism, object, operation)`) or `SocketMetrics` (`OnSocketOpened()`, `OnSocketClosed(reason)`) is told of those too. See [OBSERVABILITY.md](OBSERVABILITY.md).

Which operations exist, who may call them and how the VTN reads the
specification where it leaves room is in [docs/CONFORMANCE.md](docs/CONFORMANCE.md).

- **`MQTT`** turns the MQTT binding on: `MQTTConfig{Publisher, URIs, Authentication, TopicPrefix}`. The publisher sends; `URIs` and `Authentication` are what `GET /notifiers` tells clients about the broker, so `URIs` are the broker's addresses as clients reach it. Without it the 12 topic operations answer 501. `*mqtt.Publisher` implements `MQTTPublisher`.
- **`GET /notifiers`** is served when any binding is configured, and says which.

## Package `server/storage`

`import "github.com/otfabric/go-openadr/server/storage"`

```go
type Store interface {
	Programs()  Collection[openadr.Program, ProgramFilter]
	Events()    Collection[openadr.Event, EventFilter]
	Reports()   Collection[openadr.Report, ReportFilter]
	VENs()      Collection[openadr.VEN, VENFilter]
	Resources() Collection[openadr.Resource, ResourceFilter]
	Subscriptions() Collection[SubscriptionRecord, SubscriptionFilter]
	ResourceGroups() Collection[openadr.ResourceGroup, ResourceGroupFilter]   // not of 3.1.0
}

type Collection[T, F any] interface {
	Create(ctx, T) error
	Get(ctx, id) (T, error)
	Update(ctx, T) error
	Delete(ctx, id) (T, error)
	List(ctx, F) ([]T, error)   // in creation order
}

func NewMemory() *Memory
func OpenFile(path string, opts FileOptions) (*File, error)

// for a store of your own:
func Encode[T Object](v T) ([]byte, error)    // an object as JSON
func Decode[T Object](data []byte) (T, error)

// in package server/storage/storagetest:
func Run(t *testing.T, cfg Config)            // Config{New, Reopen}
```

A `Store` enforces what has to be decided atomically with a write, and
reports a violation as a `*ConstraintError`:

- program names and VEN names are unique, and a resource name within its VEN;
- an event refers to an existing program, a report to an existing event (when created, or moved to another event), a resource to an existing VEN, a subscription that names a program to an existing one;
- a program that has events, and a VEN that has resources, cannot be deleted. Reports outlive their event;
- the children of a resource group exist (a group may hold itself), and a resource or group that is deleted is taken out of the groups that held it.

`ResourceGroups` was added to the interface in v0.2.0. A store of a VTN that
does not serve resource groups still has to have the method; it may return a
collection that is always empty.

Everything about who may see or write what stays in the server, so every
backend behaves the same.

**A store of your own.** The interface is the whole contract, and three
things help with it:

- `Encode` and `Decode` turn any object a store keeps (the six kinds of the API, and `SubscriptionRecord`) into JSON and back. The types of package `openadr` have no JSON form of their own, on purpose; this is the form the API gives them, it is stable across releases, and it lets a store keep an object as one document beside the few columns it searches by.
- `storagetest.Run` is the suite the stores of this module pass: the rules above, the order of a search, concurrent writers, every property of every kind of object through a write and a read, and, with `Reopen`, across a restart. A store that passes it behaves under the VTN as `Memory` does.
- [examples/storage](examples/storage/README.md) is the interface on SQLite and on PostgreSQL, in a module of its own so that the drivers are not dependencies of this one.

- **Ownership of values:** neither the caller nor the store modifies a value, or anything it points to, after handing it over. A store may return the very slices it holds.
- **IDs** are assigned by the caller, in `Metadata.ID`.
- **`SubscriptionRecord`** is a subscription with what the VTN has to remember about the subscriber (`ReadsAll`: it was Business Logic), since a notification is decided without a request and its token at hand.
- **`Memory`** keeps everything in the process.
- **`File`** is `Memory` with a journal: every change is appended to a file, and synced, before the write returns; `OpenFile` replays the journal and rewrites it as a snapshot. A last line cut short by a crash is dropped; damage anywhere else is `ErrCorrupt`, and the file is left untouched. If the journal cannot be written, that write fails and the store accepts no further change until it is reopened, so that memory is never more than one change ahead of the file. One process per file; all objects are in memory. `FileOptions.NoSync` trades the sync for speed. Close it on shutdown. The file is created with mode 0600: it holds the bearer tokens of subscriptions.

## Package `notification/webhook`

`import "github.com/otfabric/go-openadr/notification/webhook"`

The webhook binding of notifications, for both sides.

### In a VTN: `Sender`

```go
func NewSender(SenderConfig) *Sender
func (*Sender) Verify(ctx, callbackURL string) error
func (*Sender) Send(callbackURL, bearerToken string, body []byte)
func (*Sender) Close(ctx) error

type SenderConfig struct {
	HTTPClient    *http.Client   // default: no redirects, public addresses only
	AllowInsecure bool           // also http, loopback and private addresses
	Timeout       time.Duration  // per request, default 10 s
	Attempts      int            // default 4
	Backoff       time.Duration  // before the second attempt, then doubled; default 1 s
	MaxBackoff    time.Duration  // the longest wait between two attempts; default 1 minute
	Metrics       Metrics        // OnDelivered, OnFailed, OnDropped: one per notification
	Queue         int            // per worker, default 256
	Workers       int            // default 4
	Logger        *slog.Logger   // failed and dropped deliveries
}
```

- **Lifecycle.** `NewSender` starts the workers. `Close` stops accepting, and waits until what is queued is delivered or given up on, or until its context ends; after that `Send` does nothing.
- **`Send` never blocks.** A notification for a full queue is dropped and logged.
- **Order.** Notifications for one callback URL are delivered one at a time, in the order they were sent, retries included. A webhook that is slow or down delays the callbacks that share its worker, and no others.
- **Retries.** A network failure, a 5xx, a 408 and a 429 are tried again, `Attempts` times in all. A webhook that sends `Retry-After` with such an answer is waited for that long, up to `MaxBackoff`. Any other answer outside 2xx, redirects included, is final.
- **At least once, not durable.** A webhook may see a notification twice; a notification is lost when its attempts are used up or the process stops.
- **Which URLs.** https only, no credentials in the URL, and with the default client only public addresses, checked on the address that is connected to, so that a name which resolves into the VTN's own network is refused. `AllowInsecure` lifts the scheme and address rules, for tests and closed networks. A client given in `HTTPClient` is used as it is.
- **`Verify`** sends `GET <url>?echo=<random>` and requires 200 with that value as the body. Its error is worded for the client that named the URL, and does not say what the VTN's network answered.

### In a client: `Handler`

```go
type Handler struct {
	BearerToken  string                                             // of the subscription; empty accepts anyone
	Notify       func(ctx context.Context, n openadr.Notification) error
	MaxBodyBytes int64                                              // default 1 MiB
}
```

An `http.Handler` for the callback URL of a subscription. `GET ?echo=` is
the VTN's verification and is answered without a token. `POST` is a
notification: the token is checked, the body decoded and `Notify` called.
`Notification.Object` holds a `Program`, `Event`, `Report`, `Subscription`,
`VEN` or `Resource` value, as `ObjectType` says.

- `Notify` returning an error answers 500, which asks the VTN to send the notification again. A body that is not a notification answers 400, which does not.
- `Notify` may be called concurrently and more than once for the same notification.
- The webhook has to be running before `CreateSubscription`: the VTN verifies it first.
- Set `BearerToken`, to a value made up for the subscription. Without it anyone who can reach the URL can feed the client events.

## Package `notification/mqtt`

`import "github.com/otfabric/go-openadr/notification/mqtt"`

The MQTT binding of notifications, for both sides, on this module's own
MQTT 3.1.1 client. The broker is not part of the module.

```go
type Broker struct { URL, ClientID, Username, Password string; TLS *tls.Config; Backoff time.Duration }

func NewPublisher(PublisherConfig) (*Publisher, error)     // PublisherConfig{Broker, Queue, Logger, Metrics}
func (*Publisher) Publish(topic string, body []byte)
func (*Publisher) Close(ctx) error

func NewSubscriber(SubscriberConfig) (*Subscriber, error)  // SubscriberConfig{Broker, Topics, Notify, Logger, Metrics}
func (*Subscriber) Ready(ctx) error
func (*Subscriber) Sessions() int
func (*Subscriber) Close() error
```

- **`Broker.URL`** is `mqtt://host` (port 1883) or `mqtts://host` (port 8883, TLS). `ClientID` has to be unique among the clients of a broker: two connections with one ID take each other's place.
- **Neither constructor waits for the broker.** The connection is made in the background and made again when it is lost, with `Backoff` doubling up to a minute. `Subscriber.Ready` waits for the first subscription.
- **`Publisher`**: `Publish` never blocks; notifications wait in a queue while the broker is away and are dropped, and logged, when it is full. They reach the broker in order, at QoS 1; one that failed is sent again on the next connection before anything after it. `Close` waits for the queue to drain or its context to end.
- **`Subscriber`**: `Notify` is called for one notification at a time, in the order the broker delivers them, and not after `Close` has returned. A message that is not a notification is logged and skipped.
- **What is missed.** Sessions are clean: what is published while a subscriber is disconnected is not delivered later. `Sessions` grows with every reconnect; a client that must not miss a change reads from the VTN when it does. A message may also arrive twice.
- **Topics** come from the VTN: `MQTTTopics` on the clients, with one of the twelve sets (`client.EventsForVEN(venID)`, `client.AllEvents()`, ...). `Topics.All` is a filter for the other three; `Create` is empty for a single object.
- **Authentication.** `GET /notifiers` says which method the broker wants. For `OAUTH2_BEARER_TOKEN`, put the announced user name in `Username` and the client's access token in `Password`; for `CERTIFICATE`, the certificate in `TLS`. The subscriber does not do this by itself.

## Package `notification/websocket`

`import "github.com/otfabric/go-openadr/notification/websocket"`

Notifications over a WebSocket, for the client side. Not part of OpenADR
3.1.0: see [Beyond OpenADR 3.1.0](#beyond-openadr-310) for what goes over
the wire.

```go
func NewSubscriber(SubscriberConfig) (*Subscriber, error)  // SubscriberConfig{Dial, Notify, KeepAlive, Backoff, Logger, Metrics}
func (*Subscriber) Ready(ctx) error
func (*Subscriber) Sessions() int
func (*Subscriber) Close() error

// in package client:
func (*VEN) NotificationStream(ctx) (*NotificationStream, error)   // likewise on *BL
func (*NotificationStream) Next() (openadr.Notification, error)
func (*NotificationStream) KeepAlive(interval time.Duration)
func (*NotificationStream) Close() error
```

- **`Dial`** is the `NotificationStream` method of a client: `websocket.SubscriberConfig{Dial: ven.NotificationStream, ...}`. The stream is opened with the client's HTTP client and token, at `notifiers/ws` under its base URL; a 401 is answered once with a fresh token, as for any call. The context given to `NotificationStream` bounds the opening, not the life of the stream.
- **`Subscriber`** keeps a stream open: it reconnects when the stream ends, with `Backoff` doubling up to a minute, and pings the VTN every `KeepAlive` (30 s), giving up a connection on which nothing is heard for two and a half times as long. `Notify` is called for one notification at a time, in the order the VTN sends them, and not after `Close` has returned.
- **What is missed.** A VTN keeps nothing for a client that is away, and disconnects one that reads too slowly. `Sessions` grows with every reconnect; a client that must not miss a change searches when it does.
- **What a client is sent** is what its subscriptions with `Mechanism: openadr.MechanismWebSocket` select, as it may see it: the same rules as for a webhook.
- **`NotificationStream`** is the stream itself, for a program that wants to do the reconnecting. `Next` passes over messages that are not notifications it knows; an error from it ends the stream. A VTN without WebSockets refuses the opening with an `*openadr.Error`: 501 from this module's VTN, 404 from another.

## Package `timeline`

`import "github.com/otfabric/go-openadr/timeline"`

The intervals of an event or report as a table. Nothing here does I/O
besides reading and writing CSV.

```go
type Row struct {
	IntervalID     int32
	Sub            int               // which of the sub-intervals of one payload
	Start, End     time.Time         // a zero End: no end
	RandomizeStart openadr.Duration
	Type           string
	Values         []openadr.Value
}
type Table []Row

func OfEvent(openadr.Event, Options) (Table, error)
func Of(period *openadr.IntervalPeriod, intervals []openadr.Interval, o Options) (Table, error)

type Options struct { Now time.Time; SubIntervals bool; Until time.Time }

func (Table) At(time.Time) Table
func (Table) Span() (start, end time.Time)
func (Table) Intervals() []openadr.Interval
func (Table) WriteCSV(io.Writer) error
func ReadCSV(io.Reader) (Table, error)
```

The rules it applies are those of the User Guide ("Event and Interval
Timing"):

- **Defaults.** An interval without a period, or without a start or a duration in it, takes them from the event; one without a start follows the interval before it.
- **"Now".** A start in year 1 (the specification writes `0001-01-01`) on the event, or on the first interval of an event without a default, is `Options.Now`, which is the current time when zero. On a later interval it means "after the previous one". The clients read both spellings of the User Guide for it, `0001-01-01` and `0001-01-01T00:00:00`, which are not the date-times the schema asks for.
- **No end.** A duration of `P9999Y` gives a row with a zero `End`. An interval cannot follow one that does not end: that is an error.
- **Sub-intervals.** With `SubIntervals`, a payload with several values becomes as many rows of equal length. A payload that holds a point is left whole. Without it, a row holds all the values of its payload.
- **The duration of the event** (`OfEvent` only). Shorter than the intervals, it leaves out what lies beyond it and cuts the row it ends in. Longer, it repeats the intervals. `P9999Y` repeats without end: the intervals are laid out once unless `Until` says how far to go. `Until` never extends an event.
- **`RandomizeStart`** is carried on the row and not applied: the table shows the times as announced.
- **Calendar durations** (`P1M`, `P1D`) are applied on the calendar, in the location of the start.
- **Errors.** An interval for which no start or no duration can be found, a negative duration, and a table of more than about a million rows (`ErrTooLarge`).

`Of` is the same for a list of intervals with a default period, which is
what a resource of a report carries.

`Intervals` is the way back: consecutive rows with one `IntervalID` become
one interval with a period of its own that spans them, and their rows of one
`Type` one payload. Rows that came from `Of`, with or without
`SubIntervals`, give intervals that lay out the same again.

**CSV.** A header line, then one line per row:

```
interval,sub,start,end,type,values,randomize_start
0,0,2030-01-01T00:00:00Z,2030-01-01T01:00:00Z,PRICE,0.17,
```

Times are RFC 3339; `end` is empty for a row without end; `values` is JSON,
one value as it is and several as an array; `randomize_start` is an ISO 8601
duration. `ReadCSV` wants the header and the columns `start`, `type` and
`values`, in any order, ignores columns it does not know, numbers the rows
that have no `interval`, and takes a cell of `values` that is not JSON as a
string.

## Package `discovery`

`import "github.com/otfabric/go-openadr/discovery"`

DNS-SD over Multicast DNS, as the specification describes it for VTNs on a
local network: service type `_openadr3._tcp`.

```go
func Browse(ctx, Options) ([]VTN, error)                // Options{Timeout, First}
func Advertise(Advertisement) (*Advertiser, error)      // Advertisement{Instance, Host, Port, BasePath, Version,
func (*Advertiser) Close() error                        //   LocalURL, ProgramNames, NoAuth, OpenAPIURL, Logger}

type VTN struct {
	Instance, Host string; Port int; Addrs []netip.Addr
	Version, BasePath, LocalURL, OpenAPIURL string; ProgramNames []string; RequiresAuth bool
	Text map[string]string
}
func (VTN) URL() string
```

- **`Browse`** asks on every network interface with multicast and an IPv4 address, waits `Timeout` (3 s) for answers, and returns the VTNs that gave a host, a port and a TXT record, by instance name. With `First` it returns at the first. Finding none is not an error. It asks in both ways there are: from a port of its own, as a one-shot resolver, which works beside the resolver of the system and needs no privilege; and, where the Multicast DNS port can be shared, from that port, because some responders (OpenLEADR's) answer to the whole network only.
- **`VTN.URL`** is the `local_url` the VTN announced, or else `https://host:port/base_path`. The host is a `.local` name; `Addrs` are for a system that cannot resolve those, which includes Go's own resolver on some platforms.
- **`RequiresAuth`** is false only when the VTN says `requires_auth=False`; a VTN that does not say is taken to want a token.
- **`Advertise`** announces a VTN until `Close`, which also says goodbye. `Port` is required; the host name defaults to the machine's, `Version` to 3.1.0, and `LocalURL` to `https://<host>.local:<port>/<base path>`. `NoAuth` announces a VTN with `AllowAnonymous`. It shares port 5353 with the resolver of the system, and fails where that is not possible.
- **Limits.** IPv4 multicast only (IPv6 addresses are announced and reported). No probing: a name that is already in use on the network is not detected. Not every network carries multicast: many cloud networks do not.
- **Tested with** Avahi, Apple's mDNSResponder and OpenLEADR, in both directions: see [INTEROP.md](INTEROP.md).
- **Trust.** Multicast DNS proves nothing about who answers. See [SECURITY.md](SECURITY.md).

## Beyond OpenADR 3.1.0

Two features are not in the specification. Both follow
[OpenLEADR](https://github.com/OpenLEADR/openleadr-rs), so that the two
implementations work together, and this module's clients are tested against
OpenLEADR for both. A VTN of this module has neither unless configured to;
a 3.1.0 client never meets them.

**Resource groups** (`server.Config.ResourceGroups`), proposed for a later
revision of OpenADR:

```go
type ResourceGroup struct { Metadata; Name string; Attributes []ValuesMap; Targets []string; Children []ResourceGroupChild }
type ResourceGroupChild struct { Type ResourceGroupChildType; ID string }   // ChildResource or ChildResourceGroup
```

| | |
|---|---|
| Paths | `/resource_groups`, `/resource_groups/{id}`; `resourceGroupName`, `targets`, `skip`, `limit` on a search |
| Object | `objectType` `RESOURCE_GROUP`; `resourceGroupName`, `targets`, `attributes`, `children: [{type, id}]` with `type` `ven_resource` or `resource_group` |
| Business Logic | reads all and writes: `(*BL).ResourceGroups`, `ResourceGroup`, `CreateResourceGroup`, `UpdateResourceGroup`, `DeleteResourceGroup` |
| A VEN | reads the groups that hold one of its own resources, directly or through other groups, and of their children only those resources and such groups: `(*VEN).ResourceGroups`, `ResourceGroup` |
| Rules | children have to exist (409 otherwise); names are not unique; a group may hold itself, and groups each other; deleting a resource or a group takes it out of the groups that held it |
| Notifications | none: a 3.1.0 subscription cannot name them |

Expect this to change when a revision of the specification adopts resource
groups.

**WebSocket notifications** (`server.Config.WebSocket`):

| | |
|---|---|
| Announced | `"WEBSOCKET": true` in the answer to `GET /notifiers` (`Notifiers.WebSocket`) |
| Subscribing | an entry of `objectOperations` with `"mechanism": "WEBSOCKET"` and no `callbackUrl` (`ObjectOperation.Mechanism`); `WEBHOOK`, or no mechanism, is a webhook as in 3.1.0 |
| Receiving | `GET /notifiers/ws` with the bearer token, upgraded to a WebSocket; each notification is one text message, the JSON of a 3.1.0 notification |
| `Notification.ID` | OpenLEADR adds an `id` of the operation to its notifications; this module reads it and does not send one |
| Connections | `WebSocketConfig{PingInterval, MaxPerClient, Queue}`: 4 per client (OpenLEADR: 1), then 409; a client that does not answer two pings, or lets 256 notifications queue up, is disconnected |
| Not kept | what happens while a client is not connected is not sent later |

## Package `auth`

`import "github.com/otfabric/go-openadr/auth"`

```go
type Validator interface { Validate(ctx, token string) (Identity, error) }
type Identity  struct { ClientID string; Scopes []string }

func (Identity) HasScope(string) bool
func (Identity) Allows(string) bool       // read_all includes the narrower read scopes
func (Identity) IsBusinessLogic() bool    // has read_all

func BusinessLogicScopes() []string
func VENScopes() []string

func NewIssuer(IssuerConfig) (*Issuer, error)             // Validator and http.Handler
func NewJWTValidator(JWTConfig) (*JWTValidator, error)   // Validator
```

A `Validator` is called once per request and must be safe for concurrent
use. It returns `ErrInvalidToken` for a token that is unknown, malformed or
expired (answered 401); any other error is answered 500. The `ClientID` it
returns is what the VTN writes into the objects a VEN creates, so it has to
be stable for a client across tokens.

`Issuer` is an in-memory token service for the client-credentials grant:
give it as `Config.Validator` and as `Config.TokenHandler`. It accepts the
credentials in the form body or as HTTP Basic authentication, issues opaque
tokens (one hour by default, `IssuerConfig.TokenTTL`), and narrows the scopes
to those a request asks for. Tokens do not survive a restart.

`JWTValidator` validates JSON Web Tokens signed by an identity provider:

```go
type JWTConfig struct {
	Keys          []JWTKey       // {ID, Key}: *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey, or []byte for an HMAC
	JWKSURL       string         // the provider's key set, fetched when needed
	HTTPClient    *http.Client
	Issuer        string         // required iss, when set
	Audiences     []string       // aud must name one, when set
	ClientIDClaim string         // default: client_id, then azp, then sub
	ScopeClaim    string         // default: scope, then scp
	Leeway        time.Duration
}
```

- **Algorithms:** RS256/384/512, ES256/384/512, EdDSA (Ed25519), HS256/384/512. The kind of key decides which it verifies: a public key is never used as an HMAC secret, and a token without a signature is never accepted.
- **Always checked:** the signature, and `exp`, which a token must have. `nbf` when present. `iss` and `aud` when configured: set `Audiences` whenever the provider also issues tokens for other services.
- **The key set** is fetched with the first token that needs it, and again when a token names a key that is not known, at most once a minute. Keys the provider withdraws stop working at the next fetch. While the provider is unreachable, known keys keep working; with no keys at all, `Validate` returns an error that is not `ErrInvalidToken`, and the VTN answers 500 rather than telling clients their tokens are bad. Symmetric keys are never taken from a key set.
- **The client ID** must be in the token: one without it is invalid, since the VTN could not tell whose objects are whose.

## Concurrency

- `client.VEN` and `client.BL` are safe for concurrent use. Calls are independent HTTP exchanges; only the fetching of a token is serialized.
- `server.Server` is safe for concurrent use, as `http.Handler` requires. A read by ID followed by a write is two steps on the store: two updates of one object at the same time both succeed, and the later one wins.
- `storage.Memory`, `storage.File`, `auth.Issuer`, `auth.JWTValidator`, `webhook.Sender`, `mqtt.Publisher`, `mqtt.Subscriber`, `websocket.Subscriber` and `discovery.Advertiser` are safe for concurrent use. A `client.NotificationStream` is read by one goroutine at a time, and may be closed from any. A `webhook.Handler` calls `Notify` from the goroutine of each request.
- The domain types are plain values. Copying one copies its slices by reference: do not modify an object that has been handed to a store.

## Limits

| Limit | Value | Where |
|-------|-------|-------|
| Objects per search | 50 | the specification; `limit` above it is answered 400 |
| Names, targets, payload types | 128 characters | the schema; checked by `Validate` |
| Object IDs | 1 to 128 characters of `[a-zA-Z0-9_-]` | the schema |
| Request body | 1 MiB | `server.Config.MaxBodyBytes` |
| Response body | 16 MiB | `client.Config.MaxResponseBytes` |
| HTTP exchange | 30 s | the default `http.Client` of the client |
| Numbers in payloads | `float64` | exact for integers up to 2^53 |
| Compression | answers from 1 KiB | `server`; off with `Config.DisableCompression` |
| WebSockets per client | 4 | `server.WebSocketConfig.MaxPerClient` |
| Notifications queued per WebSocket | 256 | `server.WebSocketConfig.Queue` |
| Message on a WebSocket | 4 MiB to a client, 64 KiB to the VTN | fixed |
| Wait between webhook attempts | 1 minute at most | `webhook.SenderConfig.MaxBackoff` |
| Rows of a table | about a million | `timeline.ErrTooLarge` |
