# ADR-153: Retired Messaging Keys Fail Startup Instead of Being Ignored

**Status:** Accepted
**Date:** 2026-10-25
**Related:** [ADR-024](adr_024_config_key_flatsmush.md) · [ADR-064](adr_064_app_validates_every_config.md) · [ADR-091](adr_091_streams_opt_in_registration.md) · [ADR-104](adr_104_key_presence_recorded_at_merge_seam.md) · [ADR-144](adr_144_dotted_keystore_entry_names.md)

## Context

`MessagingConfig` carried four fields that no framework code ever read:
`Broker.VirtualHost` (`messaging.broker.virtualhost`), `Routing.Exchange` and `Routing.Key`
(`messaging.routing.exchange`, `messaging.routing.key`), and `Headers` (`messaging.headers`).
The AMQP client dials `messaging.broker.url` verbatim, so the vhost is the URL path, or `/` when
the path is empty. A path-less URL beside `virtualhost: orders` therefore connected to `/`, and
nothing reported it. Exchange, routing key and headers are set per publisher, on
`messaging.PublisherDeclaration`. Even so, `config.example.yaml`, `.env.example` and `llms.txt`
advertised all four keys. `llms.txt` also promised a default exchange and headers added to every
published message, and neither exists. Fixes #2193.

## Decision

1. **The fields are removed.** `BrokerConfig.VirtualHost`, `MessagingConfig.Routing` (with
   `RoutingConfig`) and `MessagingConfig.Headers` are deleted, so a hand-built literal that sets
   one stops compiling.
2. **Any delivery of a retired key fails startup.** `validateNoRetiredKeys`
   (`config/retired_keys.go`) runs in `normalize`, beside the delivered-empty rejections and
   before any shaping. It reads the ADR-104 presence record. A key counts when YAML, an overlay or
   the environment delivered it, whatever the value: an empty string and a YAML null are template
   copies too. `messaging.headers` is a map, so any key delivered under it counts, and so does a
   null written at `messaging.headers` itself, because that records the section path. Paths are
   matched case-insensitively, as mapstructure matched the fields. The messaging normalizer could
   not make this check: it sees the decoded struct, not the tree.
3. **The error names the key and the fix, never a value.** Each delivered key gets one
   `*ConfigError` (`config_invalid`), and the errors are joined in key order, so a verbatim
   template copy is fixed in one pass. `Field` is the key. `Action` says to remove it, names its
   environment variable, and says where the setting goes. For the vhost that is the URL path,
   `amqp://host:port/<vhost>`, with each `/` in the name encoded as `%2F`. For the other three it
   is `Exchange`, `RoutingKey` or `Headers` on `messaging.PublisherDeclaration`. The check reads
   presence only, so no configured value can reach the message: not the broker URL and not its
   credentials (#2192).
4. **`LoadFromMap` stays silent.** It skips `Validate` by design, so
   `go-bricks-migrate --source-config` decodes such a file without comment. Every path that
   validates refuses it: `Load`, `app.NewWithConfig` and `Builder.WithConfig` (ADR-064).

## Why this departs from ADR-024

ADR-024 renamed 21 keys and accepted that the old spellings "fall back to defaults (silently, as
koanf has no unknown-key error)". That was tolerable for a rename, because the new key existed
and the old spelling fell back to a documented default. A retired key has no new home in the
config. A vhost left in `virtualhost` connects the service to a different vhost, and an exchange
or a header map configures nothing. Two later decisions refuse this shape. ADR-091 fails startup
on a `messaging.streams.uri` left without its import, because "a leftover URI must not boot as a
silent no-op". ADR-144 refuses config shapes that load green while dropping a value. A retired key
is the same case: config an operator wrote that the framework would otherwise drop.

## Alternatives considered

- **Honor the keys.** Rejected. A second vhost source beside the URL gives two answers to one
  question, and a process-wide exchange or header default would sit beside the per-publisher
  declaration that already owns them.
- **Warn and boot.** Rejected. An operator reads a startup WARN after the wrong vhost has already
  served traffic.
- **A general unknown-key gate.** Out of scope. Every consumer `InjectInto` key is unknown to
  `Config`, so that gate needs its own decision.
- **Check inside `normalizeMessaging`.** Rejected. It receives `*MessagingConfig` and cannot see
  presence.

## Consequences

- **Breaking.** A deployment that copied a v0.72.0 template verbatim matches, including an
  environment variable rendered empty (`MESSAGING_BROKER_VIRTUALHOST=`). Moving a non-default
  vhost into the URL path CHANGES the vhost the service connects to, because it connected to the
  URL's vhost before.
- Hand-built literals that set a removed field, and code that names `RoutingConfig`, stop
  compiling.
- Blind spots: `LoadFromMap` (by design), and a dynamic tenant source, which never passes through
  koanf.

## References

- `config/retired_keys.go`: `validateNoRetiredKeys`, `retiredKeys`
- `config/phases.go`: `normalize`
- `config/source.go`: `Config.deliveredAtOrUnder`, beside `Config.delivered` (the ADR-104 presence door)
- See [migrations.md](migrations.md) `[C73.7]`.
