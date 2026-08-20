# ADR-073: The `TestKey*` config-key constants are removed, not corrected

- **Status**: Accepted
- **Date**: 2026-08-20
- **Related**: [ADR-052](adr_052_remove_jose_policy_registry.md) and [ADR-053](adr_053_remove_server_test_timeout_constants.md) (the dead-exported-surface precedent this follows) · `wiki/migrations.md` atom `[C401.1]` (the flat-smush rename, which corrected two values in this file and had no mandate over the rest)

## Context

`config/testkeys.go` exported 33 constants naming configuration keys —
`TestKeyCacheEnabled = "cache.enabled"` and so on — with a header saying they
existed "to eliminate string literal duplication and provide type-safe
references" in tests.

Nothing used them. Not one of the 33 has a call site anywhere in the repository:
not in `config`'s own tests, not in any other package's, not in the tools module.
The file has been a self-contained island for its whole life.

Worse, five of the constants name keys the loader does not read.

`TestKeyDatabaseConnectionString` says `database.connection_string`. The real
koanf tag is `connectionstring`, one word — and has been since two months BEFORE
this file was written. The constant was never correct: the key it names had
already been retired when someone typed it.

The four broker constants — `TestKeyMessagingBrokerHost`, `Port`, `User`,
`Password` — name `messaging.broker.host`, `.port`, `.username`, `.password`.
Those keys have never existed. `BrokerConfig` has carried exactly two fields for
its entire history, `url` and `virtualhost`; there was no rename, no migration,
no earlier spelling. The constants describe a schema the framework never had.

That is the part that makes this more than tidying. A test written against one of
those five sets a key the loader never reads, gets the zero value, and passes —
proving nothing, while looking like coverage. A constant is supposed to be the
defence against exactly that typo.

## Decision

Delete `config/testkeys.go`. No replacement surface.

The alternative shapes all keep a facility no one uses, and the two candidates
were weighed:

**Fix the five values and keep the file.** Corrects the lie but preserves 33
exported symbols with zero call sites, and each one is a promise to keep it in
step with the schema forever. Nothing enforces that promise, which is the whole
lesson here: `connection_string` was wrong the day it was written and stayed
wrong for nine months, because a constant nobody calls is checked by nothing —
not the compiler, not a test, not the loader.

**Deprecate the five and add corrected siblings.** Doubles the surface to retire
a facility that has never been used once. This project's stated position is that
obsolete paths are removed rather than shimmed.

Tests that want a config key inline the string. That is what every test in the
repository already does, and it puts the key literal next to the assertion that
depends on it, where a wrong value is visible rather than hidden behind a name
that reads as authoritative.

## Consequences

**Positive.** Thirty-three exported symbols leave the API surface, and five
statements about the config schema that were false leave the repository. Nothing
in the framework changes behaviour; no key is renamed and no loader path moves.

**Negative.** This is apidiff-INCOMPATIBLE: a consumer importing
`config.TestKeyServerPort` no longer compiles. The break is compiler-caught,
which is the good kind — nothing fails silently. Documented as `[C60.14]`, whose
atom carries the correction table so that a consumer inlining a literal does not
inline one of the five wrong values on the way out.

**Neutral.** The three `custom.api.*` constants named keys that were never
framework schema at all. Two were fixtures for the config-injection tests; the
third, `custom.api.retries`, matches nothing in the tests — the injection test
uses `custom.api.max.retries` — and appears only in the README's example. They go
with the rest; a fixture key belongs in the test that uses it.
