# Database Architecture (Deep Dive)

Unified `database.Interface` supporting PostgreSQL and Oracle with vendor-specific SQL generation, type-safe WHERE clauses, performance tracking via OpenTelemetry, connection pooling, and health monitoring.

**Package Structure:**
- `database/types/` — Core interfaces
- `database/internal/tracking/` — Performance metrics
- `database/internal/builder/` — Query builder implementations

| Database | Placeholders | Key Features |
|----------|--------------|--------------|
| **Oracle** | `:1`, `:2` | Automatic reserved word quoting, service name/SID options, **SEQUENCE support (built-in), UDT registration for custom types** |
| **PostgreSQL** | `$1`, `$2` | pgx driver with optimized connection pooling |

## Named Databases (Single-Tenant Multi-Database)

GoBricks supports accessing multiple databases in single-tenant mode, useful for legacy system migrations where applications need to access both old (e.g., Oracle) and new (e.g., PostgreSQL) databases.

**Configuration:**
```yaml
database:                # Default database (unchanged - backward compatible)
  type: postgresql
  host: primary.db.example.com
  port: 5432
  database: main_db

databases:               # Named databases — supports mixed vendors
  legacy:
    type: oracle
    host: legacy-oracle.example.com
    port: 1521
    service_name: LEGACYDB
    username: legacy_user
    password: ${LEGACY_DATABASE_PASSWORD}
  analytics:
    type: postgresql
    host: analytics.db.example.com
    port: 5432
    database: analytics_db
```

**Module Usage:**
```go
func (m *Module) Init(deps *app.ModuleDeps) error {
    m.getDB = deps.DB              // Default database (unchanged)
    m.getDBByName = deps.DBByName  // Named database access
    return nil
}

func (h *Handler) MigrateLegacyData(ctx context.Context) error {
    legacyDB, err := h.getDBByName(ctx, "legacy")
    if err != nil { return err }

    mainDB, err := h.getDB(ctx)
    if err != nil { return err }

    oracleQB := database.NewQueryBuilder(database.Oracle)
    query, args, _ := oracleQB.Select("*").From("OLD_USERS").ToSQL()
    rows, _ := legacyDB.Query(ctx, query, args...)
    // ... process and write to mainDB ...
    return nil
}
```

**Key Features:**
- Mixed vendor support per named database
- Backward compatible: `deps.DB(ctx)` works exactly as before
- Reuses infrastructure: same DbManager with LRU, connection pooling, idle cleanup
- Works with multi-tenant: named databases are shared across all tenants

## Struct-Based Column Extraction (v0.15.0+)

GoBricks eliminates column repetition through struct-based column management using `db:"column_name"` tags.

**Benefits:**
- **DRY:** Define columns once in struct tags, reference by field name
- **Type Safety:** Compile-time field name validation (panics on typos)
- **Vendor-Aware:** Automatic Oracle reserved word quoting
- **Zero Overhead:** One-time reflection (~2µs), cached forever (~50ns access)
- **Refactor-Friendly:** Rename struct fields → compiler catches all query references

**Quick Example:**
```go
type User struct {
    ID    int64  `db:"id"`
    Name  string `db:"name"`
    Level int    `db:"level"`  // Oracle reserved word — auto-quoted
}

cols := qb.Columns(&User{})  // Cached per vendor

query := qb.Select(cols.All()).From("users")
// or select specific fields:
query = qb.Select(cols.Cols("ID", "Name")).From("users")

query := qb.Select(cols.All()).
    From("users").
    Where(f.Eq(cols.Col("Level"), 5))
// Oracle: SELECT id, name, "level" FROM users WHERE "level" = :1

qb.Update("users").
    Set(cols.Col("Name"), "Jane").
    Where(f.Eq(cols.Col("ID"), 123))
```

**Service-Level Caching Pattern:**
```go
type ProductService struct {
    qb   *database.QueryBuilder
    cols dbtypes.Columns
}

func NewProductService(db database.Interface) *ProductService {
    qb := database.NewQueryBuilder(db.DatabaseType())
    return &ProductService{
        qb:   qb,
        cols: qb.Columns(&Product{}),
    }
}
```

**Performance:** First use ~2µs (reflection), cached access ~50ns, thread-safe via `sync.Map`.

**Type-Safe Methods:** `f.Eq`, `f.NotEq`, `f.Lt/Lte/Gt/Gte`, `f.In/NotIn`, `f.Like`, `f.Regex/RegexI/NotRegex/NotRegexI`, `f.JSONContains` (PostgreSQL only), `f.Null/NotNull`, `f.Between`.

**Escape Hatch:** `f.Raw(condition, args...)` (and `jf.Raw(...)` for JOIN conditions) — user must manually quote Oracle reserved words and parameterize all value sides. Every call site MUST carry a `// SECURITY: Manual SQL review completed - <rationale>` comment.

## Table Aliases

```go
type User struct {
    ID     int64  `db:"id"`
    Name   string `db:"name"`
    Status string `db:"status"`
}

type Profile struct {
    UserID int64  `db:"user_id"`
    Bio    string `db:"bio"`
}

qb := database.NewQueryBuilder(database.Oracle)
jf := qb.JoinFilter()
f := qb.Filter()

userCols := qb.Columns(&User{})
profileCols := qb.Columns(&Profile{})

u := userCols.As("u")
p := profileCols.As("p")

query := qb.Select(u.Col("ID"), u.Col("Name"), p.Col("Bio")).
    From(dbtypes.MustTable("users").MustAs("u")).
    LeftJoinOn(dbtypes.MustTable("profiles").MustAs("p"),
        jf.EqColumn(u.Col("ID"), p.Col("UserID"))).
    Where(f.Eq(u.Col("Status"), "active"))
// Oracle: SELECT u."ID", u."NAME", p."BIO" FROM users u LEFT JOIN profiles p ON u."ID" = p."USER_ID" WHERE u."STATUS" = :1
```

## Mixed JOIN Conditions (v2.2+)

```go
jf := qb.JoinFilter()
f := qb.Filter()

query := qb.Select("*").
    From(dbtypes.MustTable("orders").MustAs("o")).
    JoinOn(dbtypes.MustTable("customers").MustAs("c"), jf.And(
        jf.EqColumn("c.id", "o.customer_id"),         // Column-to-column
        jf.Eq("c.status", "active"),                  // Column-to-value
        jf.In("c.tier", []string{"gold", "platinum"}),
    )).
    JoinOn(dbtypes.MustTable("products").MustAs("p"), jf.And(
        jf.EqColumn("p.id", "o.product_id"),
        jf.Eq("p.price", qb.MustExpr("TO_NUMBER(o.max_price)")),
    )).
    Where(f.Eq("o.status", "pending"))
```

**Available Methods:** `Eq`, `NotEq`, `Lt/Lte/Gt/Gte`, `In/NotIn`, `Between`, `Like`, `Null/NotNull`.

**Expression Support:** All comparison methods accept `qb.Expr()` for complex SQL expressions without placeholders.

**Raw() Escape Hatch:** Use `jf.Raw()` only for conditions type-safe methods cannot express (e.g., spatial functions, exotic operators).

## Subquery Support

```go
type Review struct {
    ProductID int64 `db:"product_id"`
    Rating    int   `db:"rating"`
}

reviewCols := qb.Columns(&Review{})
productCols := qb.Columns(&Product{})

p := productCols.As("p")

subquery := qb.Select("1").From("reviews").
    Where(f.And(
        f.Eq("reviews."+reviewCols.Col("ProductID"), qb.MustExpr(p.Col("ID"))),
        f.Eq(reviewCols.Col("Rating"), 5),
    ))

query := qb.Select(p.Col("Name")).
    From(dbtypes.MustTable("products").MustAs("p")).
    Where(f.Exists(subquery))
```

**Methods:** `f.Exists(subquery)`, `f.NotExists(subquery)`, `f.InSubquery(column, subquery)`. Supports correlated and nested subqueries.

## SELECT Expressions (v2.1+)

```go
query := qb.Select(
    cols.Col("Category"),
    qb.MustExpr("COUNT(*)", "product_count"),
    qb.MustExpr("AVG(price)", "avg_price"),
).From("products").GroupBy(cols.Col("Category"))
```

**SECURITY WARNING:** Raw SQL expressions are NOT escaped. Never interpolate user input:

```go
qb.MustExpr("COUNT(*)", "total")                  // SAFE
qb.MustExpr(fmt.Sprintf("UPPER(%s)", userInput))  // SQL INJECTION
```

Use WHERE with placeholders for dynamic values: `qb.Select("*").From("users").Where(f.Eq(userColumn, userValue))`.

## Identifier Validation (ADR-031)

The string-identifier arguments of `From`, the JOIN family (`JoinOn`/`LeftJoinOn`/`RightJoinOn`/`InnerJoinOn`/`CrossJoinOn`), `OrderBy`, `GroupBy`, `Set`, `SetMap`, and `DeleteQueryBuilder.OrderBy` must be **developer-controlled, not user input**. As of ADR-031 these arguments are validated against a safe identifier grammar on **all vendors** (PostgreSQL and Oracle) *before* interpolation:

- **Table args** (`From`, JOIN tables): a simple or qualified identifier — `col`, `table.col`, `schema.table.col` — plus an optional inline alias (`"users u"`). A `*TableRef` from `Table("users").As("u")` validates its name and alias separately.
- **Identifier args** (`Set`, `SetMap` columns): a simple or qualified identifier plus the framework's own quoted reserved-word output (`"level"`).
- **Clause args** (`OrderBy`, `GroupBy`, `DeleteQueryBuilder.OrderBy`): the above plus an optional bounded direction — `col ASC|DESC [NULLS FIRST|LAST]`.

Anything outside the grammar — quotes used for injection, embedded whitespace, semicolons, `--` / `/* */` comment sequences, function calls, or extra tokens — is **rejected as a `ToSQL()` error** (the fluent methods cannot return errors, so the violation is deferred to `ToSQL()`; they never panic on bad identifier content). This closes the injection vector where `.OrderBy(userInput)` with a value like `"name; DROP TABLE users--"` was previously interpolated verbatim on PostgreSQL.

```go
qb.Select("*").From("users").OrderBy("name ASC")                 // SAFE
qb.Select("*").From("users").OrderBy("COUNT(*) DESC")            // REJECTED → use qb.MustExpr("COUNT(*) DESC")
qb.Select("*").From("users").OrderBy(req.Query("sort"))          // REJECTED if the value isn't a bare column+direction
```

Valid identifiers on PostgreSQL are left **unquoted** (PG folds unquoted identifiers to lowercase; quoting would change which physical column is referenced). Complex or computed expressions must go through `qb.Expr()`/`Raw()`, which carry an explicit `// SECURITY:` annotation. Pass user **values** through the parameterized Filter API (`f.Eq`, etc.).

## Connection Pool Defaults

| Setting | Default | Purpose |
|---------|---------|---------|
| `pool.max.connections` | 25 | Maximum open connections |
| `pool.idle.connections` | tracks `pool.max.connections` | Idle connection cap (not a floor — no pre-warming). Tracking max avoids connection churn under load; `database/sql` clamps it to max |
| `pool.idle.time` | 5m | Close idle connections (prevents stale connections) |
| `pool.lifetime.max` | 30m | Force periodic recycling (DNS, memory hygiene) |
| `pool.keepalive.enabled` | true | TCP keep-alive probes |
| `pool.keepalive.interval` | 60s | Probe interval (below NAT timeouts) |

> **Idle defaults to max (changed in [ADR-025](adr_025_pool_idle_tracks_max.md)).** Earlier versions defaulted idle to a fixed `2`, which made the pool repeatedly open and close physical connections (TCP+TLS+auth) under sustained load. Idle now defaults to `pool.max.connections` so warm connections are reused. Set a lower `pool.idle.connections` explicitly only when you deliberately want idle connections released back to the database. See [migrations.md](migrations.md#connection-pool-idle-default--tracks-max-adr-025) for the footprint implications.

**Cloud Provider Idle Timeouts:**
| Provider | Component | Timeout |
|----------|-----------|---------|
| AWS | NAT Gateway/ALB | 350s |
| GCP | Cloud NAT | 30s |
| Azure | NAT Gateway | 240s |
| On-prem | Firewalls | 60-300s |

**Override defaults:**
```yaml
database:
  pool:
    idle:
      time: 3m
    lifetime:
      max: 15m
    keepalive:
      interval: 30s
```

### Per-tenant connection manager sizing (multi-tenant)

The `pool.*` settings above govern the connection pool **within a single database**. In multi-tenant mode there is a second, outer cap: the `DbManager` keeps at most one connection per tenant key in an LRU cache whose size is `multitenant.limits.tenants`. This is an LRU cap, not a per-tenant guarantee — when more tenants are active than the limit, every request for a not-currently-cached tenant evicts the least-recently-used connection and opens a fresh one. That **eviction thrash** silently degrades latency (each miss pays the full connect cost) without surfacing an error.

Size `multitenant.limits.tenants` to at least the number of tenants you expect to serve concurrently. For **statically-configured** tenants (`multitenant.tenants`) the framework counts them at startup and emits a **WARN** when the manager's max size is below the configured tenant count. For **dynamic** tenant sources the count is unknown at startup, so no warning can be emitted — size the limit against your expected fleet manually.

> Eviction (and idle cleanup) closes the evicted connection **outside** the manager lock, so a slow `Close()` on an evicted tenant never blocks concurrent `Get()` calls for other tenants.
>
> A connection that is **still in use** when evicted (held by an in-flight request, message, or job) is detached from the cache immediately but its `Close()` is **deferred until the last borrower releases its lease** — so an in-use connection is never closed under an active caller ([ADR-032](adr_032_lease_refcount_tenant_handles.md), the M3 fix). The lease is reference-counted by `DbManager` and released by the framework at each request/message/job boundary; **application code is unchanged** (`deps.DB(ctx)` keeps its `(Interface, error)` signature). Direct callers of `DbManager.Get` see a new `ReleaseFunc` third return — see [migrations.md](migrations.md).

### Connection-manager pool tunables (`database.manager.*`)

The manager's own lifecycle is operator-tunable, matching the `messaging.publisher.*` and `cache.manager.*` surfaces. All three keys default to today's hardcoded behavior, so leaving them unset changes nothing.

| Key | Default (single-tenant) | Default (multi-tenant) | Purpose |
|-----|-------------------------|------------------------|---------|
| `database.manager.maxsize` | `10` | `multitenant.limits.tenants` | Max cached database handles (LRU cap) |
| `database.manager.idlettl` | `1h` | `30m` | Idle timeout before a cached handle is closed |
| `database.manager.cleanupinterval` | `5m` | `5m` | How often the background cleanup sweep runs |

```yaml
database:
  manager:
    maxsize: 20       # raise the LRU cap above the default 10 single-tenant handles
    idlettl: 2h
    cleanupinterval: 10m
```

Each key also binds from the environment (`DATABASE_MANAGER_MAXSIZE`, `DATABASE_MANAGER_IDLETTL`, `DATABASE_MANAGER_CLEANUPINTERVAL`); negative values fail startup naming the key. In multi-tenant mode a zero/unset `maxsize` is **preserved** so the manager keeps scaling the cap to `multitenant.limits.tenants` — set an explicit positive value only to override that scaling.

These keys are set under the primary `database:` section only, but they govern the **single process-wide manager**, which caches the primary handle, every named `databases.<name>` handle (keyed `named:<name>` via `deps.DBByName`), and per-tenant handles. **Count named databases when sizing `maxsize`**: a single-tenant app with the primary plus 12 named databases needs `maxsize >= 13` to avoid LRU eviction churn. A `manager` sub-block under `databases.<name>` or `multitenant.tenants.<id>.database` is rejected at startup — it would otherwise be silently ignored.

## Repository Method Attribution

The `db.client.operation.duration` metric carries `db.operation.name` (the SQL verb:
`select`, `insert`, …) but not which application method issued the query. To attribute
query latency to a business operation in dashboards (e.g. `GetCustomer` vs
`InsertTransaction`), tag the context before the call with `database.WithRepositoryMethod`:

```go
func (r *CustomerRepo) GetCustomer(ctx context.Context, id int64) (*Customer, error) {
    ctx = database.WithRepositoryMethod(ctx, "GetCustomer")
    row := r.db.QueryRow(ctx, query, id)
    // ...
}
```

The tracking layer reads the value and adds it as the `repository.method` attribute on
the duration histogram. The attribute is omitted entirely when unset (no empty-string
series). `database.RepositoryMethodFromContext(ctx)` reads the value back for custom
instrumentation.

**Cardinality contract:** the method name MUST be a static, low-cardinality identifier
(a method or function name). Because it becomes a metric attribute, interpolating
per-request data such as IDs or emails would explode metric cardinality.

## Session Timezone (Breaking Change — ADR-016)

| Setting | Default | Purpose |
|---------|---------|---------|
| `database.timezone` | `UTC` | IANA timezone applied per session (PostgreSQL via pgx `RuntimeParams`, Oracle via `ALTER SESSION SET TIME_ZONE` on every new physical connection) |

**Behavior:**
- Unset / empty → defaulted to `UTC` at config validation
- IANA name (`Asia/Tokyo`, `America/New_York`) → validated via `time.LoadLocation`, applied per-connection
- `-` sentinel → opt-out; sessions inherit the database server's default (legacy behavior)
- Numeric offsets like `+05:30` → rejected by validation. Use IANA `Etc/GMT±N` (note inverted sign)

**Why per-connection?** A single `SET TIME ZONE` after `sql.Open` only fixes the first borrowed connection — later pool members revert to the server default. The implementation routes through `pgx.RuntimeParams` and an Oracle `driver.Connector` wrapper so every new physical connection inherits the configured timezone.

```yaml
database:
  timezone: Asia/Tokyo   # Apply Tokyo time to every session

# Or preserve legacy behavior:
database:
  timezone: "-"
```

## Oracle SEQUENCE Objects (No Configuration Required)

```go
var id int64
err := conn.QueryRow(ctx, "SELECT user_seq.NEXTVAL FROM DUAL").Scan(&id)

_, err = conn.Exec(ctx, "INSERT INTO users VALUES (user_seq.NEXTVAL, :1)", name)
```

**No UDT registration needed** — SEQUENCE returns standard NUMBER type.

## Oracle User-Defined Types (Require Registration)

For custom object/collection types created with `CREATE TYPE`:

```go
type Product struct {
    ID    int64   `udt:"ID"`
    Name  string  `udt:"NAME"`
    Price float64 `udt:"PRICE"`
}

oracleConn := conn.(*oracle.Connection)
err := oracleConn.RegisterType("PRODUCT_TYPE", "PRODUCT_TABLE", Product{})

products := []Product{{ID: 1, Name: "Widget", Price: 19.99}}
_, err = conn.Exec(ctx, "BEGIN bulk_insert_products(:1); END;", products)
```

**When required:** Bulk insert/update with TABLE OF collections, stored procedures with custom object parameters, functions returning complex types.

**Common Error:** `"call register type before use user defined type"` — call `RegisterType()` during initialization.
