# Server TLS Listener (Deep Dive)

`server.tls.*` (ADR-042) enables HTTPS on the go-bricks application listener via Echo's `echo.StartConfig.TLSConfig`, and `server.tls.clientauth` adds client-certificate verification for app-terminated mTLS (ADR-130). This page covers the config reference and, more importantly, which deployment topology it fits — read the "Deployment Guidance" section before turning it on.

## Config Reference

| Key | Env var | Type | Notes |
| --- | --- | --- | --- |
| `server.tls.enabled` | `SERVER_TLS_ENABLED` | bool | Default `false` — plaintext listener |
| `server.tls.certfile` | `SERVER_TLS_CERTFILE` | string | Server certificate PEM file path |
| `server.tls.certvalue` | `SERVER_TLS_CERTVALUE` | string | Server certificate as a base64-encoded PEM string |
| `server.tls.keyfile` | `SERVER_TLS_KEYFILE` | string | Server private key PEM file path |
| `server.tls.keyvalue` | `SERVER_TLS_KEYVALUE` | string | Server private key as a base64-encoded PEM string |
| `server.tls.minversion` | `SERVER_TLS_MINVERSION` | string | `""` or `"1.2"` (floor, default) \| `"1.3"` |
| `server.tls.clientauth` | `SERVER_TLS_CLIENTAUTH` | string | `""` (off, default) \| `verify` \| `require-verify` — see [Client-Certificate Verification](#client-certificate-verification) |
| `server.tls.clientcafile` | `SERVER_TLS_CLIENTCAFILE` | string | Client-CA bundle PEM file path |
| `server.tls.clientcavalue` | `SERVER_TLS_CLIENTCAVALUE` | string | Client-CA bundle as a base64-encoded PEM string |

Exactly one of `certfile`/`certvalue` and exactly one of `keyfile`/`keyvalue` must be set when `enabled` is `true` — both the cert and the key are required (a server always needs a certificate to terminate TLS; there is no CA-only mode as there is for the httpclient's client-cert config). Config validation (`config/server_section.go`) checks presence and mutual exclusivity structurally at startup; the PEM material itself is read and parsed at `server.Start()` — a bad path, corrupt PEM, or mismatched cert/key pair fails startup fast rather than degrading to plaintext.

Full example:

```yaml
server:
  tls:
    enabled: true
    certfile: /etc/tls/server.crt
    keyfile: /etc/tls/server.key
    minversion: "1.2"
```

Or with inline base64-encoded material (e.g. injected from a secret manager):

```yaml
server:
  tls:
    enabled: true
    certvalue: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t...
    keyvalue: LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0t...
```

PEM material loads through the same `internal/secretfile` guards the httpclient TLS loader uses: a `*File` value that looks like inline key material (rather than a path) is rejected with a clear error instead of being read as a path, and read errors never echo unbounded file content into a startup log.

The listener is **HTTP/1.1-only** — `NextProtos` is deliberately left unset. Certificate rotation is restart-based; there is no hot-reload watcher in this iteration.

## Client-Certificate Verification

`server.tls.clientauth` (ADR-130) makes the listener verify client certificates against a client-CA bundle during the handshake — a caller is rejected at the handshake, never with a 403 afterwards. It applies listener-wide; there is no per-route policy.

| `clientauth` | Client certificate | Chain verified against `clientcafile`/`clientcavalue`? |
| --- | --- | --- |
| `""` | not requested | n/a |
| `verify` | optional | yes, *if given* — a certless client is still accepted |
| `require-verify` | mandatory | **yes — the only mode where every accepted client presented a verified chain** |

Go's `request` and `require` modes are deliberately not exposed: they hand a handler an **unverified** certificate, which reads as authentication and is none. Both, and any other value, are refused at startup, and the error lists `verify` and `require-verify`.

```yaml
server:
  tls:
    enabled: true
    certfile: /etc/tls/server.crt
    keyfile: /etc/tls/server.key
    clientauth: require-verify
    clientcafile: /etc/tls/partner-ca.pem
```

**Validation.** With `server.tls.enabled: true`, a verifying `clientauth` needs exactly one of `clientcafile`/`clientcavalue`, and a client CA without a `clientauth` is refused — Go would load it and ignore it, so the operator would believe verification is on. The bundle is read at `server.Start()` through the same `internal/secretfile` guards as the server cert/key; an unreadable, empty or partly corrupt bundle fails startup rather than trusting fewer roots than configured. With `enabled: false` the keys are not validated (a staged flip), and the staged-material WARN in (c) also fires for them. The `minversion` floor applies unchanged on the mTLS path.

**Reading the identity.** A verified chain identifies the caller; it does not authorize it — the handler, or the leaf hook below, still decides what that identity may do. Handlers read `ctx.Request().TLS.VerifiedChains`, **never** `PeerCertificates`: under `verify` a certless request has no chain, and `VerifiedChains` is what the verifier actually accepted. Under `verify`, a route family that must refuse certless callers checks for an empty `VerifiedChains` itself.

### Leaf-validation hook

For SAN/OU/CN allowlists, pass `server.Options.TLSVerifyPeerCertificate` — the stdlib `VerifyPeerCertificate` signature — through `app.Options.ServerOptions` (or `server.NewWithOptions` for a server you build yourself):

```go
fw, _, err := app.NewWithOptions(&app.Options{
    ServerOptions: &server.Options{
        TLSVerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
            if chains[0][0].Subject.CommonName != "partner-gateway" {
                return errors.New("client not on the allowlist")
            }
            return nil
        },
    },
})
```

- It runs **after** chain verification, so read `verifiedChains`, not `rawCerts`. Returning an error rejects the handshake.
- It runs on **every** handshake, including a resumed session: Go skips `VerifyPeerCertificate` on resumption, so the framework runs the hook again from `VerifyConnection` — a client admitted once cannot resume past a tightened allowlist.
- A client that presents **no certificate never reaches it**. Whether one may is `clientauth`'s decision alone: use `require-verify` to refuse certless callers. This also keeps the probe listener's certless self-check passing under `verify`.
- A hook panic becomes a handshake error naming the panic's **type**, never its value.
- It is on the handshake path of every connection: keep it fast and allocation-light.
- **Inert hook fails closed, a staged one warns.** A hook with `server.tls.enabled: true` and an empty `clientauth` would guard nothing, so `Start` fails naming `verify` and `require-verify`. A hook with `enabled: false` is a staged flip: the server starts in plaintext and logs a WARN naming `server.tls.enabled`.

**Limitation:** revocation (CRL/OCSP) is **not** checked — a revoked client certificate verifies until it expires. An ALB trust store with a CRL is the edge-side answer; rotating the client-CA bundle, like the server certificate, takes a restart.

## Deployment Guidance

### (a) ALB-terminated partner mTLS + `server.tls` for the ALB→target hop — the primary posture

AWS ALB can terminate partner mTLS at the edge (trust store + CRL support), verifying the partner's client certificate before the request ever reaches your service. In this topology, `server.tls` covers the **ALB→target** hop:

- The ALB **does not validate target certificates** by default — the ALB→target leg is encryption in transit, not peer authentication. A self-signed or internally-issued certificate is fine here; the ALB is not checking it against a trust store. The exception is `server.probes.port`: once it is set, the probe listener's check requires the leaf to carry a DNS or IP SAN, allow `serverAuth` and be within its validity period (see (d)).
- Partner identity data, when your application needs it, arrives via ALB-injected headers (`X-Amzn-Mtls-Clientcert-*` and related). AWS does not publicly document that the ALB strips client-supplied copies of these headers, so trust them only under the deployment posture defined in [wiki/forwarded_client_cert.md](forwarded_client_cert.md#trust-model) (ADR-043): mTLS-verify listener, closed security groups, and a single ingress path to the target group. go-bricks parses these headers via `server.forwardedclientcert.*`.
- This is the recommended default for any deployment already fronted by an ALB doing partner mTLS.

### (b) App-terminated mTLS (NLB / static-IP ingress) — `server.tls.clientauth`

Some topologies terminate partner TLS at the application itself instead of at an ALB — typically because the ingress is an NLB (no application-layer TLS termination) or a static-IP requirement rules out ALB. Here the application verifies the partner's client certificate directly: set `server.tls.clientauth` (usually `require-verify`) with the partner's CA bundle, and add a leaf-validation hook when the CA signs more than the partners you admit — see [Client-Certificate Verification](#client-certificate-verification).

**The posture split.** `server.forwardedclientcert.*` ([forwarded_client_cert.md](forwarded_client_cert.md), ADR-043) is for LB-terminated partner mTLS, where the ALB verifies the partner and forwards its identity in headers; `server.tls.clientauth` is for app-terminated partner mTLS, where the application verifies the partner itself.

**How they compose.** The two are independent and may be enabled together. A proxy (Envoy, nginx) that authenticates to the application over mTLS while forwarding the end client's certificate in a header is a legitimate and stronger posture, on one condition: the proxy must set the forwarded identity from the end client's certificate it verified itself, and overwrite or strip any copy of those headers the caller sent. For a connection that presented a verified chain, the mTLS leaf, allowlisted to the proxy by the leaf-validation hook, proves the hop came from the trusted proxy; it does not prove the header's content, so a proxy that relays a caller-supplied header unchanged lets any caller claim any identity. Under `verify` a certless caller still completes the handshake and never reaches the hook, so if the application is directly reachable it can send forged `X-Amzn-Mtls-Clientcert-*` headers and claim any identity: the composed posture requires `require-verify` or proxy-only ingress (closed security groups and a single ingress path, per the [trust model](forwarded_client_cert.md#trust-model)), and under `verify` without proxy-only ingress the forwarded identity must not be trusted. Because `require-verify` is refused beside `server.probes.port > 0` (see [(d)](#d-the-probe-listener-stays-plain-http)), a deployment that runs the probe listener is on `verify` and must rely on proxy-only ingress. The mTLS leaf identifies the **hop** (the proxy) for a connection that presented a verified chain; the forwarded certificate identifies the **end client**. Both are identification, not authorization — the deployment still authorizes. **The trap:** under that posture the leaf-validation hook sees the **proxy's** certificate, not the end client's, so an allowlist written for partner subjects rejects the proxy; allowlist the proxy there and judge the partner from the forwarded identity.

### (c) The staged-material WARN

Setting `server.tls.certfile`/`certvalue`/`keyfile`/`keyvalue` (or `clientauth`/`clientcafile`/`clientcavalue`) while `server.tls.enabled` is `false` is a legitimate staging step — e.g. rolling material out ahead of a flip. The server still starts in plaintext (fail-open), but startup logs exactly one WARN naming `server.tls.enabled` as the likely omission, so a mistyped `SERVER_TLS_ENABLED` in a deployment that carries full material is never silent.

### (d) The probe listener stays plain HTTP

`server.tls.*` governs the application listener only. The probe listener (`server.probes.port`, see [startup_defaults.md](startup_defaults.md#internal-probe-listener) and ADR-120) is always plain HTTP and has no TLS opt-in, so a TLS deployment that retargets its probes to the probe port also switches the probe scheme: `scheme: HTTP` on a kubelet `httpGet`, `HealthCheckProtocol` HTTP on an ALB target group. Changing only the port fails every probe.

The probe listener's application-listener check still speaks HTTPS to the application listener, verified against the listener's own leaf and never skipped: the trust pool holds only that leaf, and the expected name is its first DNS SAN, else its first IP SAN. With `server.probes.port` set, `Start` fails before either bind, naming `server.probes.port`, when the leaf has no DNS or IP SAN, lists extended key usages that exclude `serverAuth`, or is outside its validity period. A leaf that expires while the process runs fails `/ready` from then on, as it fails every verifying client. The check presents no client certificate, so `server.tls.clientauth: require-verify` beside `server.probes.port > 0` is refused — by config validation and again by `Start` before either bind — naming both keys: every check's handshake would fail and `/ready` would stay `503` forever. `verify` is allowed: a certless handshake completes under it, and the leaf-validation hook is never asked about a client without a certificate.

## See Also

- [ADR-042](adr_042_server_tls.md) — full design rationale and consequences
- [ADR-130](adr_130_server_mtls_client_verification.md) — client-certificate verification, the leaf hook and the probe refusal
- [wiki/forwarded_client_cert.md](forwarded_client_cert.md) — the LB-terminated identity half (ADR-043)
- [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md) — the probe listener, which stays plain HTTP
- [wiki/migrations.md](migrations.md) (`[C55.3]`) — upgrade note
- [wiki/httpclient.md](httpclient.md) — the client-side TLS/mTLS counterpart
