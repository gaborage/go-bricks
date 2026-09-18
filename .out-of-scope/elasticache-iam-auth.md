# ElastiCache IAM authentication

**Decision:** Rejected — the framework does not sign SigV4 tokens for
`AUTH`, does not add a `cache.redis.auth: iam` mode, and does not take
`aws-sdk-go-v2` into the root module for the cache client.

**Reason:** Amazon ElastiCache IAM auth is an alternative credential, not a
capability gap. The RBAC path that shipped in #1734 already reaches every
ElastiCache Serverless cache: `cache.redis.username` + `cache.redis.password`
travel as `AUTH <username> <password>`, and ElastiCache lets one RBAC user
carry two passwords, so rotation needs no downtime and no token refresh.

IAM auth is AWS-only by construction: a SigV4 presigned `connect` request
(scheme stripped) used as the password, `ResourceType=ServerlessCache` in the
signed query, 15-minute token validity, a forced reconnect every 12 hours. No
other Redis or Valkey host speaks it. Supporting it would put the AWS SDK into
the framework proper for the first time (`tools/migration` carries it for
Secrets Manager, the root module does not), grow every consumer's `go.sum`,
and hand `cache/redis` a vendor lifecycle (token TTL, connection ceiling,
credential chain) that the framework abstracts elsewhere rather than adopts.
`mode: cluster` and `username` are Redis protocol features that happen to
serve ElastiCache; IAM would be the first thing in the cache client that is
AWS.

```yaml
# Supported posture: one RBAC user per service, TLS on, cluster protocol.
cache:
  redis:
    host: my-cache.serverless.use1.cache.amazonaws.com
    mode: cluster
    username: ${CACHE_REDIS_USERNAME}
    password: ${CACHE_REDIS_PASSWORD}
    tls:
      enabled: true
```

**Reopen when this fires:** a consumer must run against an ElastiCache cache
whose org policy forbids static secrets outright, so RBAC passwords are not an
option. The shape to build then is cloud-agnostic: a credentials-provider seam
on the Redis client config (`func(ctx) (username, password string, err
error)`, the go-redis per-connection hook), with the SigV4 signer supplied by
the consumer. The framework never links the AWS SDK either way.

**Prior requests:**

- [#1728](https://github.com/gaborage/go-bricks/issues/1728) — closed
  2026-09-18 (rejected, this entry)
