# ADR-175: A Broker URL the AMQP Client Cannot Parse Fails Startup

**Status:** Accepted
**Date:** 2026-11-09
**Related:** [ADR-159](adr_159_one_credential_url_posture.md) (one credential URL posture)

## Context

Startup checked `messaging.broker.url`, and each static tenant's
`multitenant.tenants.<id>.messaging.url`, only for being non-empty. A URL that `amqp.ParseURI`
rejects (`?heartbeat=70000`, an unencoded `%` in the password, a non-AMQP scheme, a string with a
space) surfaced at the first dial, which the client retried forever. A service with consumers on
the control-plane key failed after the 30s readiness wait with the generic
`timeout waiting for AMQP client to be ready`. A publisher-only service logged a WARN and booted.
Under per-tenant messaging nothing dials at startup, so a malformed static tenant URL booted too.
A written port outside 1-65535 (`:99999`, `:0`) passes `ParseURI` and failed only at the dial.

The usual cause is a reserved character left unencoded in the credentials. The fix is in the
config, so the error belongs at startup, before anything is dialed.

## Decision

1. **Every statically configured AMQP URL must pass `amqp.ParseURI`**, the client's own parser
   (amqp091-go), and, when a port is written, carry one within 1-65535. Otherwise `config.Validate`
   fails before anything is dialed. One malformed static tenant URL fails the whole startup. An
   empty URL stays "not configured"; a whitespace-only one is a URL, and is refused.
2. **A URL resolved at runtime gets the same check.** When `messaging.Manager` resolves a key's URL
   from a `messaging.BrokerURLProvider` (a dynamic source), it checks the URL before the client
   factory runs. A refused URL returns the error for that key from `EnsureConsumers` or
   `Publisher`, and no client is built.
3. **The message is fixed text.** It names the config key (or the manager key), one reason class
   and the remedy, and carries no vendor error text and no part of the URL:
   - `config_invalid: messaging.broker.url <reason> percent-encode reserved characters in the username and password (url.UserPassword)`,
     and the same with `multitenant.tenants.<id>.messaging.url` for a static tenant.
   - `messaging: broker URL for key "<key>" <reason>: percent-encode reserved characters in the username and password (url.UserPassword)`.

   `<reason>` is one of `is not a valid AMQP URL`, `uses an unsupported scheme`,
   `carries a port out of range` or `carries a query parameter out of range`. The verdict is
   `ParseURI`'s own plus the port range; cheap probes only pick the class (a `url.Parse` failure
   or a space, a scheme other than `amqp`/`amqps`, a written port outside 1-65535), and any other
   `ParseURI` refusal falls in the last class. A rule the vendor adds later still refuses, under
   that class.
4. **The check lives in `internal/amqpurl`**, apart from `internal/credurl`, so the credential URL
   helper that `httpclient`, `database/oracle` and `migration/source/http` import stays free of the
   AMQP client. `config` gains an import edge to amqp091-go, already a direct module dependency
   with stdlib-only dependencies.

## Alternatives considered

- **A hand-copied rule set instead of `ParseURI`.** Rejected. It drifts from what the client
  actually dials.
- **Echo the vendor error.** Rejected. `ParseURI`'s errors quote the URL and its credentials.
- **Echo the offending scheme, as the stream URI check does.** Rejected. A schemeless credential
  string parses its head as the scheme, so the echo could print a username.
- **Validate only at the dial, or make the dial failure terminal.** Rejected. A 530 at the dial
  is transient, and the dial path keeps the 30s generic timeout as the only signal.
- **Warn and boot.** Rejected. A publisher-only service already booted with a WARN and failed every
  publish.
- **Skip the malformed tenant and boot the rest.** Rejected by the maintainer: one malformed
  static tenant URL aborts startup, as any other invalid tenant entry does.

## Consequences

- **Breaking**: a deployment whose broker URL, or one static tenant's URL, fails `ParseURI` or
  writes a port outside 1-65535 no longer starts, including a publisher-only service and a
  per-tenant deployment that booted before. A dynamic source's malformed URL fails that key's
  `EnsureConsumers` or `Publisher` call instead of retrying the dial.
- **Unchanged**: a well-formed but unreachable URL behaves as before (publisher-only still boots
  with the WARN); a leading-zero port (`amqps://h:05671/vh`) and a two-segment path (`amqps://u:p@h/a/b`) are accepted; an empty username is not
  refused; `messaging.streams.uri` keeps its own validation; credential URL rendering (ADR-159),
  retry and backoff do not move. A hand-built `messaging.NewAMQPClient` is not checked and still
  finds a `ParseURI` failure at the dial, rendered masked as before.
- A URL that parses but is read differently than intended is out of scope here.
- **Known and deliberately unhandled**: opaque and hostless shapes that `ParseURI` accepts
  (the scheme followed by `user:pw@host/vh` with no `//`, the bare scheme, the scheme and `//`
  alone, or `amqps:///vh`) stay accepted. `ParseURI` reads them with its defaults (`localhost` on
  the scheme's default port, its default credentials, vhost `/` unless a path is written), so a
  missing `//` dials localhost instead of failing. The rule here is `ParseURI`'s verdict;
  refusing a hostless URL would also refuse the documented `amqps:///vh` local form, and is left
  to a separate decision.

## References

- gaborage/go-bricks#2194
- `internal/amqpurl/amqpurl.go`: `Check`, `HintFor`
- `config/messaging_section.go`: `checkBrokerURL`; `config/multitenant_section.go`:
  `checkMultitenantTenantEntry`
- `messaging/manager.go`: `Manager.createAMQPClient`
- See [migrations.md](migrations.md) `[C74.2]`.
