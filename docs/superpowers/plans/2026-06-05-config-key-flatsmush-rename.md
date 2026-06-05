# Config Key Flat-Smush Rename — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the 21 snake_case koanf config keys to the framework's flat-smushed convention so they become settable via environment variables (the `_`→`.` env transform mis-nests underscored keys), with a permanent invariant test preventing the bug class from recurring.

**Architecture:** Tag-only rename in `config/types.go` (Go field names unchanged) plus the one underscored default key and all string/doc references to those keys. No env-loading code change — once keys are underscore-free, the existing transform maps them natively. Clean break documented in ADR-024 + `wiki/migrations.md`.

**Tech Stack:** Go 1.26, koanf v2 (`koanf` struct tag), go-viper/mapstructure, testify, reflection.

**Delivered as two self-contained PRs:**
- **PR1** (this plan, Tasks 1–10): the rename + ADR + migration + tests + doc updates for the renamed keys.
- **PR2** (Task 11, separate branch off `main` after PR1 merges): unrelated ride-along doc-drift fixes.

---

## The 21-key rename table (token-level)

Each token is replaced in all five tag namespaces (`koanf`/`json`/`yaml`/`toml`/`mapstructure`) by replacing the **quoted** form `"old"` → `"new"` (the quoted token appears only inside tags). Some tokens occur in two parents (e.g. `idle_ttl`, `table_name`); a global replace covers both.

| # | old token | new token | parent(s) |
|---|---|---|---|
| 1 | `max_size` | `maxsize` | cache.manager |
| 2 | `idle_ttl` | `idlettl` | cache.manager, messaging.publisher |
| 3 | `cleanup_interval` | `cleanupinterval` | cache.manager |
| 4 | `sensitive_fields` | `sensitivefields` | log |
| 5 | `reinit_delay` | `reinitdelay` | messaging.reconnect |
| 6 | `resend_delay` | `resenddelay` | messaging.reconnect |
| 7 | `connection_timeout` | `connectiontimeout` | messaging.reconnect |
| 8 | `max_delay` | `maxdelay` | messaging.reconnect |
| 9 | `max_cached` | `maxcached` | messaging.publisher |
| 10 | `table_name` | `tablename` | outbox, inbox |
| 11 | `auto_create_table` | `autocreatetable` | outbox, inbox |
| 12 | `default_exchange` | `defaultexchange` | outbox |
| 13 | `poll_interval` | `pollinterval` | outbox |
| 14 | `batch_size` | `batchsize` | outbox |
| 15 | `max_retries` | `maxretries` | outbox |
| 16 | `retention_period` | `retentionperiod` | outbox, inbox |
| 17 | `secret_min_length` | `secretminlength` | keystore |

**Do NOT rename (false-positive tokens):** `messaging/manager.go` `idle_ttl_seconds` (metric/log field); `database/*` SQL column `table_name`; any non-config use. The `"quoted"` replace in `types.go` is naturally scoped to tags; for other files, edit only verified config-key references.

---

### Task 1: No-underscore invariant test (the cornerstone guard)

**Files:**
- Create: `config/keyinvariant_test.go`

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestConfigKoanfTagsHaveNoUnderscore guards the whole "env var cannot reach an
// underscored koanf key" bug class: the env loader maps "_"->"." (koanf's nesting
// delimiter), so any koanf leaf tag containing "_" is unreachable from the
// environment. Every koanf tag in the Config tree MUST be underscore-free.
func TestConfigKoanfTagsHaveNoUnderscore(t *testing.T) {
	var offenders []string
	seen := map[reflect.Type]bool{}

	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() == reflect.Map || rt.Kind() == reflect.Slice {
			el := rt.Elem()
			for el.Kind() == reflect.Pointer {
				el = el.Elem()
			}
			if el.Kind() == reflect.Struct {
				walk(el, prefix)
			}
			return
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("koanf")
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			if strings.Contains(name, "_") {
				offenders = append(offenders, key)
			}
			switch f.Type.Kind() {
			case reflect.Struct, reflect.Pointer, reflect.Map, reflect.Slice:
				walk(f.Type, key)
			}
		}
	}

	walk(reflect.TypeOf(Config{}), "")
	if len(offenders) > 0 {
		t.Fatalf("koanf tags must be underscore-free (env vars nest on '_'); offenders:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd config && go test -run TestConfigKoanfTagsHaveNoUnderscore -v .`
Expected: FAIL listing 21 offenders (`cache.manager.max_size`, …, `keystore.secret_min_length`).

- [ ] **Step 3: Commit the failing guard (red)**

```bash
git add config/keyinvariant_test.go
git commit -m "test(config): add no-underscore koanf-tag invariant (red)"
```

---

### Task 2: Env-reachability tests for renamed keys (red)

**Files:**
- Modify: `config/config_test.go` (append test)

- [ ] **Step 1: Write the failing test** (uses the existing `clearEnvironmentVariables()` helper in `config_test.go` for env isolation)

```go
func TestEnvOverrideReachesRenamedKeys(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("OUTBOX_BATCHSIZE", "250")
	t.Setenv("OUTBOX_AUTOCREATETABLE", "true")
	t.Setenv("MESSAGING_RECONNECT_CONNECTIONTIMEOUT", "45s")
	t.Setenv("KEYSTORE_SECRETMINLENGTH", "64")
	t.Setenv("LOG_SENSITIVEFIELDS", "pan, cvv2 ,otp")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 250, cfg.Outbox.BatchSize)
	assert.True(t, cfg.Outbox.AutoCreateTable)
	assert.Equal(t, 45*time.Second, cfg.Messaging.Reconnect.ConnectionTimeout)
	assert.Equal(t, 64, cfg.KeyStore.SecretMinLength)
	assert.Equal(t, []string{"pan", "cvv2", "otp"}, cfg.Log.SensitiveFields)
}
```

(If `config_test.go` lacks `require`/`assert`/`time` imports, add them.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd config && go test -run TestEnvOverrideReachesRenamedKeys -v .`
Expected: FAIL — fields hold defaults/zero (env landed at orphan smushed keys that no tag matches yet).

- [ ] **Step 3: Commit (red)**

```bash
git add config/config_test.go
git commit -m "test(config): assert env overrides reach renamed keys (red)"
```

---

### Task 3: Rename the 21 koanf tags + the one default key (green)

**Files:**
- Modify: `config/types.go`
- Modify: `config/config.go` (loadDefaults: `keystore.secret_min_length` → `keystore.secretminlength`)

- [ ] **Step 1: Rename all 21 tags in `types.go`** (quoted-token replace covers all five tag namespaces; run from repo root)

```bash
cd config
for pair in \
  max_size:maxsize idle_ttl:idlettl cleanup_interval:cleanupinterval \
  sensitive_fields:sensitivefields reinit_delay:reinitdelay resend_delay:resenddelay \
  connection_timeout:connectiontimeout max_delay:maxdelay max_cached:maxcached \
  table_name:tablename auto_create_table:autocreatetable default_exchange:defaultexchange \
  poll_interval:pollinterval batch_size:batchsize max_retries:maxretries \
  retention_period:retentionperiod secret_min_length:secretminlength; do
  old="${pair%%:*}"; new="${pair##*:}"
  perl -pi -e "s/\"\Q$old\E\"/\"$new\"/g" types.go
done
cd ..
```

- [ ] **Step 2: Update the underscored default key in `config.go`**

Replace `"keystore.secret_min_length": 32,` with `"keystore.secretminlength": 32,` (loadDefaults map).

- [ ] **Step 3: Run the cornerstone + env tests to verify they pass (green)**

Run: `cd config && go test -run 'TestConfigKoanfTagsHaveNoUnderscore|TestEnvOverrideReachesRenamedKeys' -v .`
Expected: both PASS.

- [ ] **Step 4: Run the full config package (expect some existing tests to now fail)**

Run: `cd config && go test ./... 2>&1 | tail -30`
Expected: invariant + env tests PASS; some pre-existing tests that hardcode old keys FAIL (fixed in Task 4).

- [ ] **Step 5: Commit (green for the new tests)**

```bash
git add config/types.go config/config.go
git commit -m "fix(config): rename underscored koanf keys to flat-smushed convention

The env loader maps '_'->'.' (koanf nesting), so underscored leaf keys like
log.sensitive_fields / keystore.secret_min_length were unreachable from the
environment. Rename all 21 to the framework's flat-smushed convention
(connectionstring/poolsize/minretrybackoff style). Go field names unchanged.

BREAKING: snake_case YAML/env keys no longer recognized. See ADR-024."
```

---

### Task 4: Fix pre-existing config tests that hardcode old keys

**Files:**
- Modify: `config/config_test.go`, `config/validation_test.go` (and any other failing `config/*_test.go`)

- [ ] **Step 1: Identify failures**

Run: `cd config && go test ./... 2>&1 | grep -E 'FAIL|--- FAIL|secret_min_length|sensitive_fields|table_name|batch_size|_test.go' | head -40`

- [ ] **Step 2: Update each failing test** — replace old koanf keys/env vars with the new smushed forms (e.g. YAML `secret_min_length:` → `secretminlength:`, env `KEYSTORE_SECRET_MIN_LENGTH` → `KEYSTORE_SECRETMINLENGTH`, `k.Get("outbox.batch_size")` → `k.Get("outbox.batchsize")`). Use the token table above.

- [ ] **Step 3: Run config tests to verify green**

Run: `cd config && go test ./... 2>&1 | tail -10`
Expected: ok.

- [ ] **Step 4: Commit**

```bash
git add config/*_test.go
git commit -m "test(config): update tests to renamed flat-smushed keys"
```

---

### Task 5: Update consistency strings (error messages + comments) and their tests

**Files:**
- Modify: `config/validation.go` (8 `NewValidationError("…reinit_delay"/…)` dotted key paths → smushed)
- Modify: `outbox/config.go`, `inbox/config.go`, `cache/manager.go` (error-message strings naming the keys)
- Modify: `keystore/keystore.go` (doc comment `secret_min_length: 32` → `secretminlength: 32`)
- Modify: `outbox/config_test.go`, `inbox/config_test.go`, `cache/manager_test.go` (assertions on those error strings, if any)

- [ ] **Step 1: Update `config/validation.go`** — replace the dotted key paths in `NewValidationError(...)` calls (lines ~596–657): `messaging.reconnect.reinit_delay`→`…reinitdelay`, `…resend_delay`→`…resenddelay`, `…connection_timeout`→`…connectiontimeout`, `…max_delay`→`…maxdelay`, `messaging.publisher.max_cached`→`…maxcached`, `messaging.publisher.idle_ttl`→`…idlettl`, `cache.manager.max_size`→`…maxsize`, `cache.manager.idle_ttl`→`…idlettl` (and `cleanup_interval` if present).

- [ ] **Step 2: Update module error strings** — `outbox/config.go` (`poll_interval`/`batch_size`/`max_retries`/`retention_period` in `fmt.Errorf`), `inbox/config.go` (`retention_period`), `cache/manager.go` (`max_size`/`idle_ttl`) — change the human-readable token to the smushed key so messages match what users set.

- [ ] **Step 3: Update `keystore/keystore.go` doc comment.**

- [ ] **Step 4: Update any test asserting those exact strings** (grep first):

Run: `grep -rnE 'poll_interval|batch_size|max_retries|retention_period|max_size|idle_ttl|reinit_delay|resend_delay|connection_timeout|max_delay|max_cached' outbox inbox cache config --include='*_test.go'`

- [ ] **Step 5: Build + test affected packages**

Run: `go test ./config/... ./outbox/... ./inbox/... ./cache/... 2>&1 | tail -15`
Expected: ok.

- [ ] **Step 6: Commit**

```bash
git add config/validation.go outbox/config.go inbox/config.go cache/manager.go keystore/keystore.go outbox/config_test.go inbox/config_test.go cache/manager_test.go
git commit -m "fix(config): align error messages and comments with renamed keys"
```

---

### Task 6: Update `config.example.yaml`

**Files:**
- Modify: `config.example.yaml`

- [ ] **Step 1: Rename the keys** under `outbox:`, `inbox:`, and `log:` (lines ~112–139): `table_name`→`tablename`, `auto_create_table`→`autocreatetable`, `default_exchange`→`defaultexchange`, `poll_interval`→`pollinterval`, `batch_size`→`batchsize`, `max_retries`→`maxretries`, `retention_period`→`retentionperiod`, `sensitive_fields`→`sensitivefields`. (Leave the explanatory comment mentioning `password, api_key` — those are example sensitive field *values*, not config keys.)

- [ ] **Step 2: Verify YAML still loads** (sanity)

Run: `cd config && go test -run TestLoad -v . 2>&1 | tail -5` (existing load tests exercise example-style config)
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add config.example.yaml
git commit -m "docs(config): rename keys in config.example.yaml"
```

---

### Task 7: Update documentation references to the renamed keys

**Files:**
- Modify: `README.md`, `llms.txt`, `CLAUDE.md`, and the relevant `wiki/*.md` (`outbox.md`, `keystore.md`, `observability.md`, `cache.md`, `messaging.md`, others as found)

- [ ] **Step 1: Enumerate exact references** (read-only) — distinguish our config keys from unrelated tokens (e.g. httpclient's own `connection_timeout`, DB columns). For each candidate file, confirm the match is one of the 21 config keys before editing:

Run: `grep -rnE '\b(max_size|idle_ttl|cleanup_interval|sensitive_fields|reinit_delay|resend_delay|connection_timeout|max_delay|max_cached|table_name|auto_create_table|default_exchange|poll_interval|batch_size|max_retries|retention_period|secret_min_length)\b' README.md llms.txt CLAUDE.md wiki --include='*.md' --include='llms.txt'`

- [ ] **Step 2: Update only verified config-key references** to the smushed form. Watch for `wiki/outbox.md` documenting `auto_create_table` default as `true` — fix to `autocreatetable` AND correct the default to `false` (real default). Skip unrelated matches (e.g. `wiki/httpclient.md` connection-timeout prose about the HTTP client, `wiki/adr_019` audit-event field names) — verify each.

- [ ] **Step 3: Sanity grep — no stray config-key underscores remain in docs**

Run: `grep -rnE 'secret_min_length|sensitive_fields|auto_create_table|batch_size|poll_interval|retention_period' README.md llms.txt CLAUDE.md wiki --include='*.md' | grep -iv 'httpclient\|adr_019\|column'`
Expected: no config-key hits (or only intentionally-excluded ones).

- [ ] **Step 4: Commit**

```bash
git add README.md llms.txt CLAUDE.md wiki
git commit -m "docs: update config key references to flat-smushed names"
```

---

### Task 8: ADR-024 + migration table

**Files:**
- Create: `wiki/adr_024_config_key_flatsmush.md`
- Modify: `wiki/architecture_decisions.md` (add ADR-024 to the index)
- Modify: `wiki/migrations.md` (add a before→after rename table)

- [ ] **Step 1: Write ADR-024** — context (env `_`→`.` mis-nests underscored keys), decision (rename 21 keys to flat-smush, the framework's established convention; no mapping layer; no single-field wrapper structs per the audit), consequences (breaking; migration table; no-underscore invariant test guard). Match the format of `wiki/adr_023_scheduler_timezone.md`.

- [ ] **Step 2: Add the ADR to `wiki/architecture_decisions.md` index.**

- [ ] **Step 3: Add a migration table to `wiki/migrations.md`** with all 21 old→new keys (and the corresponding env vars) from the table above.

- [ ] **Step 4: Commit**

```bash
git add wiki/adr_024_config_key_flatsmush.md wiki/architecture_decisions.md wiki/migrations.md
git commit -m "docs(adr): ADR-024 flat-smush config key rename + migration table"
```

---

### Task 9: Pre-push verification (make check + reviews)

- [ ] **Step 1: Full check**

Run: `make check`
Expected: fmt + lint + tests pass, including `TestConfigKoanfTagsHaveNoUnderscore` and `TestEnvOverrideReachesRenamedKeys`.

- [ ] **Step 2: Run `/code-review`** on the staged diff; apply findings (refactor, never nolint per repo precedent).

- [ ] **Step 3: Run `/security-audit`** on the diff; apply findings.

- [ ] **Step 4: Re-run `make check`** after any review fixes.

---

### Task 10: Push + open PR1 + babysit

- [ ] **Step 1: Push the branch.**

```bash
git push -u origin feature/config-key-flatsmush-rename
```

- [ ] **Step 2: Open the PR** (`gh pr create`) — title `fix(config): rename underscored config keys to flat-smushed convention`; body: problem, the 21-key table, audit rationale (flat-smush is the standard; single-field structs are drift), BREAKING + ADR-024 link, "Closes #<finding issue if one exists>", and a note that Class 2 (InjectInto parity) is a deferred follow-up.

- [ ] **Step 3: Babysit** — wait for CI; address CodeRabbit comments and SonarCloud NEW issues (fetch via `curl "https://sonarcloud.io/api/issues/search?componentKeys=gaborage_go-bricks&pullRequest=<N>&statuses=OPEN,CONFIRMED"`). Fix or document each skip in the commit. Push fixes. Re-check until quality gate green + reviews resolved.

- [ ] **Step 4: Merge** when merge-ready (squash). Delete the remote branch; sync local `main`.

---

### Task 11 (PR2): Ride-along doc-drift fixes — separate branch off `main`

**Precondition:** PR1 merged; `git checkout main && git pull`.

**Files:** `README.md`, `.env.example`

- [ ] **Step 1: Branch** — `git checkout -b fix/config-docs-env-drift`.
- [ ] **Step 2: README server-path env vars** — `README.md:329-339`: `SERVER_BASE_PATH`→`SERVER_PATH_BASE`, `SERVER_HEALTH_ROUTE`→`SERVER_PATH_HEALTH`, `SERVER_READY_ROUTE`→`SERVER_PATH_READY`; fix the YAML block shape (`server.base.path`→`server.path.base`, etc.); `:226` soften the "auto-maps to dot notation" claim.
- [ ] **Step 3: `.env.example`** — `DATABASE_TYPE=postgres`→`postgresql`. Verify the three `MULTITENANT_*` entries (`MULTITENANT_CACHE_TTL`, `MULTITENANT_LIMITS_CLEANUP_INTERVAL`, `MULTITENANT_VALIDATION_PATTERN`) against `MultitenantConfig`/`LimitsConfig` in `config/types.go`; for each with no matching field, remove it (or correct to the real key if one exists).
- [ ] **Step 4:** `make check` (docs-only, but confirm nothing references removed examples in tests).
- [ ] **Step 5:** Commit, push, open PR2 (`fix(docs): correct server-path env vars and .env.example orphans`), babysit to merge as in Task 10.

---

## Self-Review

- **Spec coverage:** rename (Tasks 1–6) ✓; consistency strings (Task 5) ✓; example yaml (Task 6) ✓; docs (Task 7) ✓; ADR + migration (Task 8) ✓; no-underscore invariant test (Task 1) ✓; env-reachability tests (Task 2) ✓; ride-along doc fixes (Task 11) ✓; Class 2 deferred (noted in PR1 body, Task 10). Follow-up Class 2 issue: file separately (best-effort; needs go-ahead per repo issue-permission note) — tracked outside this plan.
- **Placeholder scan:** none — every step has exact commands/keys.
- **Type consistency:** token table is the single source; all tasks reference it. Go field names unchanged throughout (only tags/keys/strings change).
