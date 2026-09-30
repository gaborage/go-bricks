# ADR-130: App-Terminated mTLS Verifies Client Certificates Against a Configured CA Bundle

**Status:** Proposed
**Date:** 2026-09-29
**Issue:** #767
**Breaking:** none — additive `server.tls` keys; the zero value leaves verification off
**Extends:** [ADR-042](adr_042_server_tls.md) (the deferred client-verification split)

## Context

ADR-042 shipped the `server.tls` listener and deferred client-certificate verification until a
deployment terminates partner TLS at the application itself (an NLB or static-IP ingress, with no
ALB to verify the partner). The client half shipped separately (`httpclient.NewClientTLSConfig`,
`Builder.WithTLSConfig`), and ADR-043 covers the
ALB-terminated shape by parsing the identity the ALB forwards. The app-terminated shape still has no
way to demand or verify a client certificate: the framework owns the listener's `*tls.Config`
(ADR-034, ADR-042), so a consumer cannot add `ClientCAs` from outside.

## Decision

1. **Three keys under `server.tls`.** `clientauth` (`SERVER_TLS_CLIENTAUTH`) selects the policy;
   `clientcafile` (`SERVER_TLS_CLIENTCAFILE`) or `clientcavalue` (`SERVER_TLS_CLIENTCAVALUE`, a
   base64-encoded PEM bundle) supplies the client-CA roots, exactly one of the two, the same rule
   `certfile`/`certvalue` follow.
2. **Every non-empty policy verifies.** `""` is off. `verify` maps to
   `tls.VerifyClientCertIfGiven`: a client may omit its certificate, and one it presents must chain
   to the bundle. `require-verify` maps to `tls.RequireAndVerifyClientCert`. Go's `request` and
   `require` modes accept an unverified certificate, which reads as authentication and is none, so
   they are refused at startup like any other value, and the error lists `verify` and
   `require-verify`.
3. **Explicit, or refused, on an enabled listener.** With `server.tls.enabled` true, config
   validation refuses a verifying policy without a client CA and a client CA without a policy (Go
   loads `ClientCAs` under `NoClientCert` and ignores it, so an operator would believe verification
   is on). With TLS disabled the keys are not validated, as ADR-042 treats staged cert/key: staging
   ahead of a flip is legitimate, and the staged-material WARN naming `server.tls.enabled` now also
   fires for a staged `clientauth` or client CA, so a plaintext listener carrying a policy is never
   silent.
4. **The bundle fails fast at `Start`.** It loads through the `internal/secretfile` guards the
   server cert/key and the httpclient CA use, and `secretfile.CertPool` refuses an empty,
   unreadable or partly corrupt bundle, so a startup is never clean with fewer roots than
   configured. The `MinVersion` floor (TLS 1.2, or 1.3) applies unchanged on the mTLS path.
5. **Identification, not authorization.** A verified chain says the caller holds a key certified by
   one of the configured CAs. It does not say what the caller may do: the handler, or the leaf hook
   below, still authorizes, as with ADR-043's forwarded identity and ADR-109's `Principal`. Handlers
   read `VerifiedChains`, never `PeerCertificates`.
6. **Independent of `server.forwardedclientcert`.** The two compose and neither changes the other.
   A proxy (Envoy, nginx) that authenticates to the application over mTLS while forwarding the end
   client's certificate in a header is a legitimate and stronger posture: the mTLS leaf proves the
   hop came from the trusted proxy, which is what makes the forwarded header trustworthy. The mTLS
   leaf identifies the hop; the forwarded certificate identifies the end client; both are
   identification. Under that posture the leaf-validation hook sees the proxy's certificate, not the
   end client's, and a SAN or OU allowlist written for partners would reject the proxy.
7. **The probe listener refuses only `require-verify`.** The internal probe listener's
   application-listener check (ADR-120) dials with no client certificate. Under `require-verify`
   every such handshake fails and `/ready` would stay 503 forever, so `require-verify` beside
   `server.probes.port > 0` is a startup error naming both keys: config validation refuses it, and
   `Start` refuses it again before either bind for a config assembled in Go. `verify` beside the
   probe listener is allowed: a handshake with no client certificate completes under
   `VerifyClientCertIfGiven`.

**To follow (not in this change):** a leaf-validation hook (SAN/OU allowlists) passed through
`server.NewWithOptions` and `app.Options.ServerOptions`, run as `VerifyPeerCertificate` after chain
verification and again from `VerifyConnection` on a resumed session, where `VerifyPeerCertificate`
is skipped; a hook with TLS enabled and no verifying policy fails `Start` (it would be inert on an
open endpoint), while a hook with TLS disabled only WARNs; the no-cert, wrong-CA and valid
handshake matrix; and the ADR-042 and wiki updates.

## Consequences

- Additive: three comparable string fields on `config.ServerTLSConfig`; the zero value leaves every
  deployment unverified, as today.
- No revocation checking (CRL/OCSP): a revoked client certificate verifies until it expires.
  Rotation of the bundle, as of the server certificate, is restart-based.
- Listener-wide only: no per-route policy. Under `verify` a certless client is still accepted; a
  route family that must refuse one checks `VerifiedChains` itself.

## References

- [ADR-042](adr_042_server_tls.md), [ADR-043](adr_043_forwarded_client_cert.md),
  [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md)
- `config/server_section.go` (`validateServerTLSClientAuth`, `validateServerProbes`),
  `server/tls.go` (`buildServerTLSConfig`, `parseClientAuth`), `server/server.go`
  (`startProbeListener`)
