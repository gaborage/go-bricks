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
rebuilds a buried `*url.Error`, and masks every other rendering of raw, through the same rules.

- The scheme, host, port and path are kept.
- The password becomes `xxxxx`. The username is kept only beside a non-empty password, and only
  when it holds no `@`: a lone userinfo (`amqp://tok@h`, the API-key-as-username shape), an empty
  password (`amqp://u:@h`) or a username holding an `@` becomes `xxxxx@`. A lone userinfo
  followed by an `@` later in the path or query reads, under the last-`@` scan, as a username
  holding an `@`, so `oracle://tok@h:1521/XE@vhost` renders `oracle://xxxxx@vhost`. A password
  that starts with `@` counts as empty, since the real password may end at that `@`:
  `oracle://tok:@h:1521/XE@vhost` renders `oracle://xxxxx@vhost`.
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
- Input without `://`, or with any of `:`, `/`, `?`, `#` or `@` before the first `://`, renders
  whole as `xxxxx`; empty input renders as the empty string.
- A `*url.Error` reason that quotes anything is replaced, which is the rule `RedactError` already
  applied.

**A door may render stricter, never looser:**

- `redactURLForLog` (httpclient request logs) drops userinfo, query and fragment.
- The logger filter masks the `broker_url` field whole by name.
- The PostgreSQL connector renders a fixed message and never the DSN.

| Door | Text | Renderer |
| --- | --- | --- |
| AMQP `connect()` | dial error, and the `Failed to connect to AMQP broker` log line carrying it | `RedactError` |
| Streams `safeEnvError` | environment error | `RedactError` |
| Streams `safeStartError` | consumer and publisher start errors | `RedactError` |
| Oracle connector (`redactDSNError`) | DSN error | `RedactError` |

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
  vendor text, since every occurrence of it is replaced; the mangling never reveals anything.
- On the `*url.Error` path the host and path after the last `@` now show, read from raw across the
  cut fragment, where the cut URL used to end the render early. They are parts the posture keeps.
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
- With a `#` before the last `@`, the fragment text after that `@` renders as the host:
  `oracle://tok@h:1521/XE#f@c` renders `oracle://xxxxx@c`. That text is path or fragment text,
  never credential text, so this residual is accepted.
- The `broker_url` and streams `uri` log fields, and the endpoint the streams start error names,
  still render through their own renderers (`redactAMQPURL`, `redactStreamURI`), which keep a lone
  username and render a misparsed password as written; the logger filter masks `broker_url` whole
  by name.
- Tests, log queries and alerts that match the old error text stop matching. See `[C73.18]`.
- No public API changes (`credurl` is internal; `Redact` is new there), and no configuration key
  changes.

## Open question: refuse a misparsed URL before dialing

The posture fixes the render only. A misparsed AMQP URL still dials the host url.Parse read, which
is password text (`zq9x` from `amqp://user:s3cr@zq9x?PW@broker:5672/vh`). The DNS lookup sends that
text to the resolver, and the dial error names that host as the vendor wrote it. Refusing before
the dial would close that, but it is a breaking change (a URL that dials today would refuse to
start), and it has no exact trigger. `amqp://user:1234/Spring@broker/vh` (misparsed password) and
`amqp://h:5672/my@vhost` (host, port, `@` vhost) have the same shape, and the candidate heuristics
each either refuse a valid vhost or miss a misparse. Left open.

## References

- `internal/credurl/credurl.go`: `Redact`, `RedactError`, `maskURLError`, `maskRejectedAuthority`
- `messaging/amqp_client.go`: `connect`
- `messaging/streams/manager.go`: `safeEnvError`, `safeStartError`
- `database/oracle/connection.go`: `redactDSNError`
- See [migrations.md](migrations.md) `[C73.18]`.
