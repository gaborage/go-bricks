# ADR-156: Delivered-Empty Checks Find a Key Under Every Spelling Decode Accepts

**Status:** Accepted
**Date:** 2026-10-28
**Related:** [ADR-051](adr_051_delivered_empty_database_identity.md) · [ADR-078](adr_078_delivered_empty_allowedips.md) · [ADR-090](adr_090_env_reachable_section_names.md) · [ADR-104](adr_104_key_presence_recorded_at_merge_seam.md) · [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md) · [ADR-153](adr_153_retired_messaging_keys_refused.md)

## Context

mapstructure matches a struct field by its exact spelling first and falls back to
`strings.EqualFold`, so YAML `Host:` under `server.probes`, `allowedIPs:` under `debug`, and every
database identity key in any letter case decode into the config. The delivered-empty checks asked
the ADR-104 presence record for the lowercase spelling only. `server: {probes: {Host: ""}}`
therefore widened a loopback probe bind to `server.host` (ADR-120), `debug: {allowedIPs: ""}`
booted (ADR-078), and a root database section holding only mixed-case empty identity keys booted
database-free, while a named section failed with the generic "incomplete" error instead of
ADR-051's. The retired-key check (ADR-153) folded with `strings.ToLower`, which misses Unicode
simple folds that `EqualFold` matches, such as U+017F for `s`. Fixes #2210.

## Decision

1. **One key lookup answers every presence door.** `Config.deliveries` (`config/source.go`)
   returns every path an operator layer recorded that matches the key segment by segment under
   `strings.EqualFold`, and that the final tree still holds (ADR-104 eviction), each with its raw
   value, sorted by path. `delivered` and `deliveredAtOrUnder` are built on it, and `pathMatches`
   is the package's one case fold for keys. A preloaded default is never a match, and a hand-built
   `Config` literal has none.
2. **Emptiness is read at the matched path.** `server.probes.host` and `debug.allowedips` read the
   raw value at each delivered spelling, never a `Get` of the lowercase key, so `Host: 127.0.0.2`
   is kept. The key is refused when any delivered spelling is empty or null, whether the spellings
   share a layer or come from a base file and an overlay, whatever order a map yields them in. The
   error's `Field` is the lowercase key.
3. **Database identity keys.** ADR-051's section rule finds identity keys in any spelling, in root,
   named and tenant sections, and a named section reports ADR-051's message. A section that is
   configured is also refused when one identity key has two or more delivered spellings and any of
   them is empty or null, with the same message and the lowercase key. One empty identity key in a
   configured section is still accepted.
4. **Retired keys** (ADR-153) are matched per segment with `EqualFold`, through the same lookup.

## Alternatives considered

- **Lowercase keys at load.** Rejected. ADR-090 refuses an uppercase database, tenant or keystore
  name by checking it, not by normalizing it; lowercasing at load would turn those refusals into
  silent acceptance and would change keystore kids and header names.
- **Fold with `strings.ToLower`.** Rejected. It misses folds that mapstructure matches, so a
  spelling that decodes would still escape the check.
- **Fold the getters, `Exists` and `InjectInto` too.** Out of scope: they are not presence doors.
- **Refuse two keys that differ only by case.** That is a decode-time rule of its own (#2219).

## Consequences

- **Breaking.** A config that boots on v0.72.0 with a mixed-case empty or null
  `server.probes.host`, `debug.allowedips` or database identity key, or with two spellings of one
  of them where one is empty, now refuses to start.
- An empty string at a database `port` stays the decode-time numeric refusal ([ADR-074](adr_074_delivered_empty_numeric_config.md)), in any
  spelling.
- Unchanged blind spots: `config.LoadFromMap`, which skips `Validate`; hand-built literals; and
  dynamic-source tenant configs, which never pass through koanf.

## References

- `config/source.go`: `Config.deliveries`, `pathMatches`, `Config.delivered`, `Config.deliveredAtOrUnder`
- `config/delivered_empty.go`: `validateNoDeliveredEmptyKeys`
- `config/database_section.go`: `validateNoDeliveredEmptyDatabase`
- See [migrations.md](migrations.md) `[C73.13]`.
