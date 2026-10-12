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
- [Package `auth`](#package-auth)
- [Concurrency](#concurrency)
- [Limits](#limits)

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
}
```

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
}
```

A `Server` is an `http.Handler` for the root of the API. It owns no
listener, no goroutine and no state besides its `Store`: starting, TLS,
timeouts and shutdown belong to the `http.Server` it is given to. To serve
it under a path, wrap it in `http.StripPrefix`.

- **`Webhooks`** turns subscriptions on. Without it, `/subscriptions` and `GET /notifiers` answer 501. It is an interface (`Verify`, `Send`) that `*webhook.Sender` implements; the sender has workers of its own, and the embedding program closes it on shutdown, after the `http.Server`.

- **`TokenURL`** is what clients are told, so it is the address as *they* see it, proxies and path prefixes included.
- **Without `TokenHandler`**, `POST /auth/token` answers 501, for a VTN whose tokens come from elsewhere.
- **IDs and timestamps** are the VTN's: 32 hexadecimal characters from `crypto/rand`, and `Now()` in UTC.
- **Logging** is silent without a `Logger`. With one, only failures that become a 500 are logged, with method and path, never bodies or tokens.

Which operations exist, who may call them and how the VTN reads the
specification where it leaves room is in [docs/CONFORMANCE.md](docs/CONFORMANCE.md).

- **`MQTT`** turns the MQTT binding on: `MQTTConfig{Publisher, URIs, Authentication, TopicPrefix}`. The publisher sends; `URIs` and `Authentication` are what `GET /notifiers` tells clients about the broker, so `URIs` are the broker's addresses as clients reach it. Without it the 12 topic operations answer 501. `*mqtt.Publisher` implements `MQTTPublisher`.
- **`GET /notifiers`** is served when either binding is configured, and says which.

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
```

A `Store` enforces what has to be decided atomically with a write, and
reports a violation as a `*ConstraintError`:

- program names and VEN names are unique, and a resource name within its VEN;
- an event refers to an existing program, a report to an existing event (when created, or moved to another event), a resource to an existing VEN, a subscription that names a program to an existing one;
- a program that has events, and a VEN that has resources, cannot be deleted. Reports outlive their event.

Everything about who may see or write what stays in the server, so every
backend behaves the same.

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
	Queue         int            // per worker, default 256
	Workers       int            // default 4
	Logger        *slog.Logger   // failed and dropped deliveries
}
```

- **Lifecycle.** `NewSender` starts the workers. `Close` stops accepting, and waits until what is queued is delivered or given up on, or until its context ends; after that `Send` does nothing.
- **`Send` never blocks.** A notification for a full queue is dropped and logged.
- **Order.** Notifications for one callback URL are delivered one at a time, in the order they were sent, retries included. A webhook that is slow or down delays the callbacks that share its worker, and no others.
- **Retries.** A network failure, a 5xx, a 408 and a 429 are tried again, `Attempts` times in all. Any other answer outside 2xx, redirects included, is final.
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

func NewPublisher(PublisherConfig) (*Publisher, error)     // PublisherConfig{Broker, Queue, Logger}
func (*Publisher) Publish(topic string, body []byte)
func (*Publisher) Close(ctx) error

func NewSubscriber(SubscriberConfig) (*Subscriber, error)  // SubscriberConfig{Broker, Topics, Notify, Logger}
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
- `storage.Memory`, `storage.File`, `auth.Issuer`, `auth.JWTValidator`, `webhook.Sender`, `mqtt.Publisher` and `mqtt.Subscriber` are safe for concurrent use. A `webhook.Handler` calls `Notify` from the goroutine of each request.
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
