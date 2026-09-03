# httpclient/ — GoBricks package rules

Loaded when work touches `httpclient/`. Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## HTTP Client

Production-ready HTTP client: `httpclient.NewBuilder(logger)` fluent chain (`WithTimeout`, `WithRetries`, `WithW3CTrace`, `WithPeerName`) then `Build()`, which returns `(Client, error)` and fails construction when a `WithTransport`/`WithTLSConfig`/`WithHTTPClient` composition would silently discard TLS material or a caller-supplied `RoundTripper` (ADR-044; full example in [llms.txt](../llms.txt)). For full options and interceptor patterns, see [wiki/httpclient.md](../wiki/httpclient.md).
