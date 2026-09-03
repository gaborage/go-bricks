# #1308 — seal/open throwaway prototype

## The question

> Do the decided envelope (#1304), seal tags (#1305), family-kid keys (#1306) and replay slots (#1307) compose into a producer/consumer DX that feels right — and do a tampered clear field, a wrong key, a strip-and-re-sign, a rotation overlap, a cross-type reroute, a tenant mismatch and an unsealed body all fail exactly the way those decisions say?

## Run

```sh
go run ./research/amqp-seal-prototype                       # console walkthrough
go run ./research/amqp-seal-prototype -html /tmp/seal.html  # one self-contained static HTML page (tabs per scenario)
```

The report is **self-asserting**: every step declares what it expects (an error code, a startup error, or a clean open) and records what fired. Both renderers open with the matrix (scenario · step · expected · fired · disposition · ✓/✗) and the process exits 1 on any ✗. That matrix is the negative-vector list.

Build gates the prototype passes: `go build ./research/amqp-seal-prototype/ && go vet ./research/amqp-seal-prototype/ && gofmt -l research/amqp-seal-prototype` (empty output). No Go tests; the run itself is the assertion.

## What to look at

- `module_shape.go` — **the DX under test**: the code a module author writes under #1305 (`DeclareMessaging` with `DeclareTypedPublisher[PaymentAuthorized]`, an `h.Publish(ctx, client, evt)` call site, `DeclareTypedConsumerWithMeta` with `AcceptUnsealed`, a handler whose one `meta.DedupKey()` call works in every migration state). Thin framework stand-ins on top; S0 embeds and executes it.
- `envelope.go` — the pure, liftable module: `seal` tag scanner, keystore stand-in + activation, `Producer.Startup`/`Seal`, `Consumer.Startup` (fail-fast, scans T once) and `Consumer.Open` — the opener rules in the order the tickets fix them: sniff, alg, family pin, generation, signature, then the authenticated-slot rule (jti/iat/etyp/sp/tid-when-required, all before any inner-JWE work), etyp, tid, sp, inner JWE, splice, envelope. Error **codes** are the identity; rule numbers are a rendering aid. `Meta.Sealed()` / `Meta.DedupKey()` are the WithMeta door.
- `scenarios.go` — S0–S11 guided walkthroughs. The sample event `PaymentAuthorized` carries the sentinel `_ struct{} \`seal:"sign=svc-payments-sign,encrypt=acme-core-enc"\`` plus one `seal:"subject"` field. Every negative vector is built from the positive header's typed values and the harness asserts it differs in exactly the intended field.
- `report.go` — console + HTML renderers. After every step the full relevant state is captured: producer struct, activation + resolved generations, the exact wire body (and its three segments), the outer and inner protected headers decoded FROM THE WIRE, the payload doc (pretty-printed and as exact raw bytes), the AMQP delivery (Type, ContentType, headers), and on open: producer-wrote / travelled / consumer-saw side by side with a `reflect.DeepEqual` round-trip flag, `SealedEnvelope`, `DedupKey`, ledger verdict; on failure the code and rule.

Vocabulary is CONTEXT.md "Payload sealing": Seal, Subject, Two-kid identity, Typed door, Accept-unsealed, Logical kid, Generation, Accept set, Activation, Redelivery, Replay, Duplicate, Dedup key.

## Stated assumption

Go program, not a clickable HTML page, because the DX under test is Go struct tags and the producer/consumer code shape; the report is static.

## Caveats — prototype ≠ implementation

- No `PayloadError` / `PayloadStageOpen` integration; failures are a local `OpenError{Rule, Code, Disposition}`.
- No real keystore module: an in-memory name-addressed map with RSA keys generated at startup (2048-bit; public-only entries for the peer side).
- No messaging client: `Frame{Body, Headers}` stands in for the AMQP delivery; no broker, no ack/nack, no DLQ.
- No ledger table: an in-memory set keyed by dedup key with a dedup-hit counter.
- `Consumer.DisableFamilyPin` is a debug knob that exists only here (S4 defense-in-depth step).
- `TenancySingle` ignores `tid`: #1306/#1307 define shared and per-tenant only — an open #1309 question, not a decision embodied here.
- Spec-gap proposals raised by the panel (`omitempty` scan error, `cty` enforcement, kid charset, split codes per layer, splice preserving key order, header-id migration atom) are deliberately NOT embodied; they belong to #1309.
- Keys and `jti` are fresh every run, so wire bytes differ run to run.
- Card data shown in producer/consumer views is the published test vector `4111111111111111`; no CVV anywhere (SAD is barred).
- Not merged, never gated (`make check` / `make mutate` not run); this branch is an asset linked from #1308.
