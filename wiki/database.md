# Database Architecture (Deep Dive)

Unified `database.Interface` supporting PostgreSQL and Oracle with vendor-specific SQL generation, type-safe WHERE clauses, performance tracking via OpenTelemetry, connection pooling, and health monitoring.

**Package Structure:**

- `database/types/` — Core interfaces
- `database/internal/tracking/` — Performance metrics
- `database/internal/builder/` — Query builder implementations

| Database | Placeholders | Key Features |
| --- | --- | --- |
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
    oracle:
      service:
        name: LEGACYDB
    username: legacy_user
    password: ${LEGACY_DATABASE_PASSWORD}
  analytics:
    type: postgresql
    host: analytics.db.example.com
    port: 5432
    database: analytics_db
```

**Naming rule (enforced at startup).** A key under `databases` must match `^[a-z0-9-]+$` —
lowercase letters, digits and hyphens. An environment variable is mapped to a config key by
lowercasing it and turning `_` into `.`, so `DATABASES_REPORT_DB_PORT` reaches
`databases.report.db.port`: a section named `report_db` is addressable by no variable at all, and
where a sibling `databases.report` exists the variable is applied to the sibling's subtree instead —
dropped where the remaining segments name no field, or landing on the sibling's own setting where
they do. `config.Validate`
rejects such a name and says which key to rename ([ADR-090](adr_090_env_reachable_section_names.md)).
A hyphenated name is legal config, but whether it is *settable* from the environment depends on the
runtime: Docker and Kubernetes permit `-` in a variable name, POSIX `export` does not.

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

**Operand contract (both families, nine value doors — `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, `Between`, `In`, `NotIn`):** `f` and `jf` resolve an operand the same way before doing anything with it — a NIL pointer is nil before it is asked for anything, then a `driver.Valuer` becomes its `Value()`, then a pointer becomes its element — and classify the RESULT as NIL (`nil`, a typed nil pointer, a Valuer reporting NULL), a LIST (a slice or an array), or a SCALAR (anything else, `[]byte` and a Valuer holding a value included). A `dbtypes.RawExpression` operand (`qb.Expr(…)`) is spliced before all of this — its SQL goes into the predicate verbatim, with no placeholder and no argument — so it is neither resolved nor classified.

- **Equality** (`Eq`, `NotEq`): NIL renders `IS NULL` / `IS NOT NULL`; a LIST expands to `IN (…)` / `NOT IN (…)`, an empty one to the constant `(1=0)` / `(1=1)`; a SCALAR is bound to `col op ?` whatever its Go type — a struct no driver accepts is passed through, not diagnosed.
- **Ordering** (`Lt`, `Lte`, `Gt`, `Gte`) and both bounds of `Between`: a NIL or LIST operand is refused with `dbtypes.ErrOrderingOperandNotComparable` (exported from `database/types`, so `errors.Is` works from your own code), surfaced at `ToSQL()`. There is no rendering for `col < NULL` or for an ordering against a set, so the door fails closed rather than emitting SQL that silently matches nothing.
- **`In` / `NotIn`**: every ELEMENT is resolved the same way, so a nil-pointer element binds as an untyped nil rather than reaching the driver as a pointer it would have to dereference. A scalar operand is wrapped in a one-element list — a `[]byte` included, so it renders `IN (?)` here rather than the `col = ?` a `[]byte` takes at the compare doors — and an empty operand renders `(1=0)` / `(1=1)`.

Every door binds the value it RESOLVED (`int64(5)`), never the wrapper (`sql.NullInt64{5,true}`) or the pointer. The driver receives the same value either way — it is the value `database/sql` would have unwrapped at bind time — so only something reading `ToSQL()`'s args back, a golden file or a contract test, can see the difference.

One divergence remains between the families, and it is spelling, not meaning: for a SCALAR operand `jf.NotEq` renders `!=` where `f.NotEq` renders `<>` (#1200). Prefer the explicit spelling — `f.In`/`f.Null` and `jf.In`/`jf.Null` — when you know the shape: it says what you mean and does not depend on a runtime type test. And note what the contract makes of a nil check at a call site: an UNGUARDED nil operand MEANS `IS NULL`, so a guard around such a call is no longer a workaround — it is the only way left to say "emit no predicate at all". Delete it only if you meant `IS NULL`.

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

**Available Methods:** `Eq`, `NotEq`, `Lt/Lte/Gt/Gte`, `In/NotIn`, `Between`, `Like`, `Null/NotNull`. Nine of them follow the *Operand contract* above — the six compare doors, `In`, `NotIn` and `Between`, whose two bounds are each resolved as an ordering operand. `Like` does NOT: it takes a string pattern, so there is no operand to resolve.

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

subquery := qb.Select(qb.MustExpr("1")).From("reviews").
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

Use WHERE with placeholders for dynamic **values**: `qb.Select("*").From("users").Where(f.Eq(cols.Col("Status"), userValue))`. Note the column is a struct-tag lookup, not a variable: the value is parameterized, the column is interpolated, and only the value may come from the caller.

## Identifier Validation (ADR-031, ADR-082)

Every **identifier argument** — a caller-supplied string that becomes SQL *syntax*
rather than a bound value — must be developer-controlled, not user input. All of
them are validated against a safe identifier grammar on **both vendors** before
interpolation, and a value outside the grammar is refused.

Where it surfaces depends on the shape of the call. The fluent builders cannot
return an error mid-chain, so the violation is deferred and comes back from
`ToSQL()`. Two doors answer elsewhere. `BuildUpsert` builds a statement directly
and returns the error as its own third value, so a caller checking only `ToSQL()`
would look in the wrong place. `cols.As(alias)` returns a `Columns`, not a
builder, so it has no error channel at all and **panics** with a
`*dbtypes.InvalidAliasError` — at the `As` call, before any statement is built.

| Door | Grammar |
| --- | --- |
| `From`, the JOIN family, `Insert`/`InsertWithColumns`/`InsertStruct`/`InsertFields`, `BuildUpsert` — table args | simple or qualified identifier plus one optional inline alias (`users u`) |
| `Set`, `SetMap`, `InsertWithColumns`/`.Columns`/`.SetMap` — column args | simple or qualified identifier, plus the framework's own quoted reserved-word output (`"level"`) |
| `OrderBy`, `GroupBy`, `DeleteQueryBuilder.OrderBy` | the above plus a bounded direction — `col ASC\|DESC [NULLS FIRST\|LAST]` |
| `Select` column list | the above plus the wildcard — `*`, `t.*` |
| **Every `Filter` and `JoinFilter` column** — `f.Eq`, `f.In`, `f.Like`, `f.Between`, `jf.EqColumn`, … | simple or qualified identifier |
| `cols.As(alias)` — the table alias that qualifies the columns | a single bare identifier (`u`), or the framework's own quoted form (`"level"`) — **panics**, see above |

```go
qb.Select("*").From("users").OrderBy("name ASC")          // SAFE
qb.Select("*").From("users").OrderBy("COUNT(*) DESC")     // REJECTED → qb.MustExpr("COUNT(*) DESC")
qb.Select("*").From("users").OrderBy(req.Query("sort"))   // REJECTED unless it is a bare column+direction
qb.Select("COUNT(*)")                                     // REJECTED → qb.MustExpr("COUNT(*)")
qb.Select("1")                                            // REJECTED → qb.MustExpr("1")
f.Eq(req.Query("field"), value)                           // REJECTED unless it is a bare column
cols.As("u")                                              // SAFE
cols.As("id FROM secrets--")                              // PANICS at the As call
```

### Validating an identifier yourself (`database/identifier`)

A service that must check a schema, role or table name it read from a secret
store *before* opening a connection can call the exported validator instead of
re-declaring the grammar. `database/identifier` is a leaf package: it imports
only the standard library and `database/types`.

```go
import (
    "github.com/gaborage/go-bricks/database/identifier"
    dbtypes "github.com/gaborage/go-bricks/database/types"
)

if err := identifier.Validate(dbtypes.PostgreSQL, schema); err != nil {
    return fmt.Errorf("schema from vault: %w", err) // errors.Is(err, identifier.ErrIdentifierTooLong) …
}
```

`Validate` takes **one bare, unquoted segment** — no dots, alias, or wildcard;
validate `schema.table` one segment at a time. The vendor's grammar is the
vendor's truth:

| Vendor | Grammar | Cap |
| --- | --- | --- |
| `dbtypes.PostgreSQL` | `^[A-Za-z_][A-Za-z0-9_$]*$` | 63 **bytes** (NAMEDATALEN-1; longer names are silently truncated, so two 64-byte names sharing a prefix collapse onto one object) |
| `dbtypes.Oracle` | `^[A-Za-z_][A-Za-z0-9_$#]*$` | 128 **bytes** (Oracle 12.2+) |
| anything else | — | `ErrUnsupportedVendor` |

The value is validated as given — never trimmed. Mixed case is accepted, but
the server folds an unquoted identifier (PostgreSQL to lowercase, Oracle to
uppercase). Each refusal class has its own `errors.Is`-able sentinel, wrapped
with the offending value: `ErrEmptyIdentifier`, `ErrIdentifierCharset`,
`ErrIdentifierTooLong`, `ErrUnsupportedVendor`. The `migration` package's role,
schema and quiesce-table validators delegate to it.

### The Filter API parameterizes values; that is not the same as validating columns

`f.Eq(column, value)` takes **two** arguments. The value becomes a placeholder.
The column becomes syntax. Reading the first property as the second is exactly
what left the column doors unvalidated between ADR-031 and ADR-082 — on
PostgreSQL, where identifiers render unquoted, `f.Eq("id = 1 OR 1=1 -- ", v)`
built `WHERE id = 1 OR 1=1 -- = $1`. Both halves are guarded now, but they are
guarded by different mechanisms, and only the value side is safe *because* it is
user input.

For a genuinely dynamic column — a sortable table, a "filter by field" endpoint —
map the caller's value to one of a fixed set your own code owns before it reaches
the builder. The grammar will not accept a computed one.

### What is not an identifier door

- **`Having`** takes a *predicate*, not an identifier, so no identifier grammar
  can judge it and its argument is interpolated as written. Treat a STRING
  predicate as raw SQL — it is annotated like `f.Raw` (see the door list below);
  `Having(qb.MustExpr(...))` is the sanctioned expression form and is not.
  `InsertQueryBuilder.Prefix`, `.Suffix` and `.Options` are the same shape, and
  unlike `Having` they have no `qb.Expr()` alternative.
- **`qb.Expr()` / `MustExpr()`** are the declared expression hatches: they exist
  to carry SQL the grammar refuses, and the builder still places what they
  produce. They carry no annotation requirement. The builder judges neither the
  syntax nor the semantics of the SQL they carry — it does reject an empty or
  whitespace-only body — and the rest of a `RawExpression` is validated too: a
  struct literal built without the constructor is checked where it is consumed,
  so an empty SQL body, or an alias that is not an unquoted identifier, fails from
  `ToSQL()` (ADR-082 2026-08-24 addendum). The alias was a six-substring denylist
  until `[C61.9]` replaced it with the grammar.
- **`f.Raw()`, `jf.Raw()`, `database.Raw()` and a STRING predicate passed to
  `Having()`** do. Each admits arbitrary SQL — the first two a WHERE/JOIN fragment,
  `database.Raw` the whole statement, `Having` the group predicate — and each
  requires an inline `// SECURITY: Manual SQL review completed - <rationale>`
  comment at every call site, which is what makes them grep-discoverable.
  `Having` also takes a `qb.Expr()` `RawExpression`, which is the preferred
  spelling and carries no annotation duty; an alias on it is an error, since a
  predicate projects nothing. That exemption is for CONSISTENCY with
  `Select`/`GroupBy`/`OrderBy`, **not** a safety claim: `RawExpression.Validate()`
  checks only that the SQL is non-empty and the alias is clean — it never inspects
  the SQL body, which carries the same injection risk as the string form and is
  reviewed as raw SQL. Its audit hook is its own name, `git grep -nE
  'MustExpr\(|[.]Expr\(|RawExpression\{'`, rather than an annotation.
- **`BuildUpsert`'s column maps** answer to the upsert's own preconditions rather
  than to this grammar — a stricter question ("is this one column the vendor's
  upsert syntax can name"). Since `[C61.15]` that question has **one answer on
  both vendors**: a conflict/insert/update key is trimmed, then must be a single
  column name carrying no escaping of its own beyond a plain wrapping quote pair,
  so `COUNT(*)`, `t.name`, `a""b` and `"a""b"` are refused on PostgreSQL too. The
  trimmed spelling is the one rendered, and identity is the RENDERED identifier on
  both vendors — so a key written with surrounding spaces matches its unpadded
  insert key, a map holding both spellings is rejected as one column written
  twice, and on PostgreSQL `ID` and `"ID"` are one column (they render alike)
  while `id` and `ID` stay two.

Valid identifiers on PostgreSQL are left **unquoted**: PostgreSQL folds unquoted
identifiers to lowercase, so quoting a valid one would change which physical
column is referenced. Where a renderer does quote, an interior quote is doubled,
so a name carrying one renders as that name instead of ending the identifier
early.

## Database-Free Services and Readiness (ADR-047)

The `database:` block is all-or-nothing. Omit it entirely and the service is
database-free: `/ready` reports `database: "not_configured"` and returns **200**, and
`deps.DB(ctx)` returns an error satisfying `config.IsNotConfigured`. Set *any* identity
field — `type`, `host`, `port`, `database`, `username`, `password`, `connectionstring`,
`oracle.service.name`, `oracle.service.sid` — and the section counts as intended, so an incomplete one **fails startup** rather than
loading and failing at first query. Complete means `type` + `host` + `port` + `username`
plus a target (`database`, or for Oracle `oracle.service.name` / `oracle.service.sid`).

That strictness is the point: an empty section carries no intent, so a dropped secret
mount looks identical to a deliberately database-free service. Making the predicate strict
means only a *literally empty* section is absence.

| Config | Startup | `/ready` |
| --- | --- | --- |
| No `database:` block | starts (one advisory WARN) | 200 · `not_configured` |
| Complete and reachable | starts | 200 · `healthy` |
| Complete but unreachable | starts | **503** — the probe stays `critical` |
| Any identity field, incomplete | **fails** | n/a |
| Multi-tenant | starts | 200 · `per_tenant` |

Two consequences worth knowing:

- **Multi-tenant `/ready` carries no database signal.** The probe
  resolves the fixed `""` key. With static tenants, validation rejects a root block
  outright, so that key cannot resolve and no tenant database has ever been probed —
  `per_tenant` states that plainly rather than claiming the service has no database.
  A multi-tenant deployment that *does* configure a root block (a shared-ledger
  control plane, `outbox.tenancy: shared`) is still probed and still `critical`.
  Where the key genuinely does not resolve, `/ready` carries no database signal at
  all: no critical probe, no startup gate, and no WARN.
- **A module that genuinely needs a database should say so.** Implement
  `app.DatabaseRequirer`; registration then aborts startup when the database is absent,
  instead of the service going green and serving errors.

## Connection String Type Inference (ADR-050)

A `connectionstring` alone used to pass validation and then never connect —
`database.NewConnection` dispatches on `type`, so an untyped DSN failed only at first
query. `type` is now inferred from a recognized scheme when it is omitted:
`postgres://`/`postgresql://` → `postgresql`, `oracle://` → `oracle`. Surrounding
whitespace does not defeat the match (a DSN read from a mounted secret often carries a
trailing newline), and the stored connection string is never rewritten — only the
classification tolerates it.

Inference runs at **two** sites, not one. `config.Validate` covers every statically
configured path (`database`, `databases.<name>`, `multitenant.tenants.<id>.database`);
`config.ApplyDatabasePoolDefaults` — the seam `database.DbManager` applies to every config
a dynamic `DBConfigProvider` returns, which never reaches `Validate` — covers the dynamic
path. Both delegate to the same scheme list, so extending it is one edit. An explicit
`type` that conflicts with the inferred scheme is a validation error on the `Validate`
path only: the seam runs per connection, where the vendor's own dial error is the better
failure.

Any other scheme leaves `type` empty — the *effect* of an unrecognized scheme depends on
the connector: the built-in one (`database.NewConnection`) fails startup with a
`connectionstring has no resolved database type` error naming every affected static path;
a caller-supplied `Options.DatabaseConnector` parses the DSN itself and is exempt **from
that startup guard**. It is not exempt from inference: the config layer is
connector-blind, so a custom connector receives `type` already resolved for a recognized
scheme, and one that branches on an empty `cfg.Type` to decide whether to parse the DSN
must be reviewed.

**An Oracle DSN needs no separate identifier.** `oracle://user:pw@host:1521/XE` alone is a
complete config: `buildOracleDSN` returns the connection string verbatim, so
`oracle.service.name`, `oracle.service.sid` and `database` are never consulted in that mode
and none of them is required. Without a connection string the rule is unchanged — exactly
one of the three, and zero or several is still a validation error. The Oracle TLS rejection
is unconditional either way: the whole `database.tls` block — `mode` included — fails
validation even alongside a connection string, because tcps/wallet is not implemented and
silently ignoring TLS settings would leave the operator believing the connection is
encrypted.

## TLS (`database.tls`)

Startup validation rejects every `database.tls` shape pgx would silently discard,
downgrade, or (for `ca: system`) invert
([ADR-062](adr_062_database_tls_fail_closed.md)). All four fields are trimmed
before the checks run. The rules fire at startup wherever `config.Validate` does — the root
`database:` block, every named database, every static tenant entry — and, since #1002, at
connection acquisition for dynamic `DBConfigProvider` records (see "These rules reach dynamic
configs too" below).

- **PostgreSQL — `mode` is an allowlist.** `disable`, `allow`, `prefer`, `require`,
  `verify-ca`, `verify-full`, or unset. A misspelled or wrongly-cased value (`Require`,
  `verify_full`) is a startup error naming `database.tls.mode`; the framework does not
  case-fold, because pgx is case-sensitive and the typo is worth reporting.
- **PostgreSQL — material demands a TLS-mandatory mode.** `cert`, `key` or `ca` may be set
  only under `require`, `verify-ca` or `verify-full`. Under `disable` pgx returns a nil TLS
  config before it reads the certificate files, and under an unset mode (which defaults to
  `prefer`), `allow` or `prefer` it sets `InsecureSkipVerify` plus a plaintext fallback — so
  the material was being discarded or the connection silently downgraded (`ca: system`
  inverted instead: pgx force-upgrades that sentinel to `verify-full` — see the quirks
  below). `cert` and `key` must still be set together.
- **PostgreSQL — a valid mode alone is always fine.** `mode: disable` with no material stays
  valid; opportunistic TLS with nothing to discard is a legitimate choice.
- **`database.tls` is incompatible with `connectionstring`.** The DSN is used verbatim and
  the block never reaches it, so setting both is a startup error. Put the parameters in the
  DSN instead (`sslmode`, `sslrootcert`, `sslcert`, `sslkey`) — that is also the escape hatch
  for pgx-native semantics these rules refuse, such as `prefer` with a client certificate.
- **Oracle rejects the whole block**, `mode` included (see above).

Two pgx quirks worth knowing when choosing a mode: `require` plus `ca` behaves as
`verify-ca` (a documented libpq inheritance), and the sentinel `ca: system` means the OS
trust store and forces `verify-full` regardless of the configured mode. `require` without
`ca` provides encryption without server authentication — pair it with `ca`, or use
`verify-ca`/`verify-full`, wherever the peer's identity matters.

**These rules reach dynamic configs too.** `ApplyDatabasePoolDefaults` runs the same
vendor-specific validation as `Validate`, so a config a `DBConfigProvider` returns —
inferred or explicitly typed — fails at connection acquisition where it previously
connected with the TLS material silently dropped. A provider that returns a nil config with a nil
error violates the interface contract: `DbManager.Get` rejects it with an error wrapping
`database.ErrNoDatabaseConfig` naming the key, rather than dereferencing it.

## Connection Pool Defaults

| Setting | Default | Purpose |
| --------- | --------- | --------- |
| `pool.max.connections` | 25 | Maximum open connections |
| `pool.idle.connections` | tracks `pool.max.connections` | Idle connection cap (not a floor — no pre-warming). Tracking max avoids connection churn under load; `database/sql` clamps it to max |
| `pool.idle.time` | 5m | Close idle connections (prevents stale connections) |
| `pool.lifetime.max` | 30m | Force periodic recycling (DNS, memory hygiene) |
| `pool.keepalive.enabled` | true | TCP keep-alive probes |
| `pool.keepalive.interval` | 60s | Probe interval (below NAT timeouts) |

> **Idle defaults to max (changed in [ADR-025](adr_025_pool_idle_tracks_max.md)).** Earlier versions defaulted idle to a fixed `2`, which made the pool repeatedly open and close physical connections (TCP+TLS+auth) under sustained load. Idle now defaults to `pool.max.connections` so warm connections are reused. Set a lower `pool.idle.connections` explicitly only when you deliberately want idle connections released back to the database. See [migrations.md](migrations.md#c416-pool-idle-connections-default-now-tracks-poolmaxconnections-was-fixed-2--silent-behavior--when-no-match) for the footprint implications.

**Cloud Provider Idle Timeouts:**

| Provider | Component | Timeout |
| ---------- | ----------- | --------- |
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
| ----- | ------------------------- | ------------------------ | --------- |
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

Each key also binds from the environment (`DATABASE_MANAGER_MAXSIZE`, `DATABASE_MANAGER_IDLETTL`, `DATABASE_MANAGER_CLEANUPINTERVAL`); negative values fail startup naming the key. In multi-tenant mode a zero/unset `maxsize` is **preserved** so the manager keeps scaling the cap to `multitenant.limits.tenants` — set an explicit positive value only to override that scaling. The sweep starts when the manager is constructed — before the first request — and stops in `DbManager.Close()`; calling `StartCleanup` yourself is not required, and a second call while a loop is already running is a no-op.

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

## Execute Helpers

`database.ExecuteQuerySingle` / `ExecuteQueryMany` / `ExecuteUpdate` / `ExecuteUpdateOne` / `ExecuteInsert` collapse the repeated `ToSQL()` → `Query`/`Exec` → `Scan`/`RowsAffected` → error-wrap glue that every SQL repository re-implements. Each helper takes a `database.Executor` (a 2-method `Query`/`Exec` interface satisfied by both `database.Interface` and `database.Tx`, so the same call works inside or outside a transaction) and a `database.SQLProvider` (implemented by every query-builder result, or by `database.Raw(sql, args...)` for hand-written SQL). An `op string` label identifies the call site in errors — it labels errors only and does not feed metrics or tracing; use `database.WithRepositoryMethod(ctx, ...)` for attribution.

| Outcome | Error shape |
| --- | --- |
| Zero-row `SELECT` (`ExecuteQuerySingle`) or an `UPDATE`/`DELETE` that matched no rows when exactly one was expected (`ExecuteUpdateOne`) | `fmt.Errorf("%s: %w", op, database.ErrNoRows)` — `ErrNoRows` wraps `sql.ErrNoRows`, so both `errors.Is(err, database.ErrNoRows)` and `database.IsNotFound(err)` match |
| Build/exec/scan/iterate/close/rows-affected infrastructure failure | `*database.ExecError{Op, Stage, Err}` — `Stage` (type `database.ExecStage`) is one of `StageBuild`, `StageExec`, `StageScan`, `StageIterate`, `StageClose`, `StageRowsAffected`; `errors.As(err, &execErr)` and `errors.Unwrap` reach the underlying driver error. `StageClose` is specific to `ExecuteQuerySingle`: after a successful scan it closes the rows explicitly (mirroring `sql.Row.Scan`) so a driver error surfacing only at `Close` — a truncated result, a connection fault mid-statement — is reported instead of swallowed; `Close` is idempotent, so the helper's deferred `Close` remains a safe early-return net. `StageRowsAffected` covers two distinct failures: the driver's `RowsAffected()` call itself erroring (any helper that inspects it), or — `ExecuteUpdateOne` only — `RowsAffected()` succeeding with a count greater than one, rejected instead of silently reported as success |

Builder input runs unmodified:

```go
q := qb.Select(cols.Cols("ID", "Name")).From("users").Where(f.Eq(cols.Col("ID"), id))
err := database.ExecuteQuerySingle(ctx, tx, q, "user_lookup", &row.ID, &row.Name)
```

A `types.Filter`/`types.JoinFilter` WHERE fragment (e.g. `f.Eq(...)` on its own) also satisfies `SQLProvider` structurally — both embed `squirrel.Sqlizer`, which declares the same-shaped `ToSql()` alongside `ToSQL()` — but it is not a complete statement. Passing one directly to any `Execute*` helper is rejected at `StageBuild` with a clear error, instead of shipping the bare fragment to the driver as if it were a full statement.

**`ExecuteUpdate` vs `ExecuteUpdateOne`:** `ExecuteUpdate` returns the raw affected-row count and does not interpret it at all — any count, including zero, is `(n, nil)` — use it whenever a non-singular match is legitimate (a bulk statement, or an idempotent state transition like `UPDATE sessions SET expired = true WHERE expires_at < now()`, where "matched nothing" isn't an error). `ExecuteUpdateOne` wraps it and enforces an exactly-one-row contract: zero rows affected maps to the same op-labeled `ErrNoRows` as `ExecuteQuerySingle`; **more than one** row affected is rejected as `*ExecError` at `StageRowsAffected` instead of silently reported as success — a broader-than-intended `WHERE` predicate that updates several rows is a data-integrity failure, not a "found it" outcome. Use it for the common case of an UPDATE/DELETE expected to match exactly one row (e.g. `UPDATE users SET active = true WHERE id = $1`). Both policies ship explicitly because only the caller can tell "absent" from "already in the target state" — for example, `UPDATE payments SET status='captured' WHERE id=$1 AND status='authorized'` returning zero rows could mean either a missing payment (404) or an idempotent replay (already captured, not an error); `ExecuteUpdate` leaves that call to the caller instead of guessing:

```go
expire := qb.Update("sessions").
    Set(cols.Col("Expired"), true).
    Where(f.Lt(cols.Col("ExpiresAt"), time.Now()))
n, err := database.ExecuteUpdate(ctx, tx, expire, "expire_sessions") // 0 rows is not an error
if err != nil {
    return fmt.Errorf("expire sessions: %w", err)
}

activate := qb.Update("users").
    Set(cols.Col("Active"), true).
    Where(f.Eq(cols.Col("ID"), id))
err = database.ExecuteUpdateOne(ctx, tx, activate, "activate_user") // 0 rows -> ErrNoRows
if errors.Is(err, database.ErrNoRows) {
    return domain.ErrUserNotFound
}
```

`database.Raw(sql string, args ...any)` adapts hand-written SQL to the same helpers — it is an escape hatch on par with `Filter.Raw`/`JoinFilter.Raw`, and broader (the SQL string replaces the whole statement, bypassing the builder's identifier validation entirely). **Every** call site requires the same review annotation `f.Raw()`/`jf.Raw()` do:

```go
// SECURITY: Manual SQL review completed - static SQL, no user input concatenated, values parameterized via args
q := database.Raw("SELECT id, name FROM users WHERE tier = $1 FOR UPDATE", tier)
err := database.ExecuteQuerySingle(ctx, tx, q, "tier_lookup", &row.ID, &row.Name)
```

Typical app-side mapping distinguishes the "not found" business outcome from an infrastructure failure:

```go
err := database.ExecuteQuerySingle(ctx, tx, q, "ownership", &row.ID, &row.Name)
switch {
case errors.Is(err, database.ErrNoRows):
    return domain.ErrResourceNotFound // business 404
case err != nil:
    return fmt.Errorf("load ownership: %w", err) // infra 500
}
```

## Session Timezone (Breaking Change — ADR-016)

| Setting | Default | Purpose |
| --- | --- | --- |
| `database.timezone` | `UTC` | IANA timezone applied per session (PostgreSQL via pgx `RuntimeParams`, Oracle via `ALTER SESSION SET TIME_ZONE` on every new physical connection) |

**Behavior:**

- Unset / empty → defaulted to `UTC` at config validation
- IANA name (`Asia/Tokyo`, `America/New_York`) → validated via `time.LoadLocation`, applied per-connection
- `-` sentinel → opt-out; sessions inherit the database server's default (legacy behavior)
- The literal `Local` → rejected by validation ([ADR-093](adr_093_reject_literal_local_timezone.md)). Here `-` is NOT host-local — it is the server-default opt-out above — so a session that must run in the application host's zone names that zone as an explicit IANA name
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
