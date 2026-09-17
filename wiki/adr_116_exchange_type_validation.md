# ADR-116: Declarations Refuse an Unknown Exchange Type at Startup

**Status:** Accepted
**Date:** 2026-09-16
**Issue:** #1566

## Context

`ExchangeDeclaration.Type` is a plain `string`. The framework had one exchange helper pair,
`NewTopicExchange` / `DeclareTopicExchange`, and kept the `topic` and `fanout` spellings as
unexported constants. A module that needed any other type built the declaration by hand and
re-spelt the type: `&messaging.ExchangeDeclaration{Type: "direct", Durable: true}`.

`Declarations.Validate()` never read `Type`. `Type: "bogus"`, `Type: "Direct"` and an omitted
`Type` all passed startup, and the broker refused the `exchange.declare` later, on declaration
replay, with a channel exception that names neither the call site nor the module. Fail Fast
says a declaration the broker can never accept is a startup failure.

## Decision

- **Export the family.** `ExchangeTypeDirect`, `ExchangeTypeTopic`, `ExchangeTypeFanout` and
  `ExchangeTypeHeaders` are untyped string constants for the four AMQP 0-9-1 core types.
  `ExchangeDeclaration.Type` stays `string`, so no consumer literal and no API signature moves.
- **Add the direct pair.** `NewDirectExchange` / `(*Declarations).DeclareDirectExchange`
  mirror the topic pair exactly: the same production defaults and the same register-and-return
  contract. There is no fanout or headers helper until a caller needs one; the constants cover
  a hand-built declaration.
- **Validate the type.** `Validate()` refuses every exchange whose `Type` is neither one of the
  four core types nor `x-`-prefixed. It reports all of them through `errors.Join`, in sorted
  name order, and each error names the exchange and the type. The comparison is exact, so
  `Direct` and an empty type are refused.

`x-` is admitted because RabbitMQ names plugin exchanges that way (`x-delayed-message`,
`x-consistent-hash`). Only the core names can be checked from the declaration set, so a
plugin exchange is never a startup refusal. Whether the plugin is enabled is still the
broker's call.

## Alternatives considered

**A named `ExchangeType` type.** It would turn a misspelling into a compile error. Rejected:
every `ExchangeDeclaration` literal a consumer wrote would stop compiling for a check that
startup validation already makes. It would also still need a conversion for `x-` plugin
types.

**Validate at the client's `DeclareExchange`.** Rejected: that runs per tenant, on the replay
path, after the application has started. Everything the rule needs is in the declaration set
at validate time.

## Consequences

- **A declaration that used to boot now refuses.** A hand-built exchange with an empty,
  wrong-case or misspelled `Type` fails `Validate()` at startup where it used to fail at the
  broker. Every such declaration was already unusable. The change is when it fails and what
  the error names. Migration is [migrations.md](migrations.md) `[C66.4]`.
- **Exchange `Args` are still not judged per type** (`x-delayed-type` and similar). The broker
  keeps that check.

## References

- [ADR-040](adr_040_declaration_args_passthrough.md): declaration `Args` reach the broker
- [ADR-106](adr_106_dlq_helper_declares_quorum_queues.md): the queue-type check this sits beside
- [wiki/messaging.md](messaging.md#helper-functions-for-simplified-declarations): the helpers
- `messaging/constants.go` (`ExchangeType*`), `messaging/helpers.go` (`NewDirectExchange`,
  `DeclareDirectExchange`), `messaging/declarations.go` (`validateExchangeTypes`)
