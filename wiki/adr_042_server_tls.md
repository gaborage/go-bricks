# ADR-042: Server TLS Listener (Client Verification Deferred)

**Status:** Accepted
**Date:** 2026-07-27

## Context

go-bricks could not serve HTTPS: `config.ServerConfig` had no TLS options and `server.Start()` always built a plaintext listener. ADR-034 sealed the Echo engine — the `Echo()` accessor was removed and `Start()` builds the `echo.StartConfig` literal terminally — so a consumer could not add TLS from outside; the framework owed the config surface for the seam it closed.

Deployments fronted by an AWS ALB that terminates partner mTLS at the edge (trust store + CRL support) still need TLS on the ALB→target hop — the ALB does not validate target certificates, so that hop is encryption in transit, not peer authentication. Deployments without an edge proxy need TLS outright. Echo v5 already carries the mechanism: `echo.StartConfig.TLSConfig` wraps the listener via `tls.NewListener` when set — go-bricks simply never populated it.

Original scoping considered shipping listener TLS and app-side client-certificate verification (mTLS) together. An infrastructure-reality review found no deployment yet terminates partner TLS at the app itself (the NLB/static-IP-ingress shape) — every named deployment sits behind an ALB that already verifies the partner certificate. Client verification was split out to a gated follow-up (a later ADR) rather than shipped speculatively.

## Decision

`config.ServerConfig` gains a `TLS ServerTLSConfig` field: `Enabled`, `CertFile`/`CertValue`, `KeyFile`/`KeyValue` (exactly one source per PEM piece), and `MinVersion` (`""`/`"1.2"` floor, `"1.3"` opt-up). PEM material loads through the same `internal/secretfile` guards the httpclient TLS loader uses (mis-filed-material detection, bounded error echoing) — keystore is not a certificate store and stays out of scope. `server.Start()` builds a `*tls.Config` and passes it as `echo.StartConfig.TLSConfig` when enabled; bad or unreadable material fails `Start()` fast rather than degrading to plaintext. TLS 1.2 is the floor.

The listener is HTTP/1.1-only: `NextProtos` stays unset. Echo's `start()` calls `server.Serve(listener)`, and the stdlib only enables HTTP/2 on the `ServeTLS` path (which go-bricks does not use) — advertising `"h2"` without the h2 server wired would break handshakes. `echo.StartConfig.StartTLS` is the h2 path go-bricks deliberately does not call.

Certificate rotation is restart-based; there is no `tls.Config.GetCertificate` watcher in this iteration. There is no raw `*tls.Config` escape hatch — the framework owns the listener's security posture, and a raw override would bypass both validation and the material guards. This differs from the httpclient half, where the client is consumer-constructed and a raw-config escape hatch already exists.

A staged-but-disabled material configuration (fields set while `server.tls.enabled` is false) is fail-open — legitimate ahead of a flip — but emits one WARN naming `server.tls.enabled`, so a mistyped flag is never silent.

**The split:** client-certificate verification (`ClientAuth`, client CA pool, leaf-validation hook) is deferred to a gated follow-up. Edge termination (ALB mTLS with trust store and CRL) already covers every named ALB-fronted deployment. App-side verification activates only when a deployment terminates partner TLS at the app itself (NLB/static-IP ingress) — no such deployment exists yet.

## Consequences

**Additive-only:** `ServerConfig.TLS` is a new comparable-typed field; the zero value (`Enabled: false`) leaves every existing deployment on plaintext, byte-for-byte unchanged. No exported signature changes.

**Scope limits carried forward:**

- ALB→target TLS is encryption in transit, not peer authentication — the ALB does not validate target certificates. Partner identity data (when needed) arrives via ALB-injected `X-Amzn-Mtls-Clientcert-*` headers; parsing those headers is a separate follow-up. **Amended by ADR-043:** AWS does not publicly document that the ALB strips client-supplied copies of these headers, so trust cannot rest on a stripping guarantee — it rests on deployment posture (mTLS-verify listener, closed security groups, single ingress path) as defined in [wiki/forwarded_client_cert.md#trust-model](forwarded_client_cert.md#trust-model).
- No revocation checking at the app in this iteration.
- No certificate hot-reload; rotation requires a restart. `buildServerTLSConfig` is where a future `GetCertificate` watcher slots in.
- HTTP/2 is not offered on the TLS listener.

See [wiki/server_tls.md](server_tls.md) for the full config reference and deployment guidance, and [wiki/migrations.md](migrations.md) (`[C55.3]`) for the upgrade note.
