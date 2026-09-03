# cache/ — GoBricks package rules

Loaded when work touches `cache/`. Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Cache Architecture

Redis-based caching with type-safe CBOR serialization, multi-tenant isolation, and automatic lifecycle management. Store the accessor function (`deps.Cache`), NOT a resolved instance — resolution is tenant-aware per call (full example in [llms.txt](../llms.txt)).

**Operations:** `Get`, `Set`, `GetOrSet` (atomic SET NX), `CompareAndSet` (Lua CAS), `CompareAndDelete` (Lua CAD), `Marshal`/`Unmarshal` (CBOR), `LoadThrough[T]` (the read-through path — cache leg bounded by `cache.loadtimeout`, per-instance single-flight; [wiki/cache.md#load-through-reads](../wiki/cache.md#load-through-reads)). Per-tenant cache instances managed automatically (LRU eviction, idle cleanup, singleflight).

For lifecycle defaults, performance benchmarks, configuration, and multi-tenant patterns, see [wiki/cache.md](../wiki/cache.md).
