# #1308 — seal/open throwaway prototype

## The question

> Do the decided envelope (#1304), seal tags (#1305), family-kid keys (#1306) and replay slots (#1307) compose into a producer/consumer DX that feels right — and do a tampered clear field, a wrong key, a strip-and-re-sign, a rotation overlap, a cross-type reroute, a tenant mismatch and an unsealed body all fail exactly the way those decisions say?

## Run

```sh
go run ./research/amqp-seal-prototype                       # console walkthrough
go run ./research/amqp-seal-prototype -html /tmp/seal.html  # one self-contained static HTML page (tabs per scenario)
```

Build gates the prototype passes: `go build ./research/amqp-seal-prototype/ && go vet ./research/amqp-seal-prototype/ && gofmt -l research/amqp-seal-prototype` (empty output). No tests.

## What to look at

- `envelope.go` — the pure, liftable module: `seal` tag scanner, keystore stand-in + activation, `Producer.Seal`, `Consumer.Open` (the opener rule set 1–11 in decided order, one error code per rule), `SealedEnvelope.DedupKey`, ledger stand-in. Read the `Open` method top to bottom — that is the spec, executable.
- `scenarios.go` — S1–S10 guided walkthroughs. The sample event `PaymentAuthorized` is the DX under test: sentinel `_ struct{} \`seal:"sign=svc-payments-sign,encrypt=acme-core-enc"\`` plus one `seal:"subject"` field.
- `report.go` — console + HTML renderers. After every step the full relevant state is captured: producer struct, exact wire body, decoded protected headers, payload doc, AMQP headers map, opened struct + `SealedEnvelope` + `DedupKey`, ledger verdict; on failure the code and the rule number.

Vocabulary is CONTEXT.md "Payload sealing": Seal, Subject, Two-kid identity, Typed door, Accept-unsealed, Logical kid, Generation, Accept set, Activation, Redelivery, Replay, Duplicate, Dedup key.

## Stated assumption

Go program, not a clickable HTML page, because the DX under test is Go struct tags and the producer/consumer code shape; the report is static.

## Caveats — prototype ≠ implementation

- No `PayloadError` / `PayloadStageOpen` integration; failures are a local `OpenError{Rule, Code, Disposition}`.
- No real keystore module: an in-memory name-addressed map with RSA keys generated at startup (2048-bit; public-only entries for the peer side).
- No messaging client: `Frame{Body, Headers}` stands in for the AMQP delivery; no broker, no ack/nack, no DLQ.
- No ledger table: an in-memory set keyed by dedup key with a dedup-hit counter.
- `Consumer.DisableFamilyPin` is a debug knob that exists only here (S4 defense-in-depth step).
- Keys and `jti` are fresh every run, so wire bytes differ run to run.
- Card data shown in producer/consumer views is the published test vector `4111111111111111`; no CVV anywhere (SAD is barred).
- Not merged, never gated (`make check` / `make mutate` not run); this branch is an asset linked from #1308.
