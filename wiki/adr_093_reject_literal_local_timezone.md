# ADR-093: The Literal `Local` Timezone Is Refused; `"-"` Is the Only Documented Opt-Out

- **Status**: Accepted
- **Date**: 2026-09-01
- **Related**: [ADR-016](adr_016_database_session_timezone.md) (the database session
  timezone, whose "accepted identifiers" list named `Local`), [ADR-023](adr_023_scheduler_timezone.md)
  (the scheduler timezone, whose `"-"` sentinel this decision makes the only host-local
  opt-in), [ADR-064](adr_064_app_validates_every_config.md) (why `config.Validate` is the
  single place the refusal has to live)

## Context

`normalizeIANATimezone` is the one validator behind every timezone key — `scheduler.timezone`,
`database.timezone`, each `databases.<name>.timezone` and each
`multitenant.tenants.<id>.database.timezone` — on every path that validates a config: `config.Validate`
(which every application construction path runs, ADR-064) and the `ApplyDatabasePoolDefaultsForKey`
door a dynamic `DBConfigProvider` and the `go-bricks-migrate` CLI go through. It defaults an empty
value to `UTC`, passes the `"-"` sentinel through, and probes everything else with `time.LoadLocation`.

Go's loader special-cases exactly one spelling before it consults the IANA database: the
string `Local` returns `time.Local`, the host process's zone. So `Local` passed validation,
and ADR-016 even listed it among the accepted identifiers. That made it a second, undocumented
spelling of the deliberate opt-in, and not an equivalent one. On the scheduler key it produced
the host-dependent wall-clock schedules ADR-023 exists to make explicit. On a database key it
was a third behavior: `"-"` leaves the session on the server's default zone, an IANA name sets
that zone on every session, and `Local` handed the application host's zone to the driver.

`local` and `LOCAL` were never affected — the loader only special-cases the exact spelling, so
those fail as unknown zones already.

## Decision

The shared normalizer refuses exact `Local` before the `LoadLocation` probe, on every key it
serves, with a `*ConfigError` whose `Field` is the key and whose message steers to the
documented form: `timezone "Local" is not accepted; use "-" for the documented opt-out or an explicit IANA zone`. The
`Action` is the valid-options list every other timezone rejection renders.

Nothing else moves. `"-"`, `UTC`, empty and every IANA name normalize as before; what `"-"`
means at runtime is unchanged on both kinds; the case variants keep failing for the reason
they already did, and a test pins that so a future loader change cannot widen the door
silently.

## Consequences

**Positive.** Host-local time has one spelling, the documented one, on every key. A grep for
`"-"` finds every deployment that opted in; nothing opts in by accident.

**Negative.** A deployment carrying `timezone: Local` booted on v0.61.0 and is refused on
v0.62.0 — at startup for a static config, at the tenant's first connection acquisition for a
dynamic `DBConfigProvider` record, and before dialing in `go-bricks-migrate` (the timing C62.1
documents per path). The fix is a one-line rewrite, and the error names the key and the
replacement.

**Neutral.** The framework keeps validating through `time.LoadLocation`; the refusal is a
single string comparison ahead of it, not a second timezone grammar. `scheduler.Module.Init` does
not re-check the value: it requires a `config.Validate`-normalized config (ADR-075) and resolves
the zone it is handed, so a `ModuleDeps` assembled by hand around an unvalidated config is outside
this decision exactly as it is outside ADR-075.

## Alternatives considered

| Option | Why not |
| ------ | ------- |
| Map `Local` to `"-"` silently | Explicit over implicit — and on a database key the two do not mean the same thing, so the mapping would change behavior without telling anyone. |
| Accept it and WARN | Fail-fast is the framework's posture for configuration; a WARN at boot is the line nobody reads until the schedule has already drifted. |
| Refuse case-insensitively | `local` and `LOCAL` already fail as unknown zones. Widening the special case would invent a rule the loader does not have and hide the real reason those spellings fail. |

## Migration Impact

Behavioral break, no API change. See [migrations.md](migrations.md) `[C62.1]`.
