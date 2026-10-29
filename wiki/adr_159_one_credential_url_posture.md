# ADR-159: One Credential-URL Posture

**Status:** Accepted
**Date:** 2026-10-28

## Context

`internal/credurl.RedactError` masks a connection URL in the error text of four doors: the AMQP
`connect()` dial error, the streams environment error (`safeEnvError`), the streams consumer and
publisher start errors (`safeStartError`), and the Oracle DSN error. It masked the password as
`xxxxx` and kept everything else: the username, even alone or beside an empty password, and the
query. go-ora reads `WALLET PASSWORD` from a DSN query, so the query is credential material too.
Input without `://`, such as an EZ Connect `user/pw@host` DSN, passed through verbatim.

The HTTP client and the migration control-plane source rendered URLs in their errors on their
own. httpclient's `executionError` dropped the userinfo of a refused redirect's Location only when
url.Parse accepted it, and kept its query and fragment; its request-build error carried url.Parse's
error raw. The migration source's `New` errors carried the base URL raw, its redirect guard
rendered `URL.Redacted()` (username and query kept), and its control-plane request error carried
net/http's raw Location.

The scan trusted url.Parse once it accepted the URL: an accepted URL never yields a `*url.Error`,
so a host:port-shaped authority was left as written. That premise does not hold. url.Parse ends
the authority at the first `/`, `?` or `#`, so an unencoded one inside a password moves password
text into the host, the port or the path. An error that echoed `amqp://user:1234/etPW@broker/vh`
showed it whole, and `amqp://user:s3cr@et?PW@broker/vh` masked only `s3cr`. On the `*url.Error`
path the rebuild rendered the URL url.Parse reported, which stops at a `#` cut from the password,
so the host never showed.

## Decision

**`internal/credurl` is the one renderer for a credential-bearing URL in error text, and it
renders one posture.** `Redact(raw) string` gives the posture for a raw URL. `RedactError`
rebuilds a buried `*url.Error`, and masks every other rendering of raw and every vendor quote, and
every delimiter-aligned vendor word, of raw or decoded-password text the posture drops, through
the same rules.

- The scheme, host, port and path are kept.
- The password becomes `xxxxx`. The username is kept only beside a non-empty password, and only
  when it holds no `@`, `/`, `?` or `#`: a lone userinfo (`amqp://tok@h`, the API-key-as-username
  shape), an empty password (`amqp://u:@h`) or a username holding one of those becomes `xxxxx@`.
  A lone userinfo followed by an `@` later in the path or query reads, under the last-`@` scan, as
  a username holding an `@`, so `oracle://tok@h:1521/XE@vhost` renders `oracle://xxxxx@vhost`. A
  password that starts with `@` counts as empty, since the real password may end at that `@`:
  `oracle://tok:@h:1521/XE@vhost` renders `oracle://xxxxx@vhost`. A username holding `/`, `?` or
  `#` has crossed out of the authority into path, query or fragment text:
  `oracle://host/XE?wallet=SECRET:abc@def` renders `oracle://xxxxx@xxxxx`.
- A non-empty query becomes `?xxxxx`; an empty one is dropped with its `?`. The fragment is
  dropped.
- The userinfo runs to the last `@` after `://`, wherever url.Parse would end it, whether it
  accepts the input or rejects it: `amqp://user:1234/Spring@broker/vh` renders
  `amqp://user:xxxxx@broker/vh`. When a `?` comes before that `@`, everything after the `@`
  renders as one `xxxxx`, since it may be query text.
- When url.Parse rejects input with no `@`, the mask runs from the first `:` after `//`, outside
  an IPv6 literal, to the next `/`, `?`, `#` or the end. So `amqp://user:pass/broker` (rejected as
  `invalid port ":pass" after host`) renders `amqp://user:xxxxx/broker`.
- On the `*url.Error` path, the URL url.Parse reported is judged with the `#` fragment it cut
  before reporting, whenever the raw URL extends it: the `@` that ends the userinfo can be in the
  cut.
- A `*url.Error` naming the stream client's rebuild of the URI around the decoded password
  (`<scheme>://<user>:` followed by a prefix of that password, cut at a `#` inside it, an empty
  prefix included) is judged as the configured URI, so it renders from that URI.
- Input without `://`, or with any of `:`, `/`, `?`, `#` or `@` before the first `://`, renders
  whole as `xxxxx`; empty input renders as the empty string.
- A `*url.Error` reason that quotes anything is replaced, which is the rule `RedactError` already
  applied.
- `RedactURLError(e, reason)` returns a copy of a `*url.Error` whose URL renders as `Redact`
  renders it, for doors that hand the error on as a `*url.Error`; `e` is never mutated. A
  transport reason is framework or peer text as often as URL text (the JOSE refusal quotes the
  peer name), so net/http's reasons that quote a redirect's Location or a response header line
  keep their words with every quoted value masked, and only another reason quoting a URL, or text
  of `e.URL` that `Redact` drops, becomes `reason`, the original still reachable through
  `errors.Is`/`errors.As`. Any other reason is kept as the same value.
- Any other double-quoted segment of the message, two or more bytes long, whose text the input
  or its decoded password holds and the rendering drops, becomes `"xxxxx"`. That covers amqp091's
  port and query checks, which quote a digit-led password or a query value outside any
  `*url.Error` (`strconv.ParseInt: parsing "xxxxx": value out of range`). Unquoted vendor text gets
  the same test word by word, as the last pass: a word is a run between value delimiters
  (`isValueDelimiter`), and one of two or more bytes is masked when the input or its decoded
  password holds it aligned, each end at an edge or touching an alignment delimiter
  (`isAlignDelimiter`, `holdsAligned`), and the rendering drops it. That covers the stream client's
  `multiple segments in URI path: /<password tail>@host` and a dial error naming a host made of
  password text. A percent-decoded query value is not checked.

**A door may render stricter, never looser:**

- `redactURLForLog` (httpclient request logs) drops userinfo, query and fragment.
- The httpclient and migration source error doors drop the whole userinfo (`StripUserinfo` on the
  `Redact` rendering).
- The logger filter masks the `broker_url` field whole by name.
- The PostgreSQL connector renders a fixed message and never the DSN.

| Door | Text | Renderer |
| --- | --- | --- |
| AMQP `connect()` | dial error, and the `Failed to connect to AMQP broker` log line carrying it | `RedactError` |
| Streams `safeEnvError` | environment error, the client's own URI-check text included | `RedactError` |
| Streams `safeStartError` | consumer and publisher start errors | `RedactError` |
| Oracle connector (`redactDSNError`) | DSN error | `RedactError` |
| httpclient `executionError` | transport error, a refused redirect's Location included | `RedactURLError`, userinfo dropped |
| httpclient `buildRequest` | request URL parse error | `RedactURLError`, userinfo dropped |
| migration `source/http` `New` | base URL parse and missing-host errors | `RedactURLError` and `Redact`, userinfo dropped |
| migration `source/http` redirect guard and `fetchPage` | refused redirect, control-plane request error | `Redact` and `RedactURLError`, userinfo dropped |

## Alternatives considered

- **Keep a renderer per door.** Rejected: each door's rule drifts into a posture of its own, and
  each misses the misparse differently.
- **Keep the username always.** Rejected: an API key passed as the username is a common shape.
- **A placeholder of its own for the query (`?<redacted>`).** Rejected: one placeholder for every
  masked part.
- **Refuse a misparsed userinfo before dialing.** Deferred to the open question below.

## Consequences

- An `@` that is legal unencoded in a path over-masks the host, since the scan cannot tell it from
  a misparsed password: `amqp://u:p@h/my@vhost` renders `amqp://u:xxxxx@vhost`. That is
  accepted: a masked host costs a debugging hint, never a secret. An `@` after the first `?` masks
  the whole tail instead, because a query value can hold credentials (go-ora's `WALLET PASSWORD`):
  `amqp://u:p@h/vh?connection_name=app@x` renders `amqp://u:xxxxx@xxxxx`. A password holding an
  unencoded `?` loses its host and path the same way. A `#` does not count: a password holding
  one keeps its host, and a fragment carries no credentials.
- A rejected URL with no credentials also loses its port, and with it the vendor's quoted reason:
  `amqp://broker:5672/vh%zz` renders `amqp://broker:xxxxx/vh%zz: invalid URL`. A port-shaped
  segment of a rejected URL may be password text, so the scan cannot keep it.
- `RedactError` masks a non-URL DSN wherever the vendor message repeats it. EZ Connect
  `user/pw@host` is now masked, and a TNS descriptor is over-masked. A very short DSN mangles the
  vendor text, since every occurrence of it is replaced; the mangling never reveals anything. The
  token pass then masks every aligned piece of such a DSN, or of a `connStr` descriptor written
  unencoded in a go-ora URL's query, wherever vendor text repeats it. go-ora refuses a non-URL DSN
  before dialing (`missing port in address`), so only the descriptor reaches a dial: a ping error
  loses its port, and its host where the error repeats it (`dial tcp xxxxx:xxxxx` for an
  IP-address `HOST`, `lookup xxxxx` for a name), and a lowercase `PROTOCOL=tcp` masks the `tcp`
  too. A descriptor percent-encoded, as `go_ora.BuildJDBC` writes it, is not checked.
- On the `*url.Error` path the host and path after the last `@` now show, read from raw across the
  cut fragment, where the cut URL used to end the render early. They are parts the posture keeps.
  The same holds for the stream client's rebuild cut inside the password: the start error renders
  the configured URI, so it names the configured host, not the leader or replica host the client
  rebuilt the URI around.
- The quoted and unquoted passes mask only text of two or more bytes, so a one-byte quote (the
  `"?"`, `"#"`, `"@"`, `":"` and `"/"` of amqp091's reserved-character hint) stays readable. Each
  such quote or word discloses one byte.
- A vendor word equal to an aligned piece of dropped text is masked, which tells a reader that
  piece was in the input. Query keys count, because the query is dropped whole and a bare query
  token (`?<token>`) sits where a key does. So amqp091's checks lose the parameter name:
  `heartbeat` and `channel_max` render `xxxxx is not an uint16`, and `connection_timeout` renders
  `xxxxx is not an integer`. When a `?` before the last `@` masks the whole tail, the host and path
  are dropped text too, and an echo of them is masked. A word that only occurs inside a longer
  word of the input (`in` in `Spring`) is not aligned and stays.
- A piece of dropped text that also occurs in the kept rendering stays, by pass design:
  `amqp://app:s3cr@co/Q9x@broker.example.com:5672/vh` leaves `lookup co`.
- The quoted and unquoted passes cost time quadratic in message length times URL length; this is
  bounded in practice, since AMQP reply-text is at most 255 bytes and the passes run only on a
  dial or start failure.
- A URL with no host after its password has no host delimiter, so it is indistinguishable from a
  URL that has one, and password text renders as host, port or path. `amqp://user:1234/broker`
  (a forgotten `@` and an all-digit password) is a valid host and port, the same shape as
  `amqp://h:5672/vh`, so it renders verbatim, as it did before. `amqp://user:QZ/XJK` renders
  `amqp://user:xxxxx/XJK`, and `amqp://user:pa@ss:QZXJ` renders `amqp://user:xxxxx@ss:QZXJ`. This
  class is accepted.
- A query value, such as an Oracle `WALLET PASSWORD`, no longer shows in a rendering of the URL.
  A vendor that echoes a query value it unescaped (`%XX`, `+`) still shows it; that echo is not
  covered.
- A username holding an `@`, such as the Azure-style `user@server`, is masked whole with its
  userinfo: `amqp://svc@corp:pw@h/vh` renders `amqp://xxxxx@h/vh`. That is accepted: the scan
  cannot tell it from a lone token stretched to a later `@`, and a masked username costs a
  debugging hint, never a secret.
- A password that really starts with an unencoded `@` loses its username the same way.
- At the error doors, with a `#` before the last `@`, the fragment text after that `@` renders as
  the host: `oracle://tok@h:1521/XE#f@c` renders `oracle://xxxxx@c`. That text is path or
  fragment text, never credential text, so this residual is accepted there.
- The `broker_url` and streams `uri` log fields, and the endpoint the streams start errors name,
  render through `RedactForLog`, so their own renderers (`redactAMQPURL`, `redactStreamURI`) are
  gone. `RedactForLog` renders a URL url.Parse rejects, one it reads with no host, or one with a
  `#` before its last `@` as `xxxxx`, and any other URL as `Redact` does. With no host to anchor
  on, or with url.Parse's host cut at that `#` while the scan reads past it, password text cannot
  be told from a host, port or path (`amqp://user:QZ/XJK`, `amqp://user:pa@ss:QZXJ`,
  `amqp://u@ser:#Q@ZJV8/vh`). Logs fan out to more sinks than an error, so these doors keep the
  old doors' whole mask for those shapes, and the `#` residual above is closed here:
  `amqp://user:pw@broker/vh#f@QZXJ` renders `xxxxx`. A URL with no userinfo no longer gains a
  fabricated `****:****@`. A misparsed password no longer shows:
  `rabbitmq-stream://app:2024/Sp/ring@broker:5552` names endpoint
  `rabbitmq-stream://app:xxxxx@broker:5552`. The logger filter still masks `broker_url` whole by
  name.
- At the httpclient and migration source doors, net/http's two reasons that quote response
  text, `failed to parse Location header "…"` and a transport's `malformed MIME header line: "…"`,
  keep their words with every quoted value rendered `"xxxxx"`. Any other reason that quotes a URL
  or dropped URL text renders `transport error detail withheld` (a request error) or
  `invalid URL` (a parse error). A replaced reason no longer answers `(*url.Error).Timeout()`; a
  timeout's reason quotes nothing, so it is never replaced.
  These doors scan only the `*url.Error`: an unquoted reason echoing URL text, such as
  a dial error naming a host url.Parse read from the password, shows as it did before.
- The two response-quoting reasons are recognized by net/http's wording, so a future net/http
  reason that quotes response text is not masked until it is listed.
- The migration redirect guard renders the Location unquoted, so its refusal is kept inside
  net/http's `*url.Error` rather than withheld for a quote.
- Tests, log queries and alerts that match the old error text stop matching. See `[C73.18]`.
- No public API changes (`credurl` is internal; `Redact`, `RedactForLog`, `RedactURLError` and
  `StripUserinfo` are new there), and no configuration key changes.

## Open question: refuse a misparsed URL before dialing

The posture fixes the render only. A misparsed AMQP URL still dials the host url.Parse read, which
is password text (`zq9x` from `amqp://user:s3cr@zq9x?PW@broker:5672/vh`). The DNS lookup sends that
text to the resolver. The unquoted pass masks the host in the dial error
(`lookup xxxxx: no such host`), but that is render-only: the lookup still happens. Refusing before
the dial would close that, but it is a breaking change (a URL that dials today would refuse to
start), and it has no exact trigger. `amqp://user:1234/Spring@broker/vh` (misparsed password) and
`amqp://h:5672/my@vhost` (host, port, `@` vhost) have the same shape, and the candidate heuristics
each either refuse a valid vhost or miss a misparse. Left open.

## References

- `internal/credurl/credurl.go`: `Redact`, `RedactForLog`, `RedactError`, `RedactURLError`,
  `StripUserinfo`, `maskURLError`, `maskRejectedAuthority`, `maskQuoted`, `maskRawTokens`,
  `rebuiltFrom`
- `httpclient/redirect.go`: `redactURLError`; `httpclient/client.go`: `executionError`,
  `buildRequest`
- `migration/source/http/source.go`: `New`, `redirectGuard`, `fetchPage`, `redactedURL`,
  `redactURLError`
- `messaging/amqp_client.go`: `connect`
- `messaging/streams/manager.go`: `safeEnvError`, `safeStartError`
- `database/oracle/connection.go`: `redactDSNError`
- See [migrations.md](migrations.md) `[C73.18]`.
