# Server TLS Listener (Deep Dive)

`server.tls.*` (ADR-042) enables HTTPS on the go-bricks application listener via Echo's `echo.StartConfig.TLSConfig`. This page covers the config reference and, more importantly, which deployment topology it fits — read the "Deployment Guidance" section before turning it on.

> **Scope note:** this is the listener half only. Client-certificate verification (app-side mTLS) is a separate, gated follow-up — see ADR-042.

## Config Reference

| Key | Env var | Type | Notes |
| --- | --- | --- | --- |
| `server.tls.enabled` | `SERVER_TLS_ENABLED` | bool | Default `false` — plaintext listener |
| `server.tls.certfile` | `SERVER_TLS_CERTFILE` | string | Server certificate PEM file path |
| `server.tls.certvalue` | `SERVER_TLS_CERTVALUE` | string | Server certificate as a base64-encoded PEM string |
| `server.tls.keyfile` | `SERVER_TLS_KEYFILE` | string | Server private key PEM file path |
| `server.tls.keyvalue` | `SERVER_TLS_KEYVALUE` | string | Server private key as a base64-encoded PEM string |
| `server.tls.minversion` | `SERVER_TLS_MINVERSION` | string | `""` or `"1.2"` (floor, default) \| `"1.3"` |

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

## Deployment Guidance

### (a) ALB-terminated partner mTLS + `server.tls` for the ALB→target hop — the primary posture

AWS ALB can terminate partner mTLS at the edge (trust store + CRL support), verifying the partner's client certificate before the request ever reaches your service. In this topology, `server.tls` covers the **ALB→target** hop:

- The ALB **does not validate target certificates** by default — the ALB→target leg is encryption in transit, not peer authentication. A self-signed or internally-issued certificate is fine here; the ALB is not checking it against a trust store. The exception is `server.probes.port`: once it is set, the probe listener's check requires the leaf to carry a DNS or IP SAN, allow `serverAuth` and be within its validity period (see (d)).
- Partner identity data, when your application needs it, arrives via ALB-injected headers (`X-Amzn-Mtls-Clientcert-*` and related). AWS does not publicly document that the ALB strips client-supplied copies of these headers, so trust them only under the deployment posture defined in [wiki/forwarded_client_cert.md](forwarded_client_cert.md#trust-model) (ADR-043): mTLS-verify listener, closed security groups, and a single ingress path to the target group. go-bricks parses these headers via `server.forwardedclientcert.*`.
- This is the recommended default for any deployment already fronted by an ALB doing partner mTLS.

### (b) App-terminated mTLS (NLB / static-IP ingress) — requires the deferred client-verification feature

Some topologies terminate partner TLS at the application itself instead of at an ALB — typically because the ingress is an NLB (no application-layer TLS termination) or a static-IP requirement rules out ALB. In this shape, the application needs to verify the partner's client certificate directly: `ClientAuth`, a client CA pool, and a leaf-validation hook. **This is not implemented yet** — it is a gated, separate follow-up (see ADR-042's "The split"). `server.tls` alone gives you a plain HTTPS listener with no client verification; do not rely on it for partner authentication in this topology until the follow-up ships.

### (c) The staged-material WARN

Setting `server.tls.certfile`/`certvalue`/`keyfile`/`keyvalue` while `server.tls.enabled` is `false` is a legitimate staging step — e.g. rolling material out ahead of a flip. The server still starts in plaintext (fail-open), but startup logs exactly one WARN naming `server.tls.enabled` as the likely omission, so a mistyped `SERVER_TLS_ENABLED` in a deployment that carries full material is never silent.

### (d) The probe listener stays plain HTTP

`server.tls.*` governs the application listener only. The probe listener (`server.probes.port`, see [startup_defaults.md](startup_defaults.md#internal-probe-listener) and ADR-120) is always plain HTTP and has no TLS opt-in, so a TLS deployment that retargets its probes to the probe port also switches the probe scheme: `scheme: HTTP` on a kubelet `httpGet`, `HealthCheckProtocol` HTTP on an ALB target group. Changing only the port fails every probe.

The probe listener's application-listener check still speaks HTTPS to the application listener, verified against the listener's own leaf and never skipped: the trust pool holds only that leaf, and the expected name is its first DNS SAN, else its first IP SAN. With `server.probes.port` set, `Start` fails before either bind, naming `server.probes.port`, when the leaf has no DNS or IP SAN, lists extended key usages that exclude `serverAuth`, or is outside its validity period. A leaf that expires while the process runs fails `/ready` from then on, as it fails every verifying client. The check presents no client certificate; the deferred client-verification mode in (b) must revisit it.

## See Also

- [ADR-042](adr_042_server_tls.md) — full design rationale and consequences
- [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md) — the probe listener, which stays plain HTTP
- [wiki/migrations.md](migrations.md) (`[C55.3]`) — upgrade note
- [wiki/httpclient.md](httpclient.md) — the client-side TLS/mTLS counterpart
