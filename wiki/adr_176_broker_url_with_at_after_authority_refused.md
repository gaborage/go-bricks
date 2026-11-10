# ADR-176: A Broker URL With an Unencoded `@` After Its Authority Is Refused

**Status:** Accepted
**Date:** 2026-11-09
**Related:** [ADR-159](adr_159_one_credential_url_posture.md) (one credential URL posture),
[ADR-175](adr_175_malformed_broker_url_fails_startup.md) (a malformed broker URL fails startup)

## Context

`url.Parse` ends the authority at the first `/`, `?` or `#`. A password holding one of those
unencoded is split without error: password text becomes the host, port, vhost, query or fragment,
and the real `@host` lands after the authority. With user `user`, password `s3cr@zq9x?PW` and
host `broker:5672`, an AMQP URL reads host `zq9x` and query `PW@broker:5672/vh`; `amqp://user:1234/Spring@broker/vh` reads host `user`,
port `1234`, vhost `Spring@broker/vh` and credentials guest/guest;
`rabbitmq-stream://app:2024/Spring@broker:5552` reads host `app`, port `2024`. Config validation,
ADR-175's `ParseURI` check included, accepted all of them. The client dialed the misread host: a
DNS lookup of password text, or a login against the wrong host with the password tail as the
vhost. ADR-159 masked only the rendered error; it left this question open.

There is no exact trigger. `amqp://h:5672/my@vhost` (host, port, `@` vhost) has the same shape as
`amqp://user:1234/Spring@broker/vh` (misread password). The one mark the split always leaves is an
`@` after the authority.

## Decision

1. **A broker URL or stream URI whose raw path, raw query or fragment holds an unencoded `@` is
   refused, and nothing is dialed or resolved for it.** An `@` inside the userinfo stays legal
   (`amqp://user:pa@ss@broker/vh` parses as meant), as do `amqp://u:p%2Fq%3Fr%23s%40t@h:5672/%2f`
   and `rabbitmq-stream://user:pass@broker:5552/%2f`. A legitimate `@` in a vhost or query value
   must be written `%40`: `amqp://u:p@h/my@vhost`, `amqp://h:5672/my@vhost` and
   `amqp://u:p@h/vh?certfile=/a@b.pem` are refused, and their `%40` forms accepted (maintainer
   decision 2026-11-09).
2. **One predicate, four doors.** `credurl.CheckAuthority` judges the ESCAPED path, the raw query
   and the escaped fragment, so `%40` stays legal. A URL `url.Parse` rejects is not its question;
   that is ADR-175's, or the dial's. It runs at:
   - `config.Validate`, for `messaging.broker.url`, every static
     `multitenant.tenants.<id>.messaging.url` and `messaging.streams.uri`; startup fails. For the
     broker URLs it runs first inside `amqpurl.Check`, ahead of ADR-175's classes, so
     `amqp://user:0123456/zq9x@broker/vh` is reported as the misread (the cause), not as a port out
     of range. For the stream URI it runs before the scheme check, so a credential head is never
     echoed as a scheme.
   - `messaging.Manager`, when it resolves a broker URL for a key (dynamic `BrokerURLProvider`
     sources included): `EnsureConsumers` or `Publisher` returns the error, and no client is built.
   - The AMQP client's connect step, for a client built with `messaging.NewAMQPClient`: the dial
     function is never invoked. The existing retry loop and backoff are unchanged; the refusal is
     logged at ERROR per retry like any connect failure and is not terminal.
   - `streams.Manager.Start`, before the environment is created and before the canceled-context
     message, which renders the endpoint.
3. **The message is fixed text** and carries no part of the URL. `<hint>` is
   `percent-encode '/', '?', '#' and '@' in the username and password (url.UserPassword), and write an '@' in the vhost or a query value as %40`:
   - `messaging config: config_invalid: messaging.broker.url carries an unencoded '@' after its authority <hint>`,
     the same under `multitenant config: tenants: ` with `multitenant.tenants.<id>.messaging.url`,
     and under `messaging config: ` with `messaging.streams.uri`.
   - `messaging: broker URL for key "<key>" carries an unencoded '@' after its authority: <hint>`.
   - `messaging: broker URL carries an unencoded '@' after its authority: <hint>` (connect step).
   - `streams: stream URI carries an unencoded '@' after its authority: <hint>`.

## Alternatives considered

- **No userinfo plus a colon before a later `@`.** Rejected. It misses `s3cr@zq9x?PW@`, and
  refuses `amqp://h:5672/my@vhost` anyway.
- **An implausible host (no `.`, not bracketed).** Rejected. It refuses
  `amqp://u:p@rabbitmq/my@vhost`.
- **Warn only in `Config.Validate`.** Rejected. It misses hand-built clients and does not stop the
  lookup.
- **Require `%40` in the vhost and the query (chosen).** Exact, at the cost of breaking every valid
  unencoded `@` after the authority.

## Consequences

- **Breaking**: a broker URL, static tenant URL or stream URI with an unencoded `@` after its
  authority no longer starts, including one whose `@` sat in a valid vhost or query value. A
  dynamic source's such URL fails that key's `EnsureConsumers` or `Publisher` call; a hand-built
  client never dials it and keeps retrying the refusal.
- **Unchanged**: rendering (ADR-159 still masks; the pre-dial `broker_url` log field keeps its
  posture); retry and backoff; opaque and hostless shapes and URLs `url.Parse` or `amqp.ParseURI`
  reject, which stay ADR-175's domain; an empty username with a password and leading-zero ports,
  which stay accepted. Oracle and HTTP credential URLs are out of scope.

## References

- gaborage/go-bricks#2264
- `internal/credurl/authority.go`: `CheckAuthority`, `ErrAtAfterAuthority`, `AuthorityHint`
- `internal/amqpurl/amqpurl.go`: `Check`, `HintFor`
- `config/messaging_section.go`: `checkBrokerURL`, `checkMessagingStreams`;
  `config/multitenant_section.go`: `checkMultitenantTenantEntry`
- `messaging/manager.go`: `Manager.createAMQPClient`; `messaging/amqp_client.go`:
  `AMQPClientImpl.connect`; `messaging/streams/manager.go`: `Manager.Start`
- See [migrations.md](migrations.md) `[C74.3]`.
