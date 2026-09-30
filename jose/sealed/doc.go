// Package sealed implements field-level sealing for AMQP event payloads: one declared
// Subject field travels as a compact JWE inside a JSON document that is signed whole as a
// compact JWS (ADR-097 — encrypt-subset-then-sign-whole). The `seal` struct tag family
// declares the two Logical kids and the Subject; ScanType turns a type into a Spec, Seal
// turns one event into its wire bytes. Open and OpenDocument verify and decrypt (the consumer
// side); Verify runs the same rules without decrypting, the producer's read-back of sealed
// bytes it holds (#1869). SealDocument is the raw-document door for tooling and JSON fixtures:
// same envelope, bytes the caller serialized, and a NewDocumentSpec that names no Go type.
//
// Sealing reuses the parent jose package's allowlist, key resolution, error type and the
// cryptoadapter seams — forbid-dual-use (one struct as both HTTP body and AMQP event) is a
// type policy enforced by the distinct tag name, not a separate implementation.
package sealed
