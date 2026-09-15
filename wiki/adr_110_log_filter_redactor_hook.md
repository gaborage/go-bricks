# ADR-110: The Sensitive-Data Filter Consults a `logger.Redactor` Before Reflecting

**Status:** Accepted
**Date:** 2026-09-12
**Issue:** #1602
**Related:** [ADR-086](adr_086_mask_inside_opaque_payloads.md) (the sibling opaque-payload door in the same dispatch) · [ADR-109](adr_109_bearer_credential_verification.md) (whose `auth.Principal` motivated the hook)

## Context

`SensitiveDataFilter` judges a value by field NAME. `FilterValue` handles `map[string]any`, then
the ADR-086 opaque-payload door, then reflects by kind, copying a struct field by field into a map
keyed by its `json` tag. Nothing a type says about itself is consulted: no `json.Marshaler`, no
`fmt.Stringer`, no `fmt.Formatter`. A type that deliberately hides fields from its own JSON or
string rendering therefore has those fields walked and logged anyway through `Interface` and
`WithFields`, both of which reach the same walker. The filter cannot see inside a value, and the
type author, the one party who knows which fields are secret, had no way to tell it.

## Options Considered

1. **Honor `json.Marshaler`.** Rejected. A marshaler exists for the wire, not for logs: the fields a
   service must return to its caller are often exactly the ones a log must not keep. Honoring it
   would also change the rendering of every existing type that implements it, which is a silent
   behavior change on the logging path, and its `[]byte` result would still need the payload door.
2. **Honor `fmt.Stringer`.** Rejected for the same reason, and it flattens structure the filter and
   the log backend can use.
3. **A dedicated, logger-owned interface.** Chosen. Nothing implements it by accident, so every
   type that does not opt in keeps byte-identical output.

## Decision

1. **The interface.** `logger.Redactor` with one method, `RedactedForLog() any`. Its godoc tells
   implementers to use a VALUE receiver: a pointer's method set includes its element's
   value-receiver methods, so both `T` and `*T` are recognized, while a pointer-receiver method
   leaves a bare `T` unrecognized and walked by reflection.
2. **Dispatch position.** In the shared `filterValueWithProtection`, sibling to the opaque door, so
   `Interface`, `WithFields` and every nested struct field, map value and slice element get it.
   It runs AFTER the sensitive-key match (a value under a key named in the filter is masked whole
   and the hook is never called), after nil and depth handling (an exhausted depth masks before
   consumer code runs), and BEFORE the opaque door and any reflection. A nil pointer is not handed
   to the hook, since calling a value-receiver method through it panics.
3. **One-shot re-entry.** The returned value is filtered at depth minus one, with the needle list,
   the opaque door and reflection all applied — but the hook is NOT consulted on the returned value
   itself, only on its children. A hook returning its own type therefore terminates after one pass
   instead of recursing to depth exhaustion and masking whole, while a `Redactor` nested inside the
   returned shape is still honored. The rule is by position, not by type: a hook that returns a
   DIFFERENT `Redactor` directly has that value walked by reflection, so it must call the inner
   method itself (`return inner.RedactedForLog()`) or nest the value in its result.
4. **Scope.** The `Err` door is untouched: `FilterConfig.ErrorRedactor` stays the error-text seam,
   and an error that also implements `Redactor` renders `Error()` at `Err` as before. The package
   ships no test double; the hook is a single method, so a test-local type is the fixture.

## Consequences

A type author can control the value's shape wherever a filtered logger's `Interface` or `WithFields`
logs it, instead of every call site remembering not to log it. It is not consulted without a filter,
at `Err`, or through `Msgf` formatting. The returned shape is still filtered, so a hook that forgets
a field the needle list names is backstopped rather than trusted. A hook is consumer code on the
logging path with no recover around it: a panicking hook propagates out of the log call. Types that
do not implement the interface render byte-identically, so this is not a breaking change and has no
migrations atom.

## References

- `logger/filter.go` — `Redactor`, `asRedactor`, `filterRedactedShape`
- `logger/filter_test.go` — `TestFilterRedactor*`, `TestFilterNonRedactorLineIsUnchanged`
- [observability.md](observability.md#self-redacting-values)
