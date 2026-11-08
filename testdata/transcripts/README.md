# Recorded OpenADR 3 exchanges

OpenADR 3 is HTTP and JSON, so the corpus is transcripts, not packet
captures: ordered conversations between this module and another
implementation, each request with its answer, and the webhook and MQTT
notifications the other side sent. go-openadr's tests decode every JSON
body with the codec of its revision and encode it again, replay the
recorded VTN answers through the VEN and Business Logic clients, replay
the recorded client requests to the VTN, send the requests recorded
against other VTNs to this module's VTN and compare the answers, and
decode the specification's own examples. This is evidence from software:
no physical device, no production system, and no conformance claim.

## Sources and evidence levels

| Directory | Source | Revision | Level | Terms |
|---|---|---|---|---|
| `openleadr/` | [OpenLEADR openleadr-rs](https://github.com/OpenLEADR/openleadr-rs) v0.2.8 (Rust), VTN of OpenADR 3.1.0 | `ghcr.io/otfabric/openadr-interop-openleadr@sha256:8f5c8e55…` (openadr-interop v0.8.0) | D | Apache-2.0 OR MIT; recorded here |
| `openleadr301/` | OpenLEADR openleadr-rs v0.1.4, VTN of OpenADR 3.0.1 | `…-openleadr301@sha256:ce3c87d9…` | D | Apache-2.0 OR MIT; recorded here |
| `cbsigma/` | [cB-Sigma openadr](https://github.com/cB-Sigma/openadr) v0.2.0 (Go), VTN and VEN/BL client | `…-cbsigma@sha256:626f3742…` | D | Apache-2.0; recorded here |
| `elaadnl/` | [ElaadNL openadr3-client](https://github.com/ElaadNL/openadr3-client) 2.4.2 (Python), VEN and BL client | `…-elaadnl@sha256:48b02fd2…` | D | Apache-2.0; recorded here |
| `spec/` | [grid-coordination/openadr3-specification](https://github.com/grid-coordination/openadr3-specification), the OpenAPI documents of 3.1.0 and 3.0.1, and the cases built from them | commit `17b91725e0f07203574ddc946e28def31cb11e44` | E | The YAML documents declare Apache-2.0 (`info.license`) |
| `fixtures/` | [openadr-interop](https://github.com/otfabric/openadr-interop) `fixtures/baseline`, `baseline-301` | v0.8.1, `95eb60f` | E | MIT |

Level D: a software testbed, the reference implementations in containers
on loopback, pinned by image digest. Level E: specification-derived.
Nothing here is level A, B or C. **No genuine production VTN or VEN
transcript was found**: none of the OpenADR 3 projects or documents we
looked at (OpenLEADR, ElaadNL, cB-Sigma, the specification repository, the
OpenADR Alliance's public material) publishes recorded production traffic,
and none was made up. The Definition and the User Guide (copyright of the
OpenADR Alliance, no licence grant) are not committed; manifest.json gives
their SHA256.

[manifest.json](manifest.json) lists every file with its source, original
name and URL where it has one, size, SHA256, evidence level, revision,
peer, and for a transcript the operations and statuses it holds.
[SHA256SUMS](SHA256SUMS) checks the files (`make transcripts-verify`, in
CI). Every file is under 300 KB: there is no extended set.

## Names

`<client>_<vtn>_<operation>.json`, lower case, words joined by hyphens. The
client or the VTN is `go-openadr` or the peer with its version
(`openleadr-0.2.8`); the operation is the interop test that recorded it
(`workflow`, `errors`, `scenario`), with `-2` for a second VTN in one test.
A transcript has no original name: it is recorded here. The specification
files keep the names of their repository.

## How they are recorded

`make transcripts-record` (Docker) runs the interop suite with
`OPENADR_TRANSCRIPT_RECORD` set
([interop/transcript_record_test.go](../../interop/transcript_record_test.go)):

- a reference VTN is reached through a recording reverse proxy, which the
  VTN is also told is its public URL, so that what it announces
  (`/auth/server`) leads through it too; compression stays as the VTN
  chose it and is recorded (`bodyEncoding`);
- this module's VTN, used by a reference client, is wrapped by a recording
  handler; the test's own requests to it (loading the fixture) say so and
  are marked `origin: self`;
- the webhook receiver of this module records what a reference VTN sends
  it, a raw MQTT subscription what a reference VTN publishes.

`transcript_scenario_test.go` adds, for each reference VTN, a scenario of
requests written by hand (each step has a `note`) for what the interop
tests do not ask: the token endpoint's errors, access without or with a
wrong token, paging (`skip`, `limit`, out of range and malformed),
malformed and invalid bodies, content types, every lifecycle, foreign
keys, subscriptions, notifiers and MQTT topics, and the 3.0.1 forms.

Each step records the protocol version (per file), actor (`VTN`, `VEN`,
`BL`, `anonymous`, found from the token the request carries), origin
(`peer` or `self`), method, route template and path, query, the headers
that mean something (Accept, Accept-Encoding, Authorization,
Content-Encoding, Content-Type, User-Agent; and in answers Allow,
Cache-Control, Content-Encoding, Content-Type, Location, Retry-After,
WWW-Authenticate), status, request and response body, and as `state` the
IDs of the objects created and not deleted.

## Bodies

A JSON body is kept token for token: only the whitespace is the file's
(the transcript is indented). Number literals, member order, `null`,
duplicate members and escapes are as sent. `bodyLength` and `bodySHA256`
are those of the bytes on the wire after redaction and decompression. A
form is kept as its decoded fields, anything else as text.

## Redaction

Deterministic, by [internal/transcript/redact.go](../../internal/transcript/redact.go),
and listed per transcript in `redactions`:

| Value | Placeholder |
|---|---|
| A client secret of the fixture | `redacted-secret-<client>` |
| Any other `client_secret` | `redacted-secret-<n>` |
| An access or refresh token (answers, `Authorization: Bearer`) | `redacted-token-<n>`, numbered by first appearance |
| A subscription's `bearerToken` | `redacted-callback-token-<n>` |
| A `password` member | `redacted-password-<n>` |
| HTTP Basic credentials | the base64 of the client ID and the secret's placeholder |
| The VTN's URL (loopback and ephemeral port) | `http://vtn.invalid` |
| A webhook callback's URL | `http://callback.invalid` |
| A broker's URL | `mqtt://broker.invalid` |
| Any other loopback URL | `<scheme>://loopback.invalid` |

The same value always gets the same placeholder within a transcript, so
"the token issued in step 2 is the one sent in step 5" survives. What
changes meaning: a token's length and format (the placeholders are not
JWTs or hex), and the host and port of a URL. The replays put their own
URL where `vtn.invalid` stands, and configure their issuer with the
secrets' placeholders. Client IDs (`interop-bl`, `interop-ven-1`, ...)
are the published test values of the fixture and are kept: they tie
tokens to VENs. The fixtures' secrets are replaced the same way. There is
no personal data: the fixture is synthetic.

## Comparison rules

[internal/transcript/compare.go](../../internal/transcript/compare.go)
compares JSON semantically and tells apart:

- an absent member, a member that is `null`, and a member of another type
  (`"1"` and `1`, `null` and `0` differ);
- numbers by value (`1` and `1.0` are equal), arrays in order, objects by
  member;

and, where a rule says so:

- **IDs** (`id`, members ending in `ID` or `IDs` but `clientID`): a
  recorded ID and a replayed one must map one to one throughout a replay;
  IDs inside other strings (MQTT topics, a problem's detail) are rewritten
  by that mapping;
- **timestamps** (`createdDateTime`, `modificationDateTime`): RFC 3339 on
  both sides, values not compared;
- **equivalence**: date-times (those two and `start`) compare as instants
  (`+00:00` and `Z`, `.840` and `.84`), durations (`duration`,
  `randomizeStart`, `timeZoneOffset`) as ISO 8601 durations (`PT1H` and
  `P0Y0M0DT1H0M0S`);
- **tokens**: `access_token` non-empty on both sides, `expires_in` a
  number;
- in the differential replay, lists ordered by name first, since OpenADR 3
  does not order a search.

Every difference left is classified in a table, and a test fails on one
that is not, and on an entry that matches nothing:
[internal/transcript/known.go](../../internal/transcript/known.go) for
decoding and the client replay, `differentialKnown` in
[server/transcript_differential_test.go](../../server/transcript_differential_test.go)
for this module's VTN against the others.

## The specification cases

The OpenAPI documents carry examples on properties, not on whole messages.
[scripts/transcripts-spec-cases.py](../../scripts/transcripts-spec-cases.py)
(`make transcripts-spec`) builds an instance of every component schema from
them, in two variants (`defaults` takes a property's default, mostly
`null`; `populated` builds optional arrays from their items), and lists
what no example covers as `missing`. They are cases made from the
documents, not recorded traffic.

## Fuzz seeds

[scripts/transcripts-fuzz-seeds.py](../../scripts/transcripts-fuzz-seeds.py)
writes the bodies the peers sent, and the specification cases, as seeds
of `FuzzCodec` in `internal/wire/v310` and `internal/wire/v301`
(`testdata/fuzz/FuzzCodec/transcript-*`, compacted JSON).
