# ADR-157: A Config Key Decode Would Read Under Another Spelling Fails Startup

**Status:** Accepted
**Date:** 2026-10-28
**Related:** [ADR-024](adr_024_config_key_flatsmush.md) · [ADR-090](adr_090_env_reachable_section_names.md) · [ADR-104](adr_104_key_presence_recorded_at_merge_seam.md) · [ADR-144](adr_144_dotted_keystore_entry_names.md) · [ADR-153](adr_153_retired_messaging_keys_refused.md)

## Context

`Load` decodes the merged koanf tree into `Config` with mapstructure, which matches a key to a
field name exactly first and otherwise by `strings.EqualFold`, a fold that also matches Unicode
simple folds such as U+017F (`ſ`) for `s`. Every other reader of the tree names a key by its
exact spelling: `Config.Exists`, the getters (`cfg.String("database.host")`), `InjectInto`, and
the observability bootstrap, which asks `b.cfg.Exists("observability")` (`app/bootstrap.go`) and
builds the no-op provider when it answers false. Two readings of one tree let a config boot green
while part of it was ignored:

- **A root section spelled another way.** `Observability:` decoded through the fold, but the
  bootstrap's exact lookup missed it, so the service ran the no-op telemetry provider. `App:`
  never reached the struct at all: the framework defaults seed a lowercase `app` (`app.name` is
  `gobricks-service`), mapstructure took that exact match, and the `App:` block was dropped. A
  section with no defaults (`Database:`) decoded into the struct, while every exact-name lookup
  read it as absent. Nothing logged or failed.
- **Two keys that differ only by case.** A map node holding `Port` and `port` gives mapstructure
  two candidates for one field. It takes the exact spelling when one exists, and otherwise the
  first `EqualFold` match the Go map yields, so one twin is dropped, possibly a different one per
  run. The twins can come from any layer: a lowercase framework default beats an operator's
  mixed-case override (`server: {Port: 9090}` ran on the default port).

Environment variables never produce either shape for a framework key on their own: `envVarToKey`
lowercases them. Fixes #2219.

## Decision

1. **A root key that folds to a framework section name must be spelled as it.** The sections are
   `configSections` (`config/config.go`): `app`, `server`, `database`, `databases`, `cache`,
   `log`, `messaging`, `multitenant`, `debug`, `source`, `scheduler`, `outbox`, `inbox`,
   `keystore`, `auth` and `observability`. A root key that `strings.EqualFold` matches to one of
   them without equaling it (`App:`, `Observability:`, `meſſaging:`) is refused. A root key that
   folds to no section (`Orders:`, `custom:`) is not judged.
2. **Two keys of one map node that differ only by case are refused** (the fold-twin rule). Any map
   node of the merged tree holding two keys that `strings.EqualFold` matches but that are spelled
   differently is refused, whatever layer each came from (framework default, YAML base, overlay,
   environment variable, or the same layer), and in data-keyed maps too:
   `observability.{trace,metrics,logs}.headers`, `custom.*`, keystore names, and maps inside
   sequences. At the root, a group that folds to a section name is rule 1's to report. Rule 2
   ships in the second PR under #2219 (atom `[C73.19]`), after rule 1 (`[C73.14]`).
3. **Both rules run at decode, so every load path refuses.** `configSource.decode` runs
   `checkKeySpelling` (`config/key_spelling.go`) before it unmarshals, so `config.Load`,
   `config.LoadFromMap`, and the `go-bricks-migrate` CLI, whose `--source-config` decodes through
   `LoadFromMap`, all refuse the tree; the CLI from its first release that pins this framework
   version. Unlike ADR-153's retired keys, `LoadFromMap` is not silent here: the refusal is about
   how decode reads the tree, not a `Validate` rule.
4. **The error names spellings, never a value.** Each is a `*config.ConfigError` of category
   `invalid`. Rule 1 reports one error per section, every wrong spelling sorted:
   `config_invalid: app is spelled App; framework section names are lowercase: rename App to app`.
   Rule 2 names every spelling of the key, sorted, tags `(framework default)` a spelling no
   operator layer delivered at or under that exact path, and gives the key with its twinned
   segment lowercased (a parent keeps the case it was written in: `orders.Api.url`), with its
   environment variable when the key round-trips through `envVarForKey` and no sequence lies on
   its path (`orders.Api.url` and `orders.routes[0].name` get none):
   `config_invalid: server.port is written in spellings that differ only by case: server.Port,
   server.port (framework default); keys are lowercase: write it once as server.port (SERVER_PORT)`.
5. **A lone mixed-case key below the root still decodes.** A key with no twin beside it, such as
   `Keys` in `keystore: {Keys: …}`, has one reading, the field it folds to, and keeps it; a lone
   header name keeps its case. A mixed-case key beside a framework default of the same name is a
   twin (rule 2): `debug: {allowedIPs: …}` sits beside the default `debug.allowedips`. ADR-144's and ADR-153's "matched case-insensitively" rules hold below the
   root as written; at the root, a section spelled another way no longer reaches them.

## Why refuse instead of normalize

ADR-090 §2 pins "check, not normalize": the framework does not rename an operator's key behind
their back. Lowercasing every key at load would also turn ADR-090's refusal of an uppercase
`databases`, `multitenant.tenants` or `keystore.keys` name into silent acceptance, change a
keystore name that is the wire kid verbatim (ADR-144), and change a header name, which is a
protocol identifier the framework does not own.

## Alternatives considered

- **Lowercase keys at load.** Rejected, for the reasons above.
- **mapstructure `ErrorUnused`.** Rejected. `Load` reads the environment with no prefix, so every
  host variable (`PATH`, `HOME`) is an unused key, and so is every consumer key read through
  `InjectInto` or a getter.
- **Refuse every key that is not lowercase.** Rejected. A lone mixed-case key below the root has
  exactly one reading, and a header name's case is the operator's; refusing them breaks configs
  that are not ambiguous.

## Consequences

- **Breaking.** A config whose root carries a framework section spelled another way now fails
  startup, on `config.Load` and on `go-bricks-migrate --source-config` alike. Renaming it makes
  values that were ignored take effect: `App: {name: orders}` ran as `gobricks-service`, and
  `Observability:` turns telemetry on once renamed.
- **Breaking, rule 2.** A config holding fold twins anywhere fails startup. The common case is a
  mixed-case override of a defaulted key: `server: {Port: …}` and `log: {Level: …}` were
  silently ignored for the default, and `debug: {allowedIPs: …}`, a list, `[]`, `""` or null
  alike, decoded beside it. A YAML key beside the variable that reaches its lowercase spelling
  fails too: `server: {probes: {Port: 9090}}` with `SERVER_PROBES_PORT`, or a header
  `Authorization:` with `OBSERVABILITY_TRACE_HEADERS_AUTHORIZATION` (or `.metrics.`/`.logs.`),
  each two map entries before.
- Environment variables alone trip neither rule, since they are lowercased before they reach the
  tree; one can still be the lowercase twin of a mixed-case YAML key.
- Blind spots: a hand-built `*config.Config` handed to `app.NewWithConfig` or `Builder.WithConfig`,
  which is never decoded, and a dynamic tenant source, which never passes through koanf.

## References

- `config/key_spelling.go`: `checkKeySpelling`, `misspelledSections`, `sectionNamed`,
  `caseTwinErrors`, `caseTwinError`, and `fromDefaults`, which asks `Config.deliveries`
  (ADR-156) whether an operator layer delivered a spelling
- `config/config.go`: `configSource.decode`, `configSections`
- `config/source.go`: `sameSegment`, the package's one case fold for keys
- `app/bootstrap.go`: the observability bootstrap's `Exists` lookup
- gaborage/go-bricks#2219
- See [migrations.md](migrations.md) `[C73.14]` and `[C73.19]`.
