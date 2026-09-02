# ECDSA Key Support for JOSE

The keystore does not surface ECDSA keys, and `ES256`/`ES384`/`ES512` stay off
the JOSE signature-algorithm allowlist.

## Why this is out of scope

The keystore returns concrete `*rsa.PrivateKey`/`*rsa.PublicKey` types, and the
JOSE allowlist (`RS256`/`PS256` signing, `RSA-OAEP-256` + `A256GCM` encryption)
matches what the store can actually serve — `ES256` was removed from the
allowlist precisely because selecting it passed registration and then failed
on every outbound request: go-jose's RSA signer rejects the pairing with
`ErrUnsupportedAlgorithm`, which `cryptoadapter.Sign` and `jose.Seal` propagate
as an error (`JOSE_OUTBOUND_FAILED`), not a crash (see PR #334).

Re-enabling it means generalizing the keystore from RSA-only to
`crypto.Signer`/`crypto.PublicKey` and teaching the crypto adapter to dispatch
by algorithm class. That is speculative crypto-surface expansion: no partner
integration requires ECDSA (Visa Token Services uses RS256/PS256), and locking
the keystore into a `crypto.Signer` shape *before* a real ECDSA caller exists
would fix the API around guesses — file-based DER? PKCS#8? raw point
coordinates? per-tenant rotation? — that a concrete integration would answer
for free. YAGNI applies with extra force to key-management surface: every
admitted key shape is a permanent review and audit obligation.

Production enforcement lives in `ParseTag`, which runs
`Policy.validateAlgorithms` on every `jose:` tag so `sig_alg=ES256` fails
registration with `JOSE_ALGORITHM_DISALLOWED`; `Seal` re-runs the same check
as defense in depth, and `Open` threads the allowlist into go-jose's parser so
an inbound `ES256` header is rejected before verification.
`TestAllowlistRejectsES256` is regression coverage for the exported allowlist
only; it stays.

## Reopen trigger

A concrete partner integration that requires an ECDSA-family JOSE algorithm.
That requirement supplies exactly the design inputs the generalization needs
(key distribution format, rotation cadence, algorithm set), at which point the
implementation outline preserved in the prior request below is the starting
point.

## Prior requests

- #347 — "keystore: ECDSA key support for JOSE"
