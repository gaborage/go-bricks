// Package testing provides utilities for testing JOSE-protected handlers in go-bricks
// applications without requiring real counterparty credentials.
//
// The primary use case is unit-testing handlers whose request/response types carry
// jose: tags (e.g., a Visa Token Services integration). Test code generates an
// in-memory key pair, registers it under the kids the handler expects, and uses
// SealForTest / OpenForTest to produce and consume nested JWE-of-JWS payloads.
//
// # Basic Usage
//
//	priv, pub := jositest.GenerateTestKeyPair(t)
//	resolver := jositest.NewTestResolver(map[string]any{
//	    "our-key":  priv, // played as both the decrypt key (us) and sign key (peer in test)
//	    "peer-key": pub,  // verify key
//	})
//
//	inboundPolicy := &jose.Policy{
//	    Direction:  jose.DirectionInbound,
//	    DecryptKid: "our-key", VerifyKid: "peer-key",
//	    SigAlg: jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg,
//	    Enc:    jose.DefaultEnc, Cty:       jose.DefaultCty,
//	}
//	outboundPolicy := &jose.Policy{
//	    Direction:  jose.DirectionOutbound,
//	    SignKid:    "peer-key", EncryptKid: "our-key",
//	    SigAlg: jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg,
//	    Enc:    jose.DefaultEnc, Cty:       jose.DefaultCty,
//	}
//
//	compact := jositest.SealForTest(t, []byte(`{"pan":"..."}`), outboundPolicy, resolver)
//	plaintext, claims := jositest.OpenForTest(t, compact, inboundPolicy, resolver)
//
// # Why a separate package
//
// jose/testing/ depends on jose/ but jose/ does not depend on jose/testing/, keeping
// production binaries free of test-only key-generation code (the rsa.GenerateKey call
// is heavy enough to matter for cold-start budgets).
package testing
