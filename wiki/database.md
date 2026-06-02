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

    oracleQB := builder.NewQueryBuilder(dbtypes.Oracle)
    rows, _ := legacyDB.Query(ctx, oracleQB.Select("*").From("OLD_USERS").Build())
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
- **Zero Overhead:** One-time reflection (~0.6µs), cached forever (~26ns access)
- **Refactor-Friendly:** Rename struct fields → compiler catches all query references

**Quick Example:**
```go
type User struct {
    ID    int64  `db:"id"`
    Name  string `db:"name"`
    Level int    `db:"level"`  // Oracle reserved word — auto-quoted
}

cols := qb.Columns(&User{})  // Cached per vendor

query := qb.Select(cols.All()...).From("users")
// or select specific fields:
query = qb.Select(cols.Cols("ID", "Name")...).From("users")

query := qb.Select(cols.All()...).
    From("users").
    Where(f.Eq(cols.Col("Level"), 5))
// Oracle: SELECT "ID", "NAME", "LEVEL" FROM users WHERE "LEVEL" = :1

qb.Update("users").
    Set(cols.Col("Name"), "Jane").
    Where(f.Eq(cols.Col("ID"), 123))
```

**Service-Level Caching Pattern:**
```go
type ProductService struct {
    qb   *builder.QueryBuilder
    cols dbtypes.Columns
}

func NewProductService(db database.Interface) *ProductService {
    qb := builder.NewQueryBuilder(db.DatabaseType())
    return &ProductService{
        qb:   qb,
        cols: qb.Columns(&Product{}),
    }
}
```

**Performance:** First use ~0.6µs (reflection), cached access ~26ns, thread-safe via `sync.Map`.

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

qb := builder.NewQueryBuilder(dbtypes.Oracle)
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
    Where(jf.And(
        jf.EqColumn("reviews."+reviewCols.Col("ProductID"), p.Col("ID")),
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

## Connection Pool Defaults

| Setting | Default | Purpose |
|---------|---------|---------|
| `pool.max.connections` | 25 | Maximum open connections |
| `pool.idle.connections` | 2 | Minimum warm connections |
| `pool.idle.time` | 5m | Close idle connections (prevents stale connections) |
| `pool.lifetime.max` | 30m | Force periodic recycling (DNS, memory hygiene) |
| `pool.keepalive.enabled` | true | TCP keep-alive probes |
| `pool.keepalive.interval` | 60s | Probe interval (below NAT timeouts) |

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
