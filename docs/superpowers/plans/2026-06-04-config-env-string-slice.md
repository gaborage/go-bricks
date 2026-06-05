# Comma-Separated Env `[]string` Config Fields — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a comma-separated env var populate a multi-element `[]string` config field across both config paths (koanf `Unmarshal` and `InjectInto`), and fail startup loudly when a non-empty security CIDR list has zero valid entries.

**Architecture:** A Type-scoped `string → []string` decode hook is added to `config.Load()`'s decoder (isolated first by a behavior-preserving prefactor). A single shared `splitAndTrimList` helper backs both the decode hook and `InjectInto`'s new `[]string` converter so semantics can't drift. `validateScheduler` gains a zero-valid-CIDR gate.

**Tech Stack:** Go 1.26, koanf v2.3.5, go-viper/mapstructure v2.5.0, testify.

**Spec:** `docs/superpowers/specs/2026-06-04-config-env-string-slice-design.md`

---

## File Structure

- `config/config.go` — extract `buildDecoderConfig()`, add `stringToTrimmedSliceHookFunc`, route `Load()` through `UnmarshalWithConf`.
- `config/converters.go` — add shared `splitAndTrimList()` + `toStringSlice()`.
- `config/validation.go` — add `validateCIDRList()`, call from `validateScheduler`.
- `config/injection.go` — add `reflect.Slice` arm + `assignStringSliceField()`; update doc comment.
- `config/config_test.go`, `config/converters_test.go`, `config/validation_test.go`, `config/injection_test.go` — tests.
- `CLAUDE.md`, `llms.txt` — doc updates for `InjectInto` supported types.

---

## Task 1: Prefactor — isolate the decoder config (behavior-preserving)

**Files:**
- Modify: `config/config.go` (imports; `Load()` unmarshal call; new `buildDecoderConfig`)

- [ ] **Step 1: Add imports**

In `config/config.go` import block add `reflect` and the mapstructure alias:

```go
import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	envprovider "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/gaborage/go-bricks/logger"
)
```

- [ ] **Step 2: Add `buildDecoderConfig` (exact replica of koanf default)**

Add near the bottom of `config/config.go`:

```go
// buildDecoderConfig replicates koanf's default Unmarshal decoder
// (knadh/koanf/v2 koanf.go:265-272) so we control the DecodeHook chain.
// koanf fills in Result and TagName at unmarshal time.
func buildDecoderConfig() *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
	}
}
```

- [ ] **Step 3: Route `Load()` through `UnmarshalWithConf`**

Replace the unmarshal call in `Load()`:

```go
	// Unmarshal into config struct
	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		DecoderConfig: buildDecoderConfig(),
	}); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
```

- [ ] **Step 4: Verify the whole config suite still passes (no behavior change)**

Run: `go test ./config/... -race`
Expected: PASS (the prefactor must not change any behavior).

- [ ] **Step 5: Commit**

```bash
git add config/config.go
git commit -m "refactor(config): extract buildDecoderConfig to control unmarshal decode hooks"
```

---

## Task 2: The fix — comma-split-and-trim decode hook (Unmarshal path)

**Files:**
- Create helper: `config/converters.go` (`splitAndTrimList`)
- Modify: `config/config.go` (`stringToTrimmedSliceHookFunc`, add to chain)
- Test: `config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `config/config_test.go`:

```go
func TestLoadMultiElementStringSliceEnv(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", "10.0.0.0/8,192.168.0.0/16")
	t.Setenv("SCHEDULER_SECURITY_TRUSTEDPROXIES", "10.0.0.0/8, 172.16.0.0/12")
	t.Setenv("DEBUG_ALLOWEDIPS", "127.0.0.1,::1")
	t.Setenv("LOG_SENSITIVE_FIELDS", "pan, cvv2 , otp")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16"}, cfg.Scheduler.Security.CIDRAllowlist)
	assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12"}, cfg.Scheduler.Security.TrustedProxies)
	assert.Equal(t, []string{"127.0.0.1", "::1"}, cfg.Debug.AllowedIPs)
	assert.Equal(t, []string{"pan", "cvv2", "otp"}, cfg.Log.SensitiveFields)
}

func TestLoadSingleElementStringSliceEnv(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", "10.0.0.0/8")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.0/8"}, cfg.Scheduler.Security.CIDRAllowlist)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestLoadMultiElementStringSliceEnv -v`
Expected: FAIL — `cfg.Scheduler.Security.CIDRAllowlist` is `["10.0.0.0/8,192.168.0.0/16"]` (len 1).

- [ ] **Step 3: Add the shared split helper**

Add to `config/converters.go` (package already imports `strings`):

```go
// splitAndTrimList splits raw on sep, trims each element, and drops empties.
// Single source of truth for env/default string -> []string semantics, shared by
// the config Unmarshal decode hook and InjectInto.
func splitAndTrimList(raw, sep string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Add the decode hook and wire it into the chain**

In `config/config.go`, add the hook:

```go
// stringToTrimmedSliceHookFunc splits a scalar string into []string on sep,
// trimming each element and dropping empties. Scoped to string -> []string only
// (Type-level), so []byte, other slices, and YAML sequences are untouched.
func stringToTrimmedSliceHookFunc(sep string) mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String || t != reflect.TypeOf([]string(nil)) {
			return data, nil
		}
		return splitAndTrimList(data.(string), sep), nil
	}
}
```

And prepend it in `buildDecoderConfig`'s compose chain:

```go
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			stringToTrimmedSliceHookFunc(","),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./config/ -run 'TestLoadMultiElementStringSliceEnv|TestLoadSingleElementStringSliceEnv' -v`
Expected: PASS.

- [ ] **Step 6: Verify the YAML-sequence path still works (regression)**

Run: `go test ./config/... -race`
Expected: PASS — YAML sequences arrive as slices and skip the hook.

- [ ] **Step 7: Commit**

```bash
git add config/config.go config/converters.go config/config_test.go
git commit -m "fix(config): split comma-separated env vars into []string fields (#539)"
```

---

## Task 3: Fail-fast on all-invalid security CIDR lists

**Files:**
- Modify: `config/validation.go` (`net` import; `validateCIDRList`; call sites in `validateScheduler`)
- Test: `config/validation_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `config/validation_test.go`:

```go
func TestValidateSchedulerCIDRListFailsWhenAllInvalid(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", "not-a-cidr,also-bad")
	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "scheduler.security.cidrallowlist")
}

func TestValidateSchedulerTrustedProxiesFailsWhenAllInvalid(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_TRUSTEDPROXIES", "garbage")
	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "scheduler.security.trustedproxies")
}

func TestValidateSchedulerCIDRListPassesCases(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "valid_multi", value: "10.0.0.0/8,192.168.0.0/16"},
		{name: "partial_invalid_keeps_valid", value: "10.0.0.0/8,bad"},
		{name: "single_valid", value: "10.0.0.0/8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironmentVariables()
			t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", tt.value)
			cfg, err := Load()
			require.NoError(t, err)
			require.NotNil(t, cfg)
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run TestValidateSchedulerCIDR -v` and `-run TestValidateSchedulerTrustedProxies`
Expected: the all-invalid tests FAIL (today `Load()` succeeds and the middleware degrades to localhost-only).

- [ ] **Step 3: Add `net` import**

In `config/validation.go` import block add `"net"`:

```go
import (
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/logger"
)
```

- [ ] **Step 4: Add `validateCIDRList` and call it from `validateScheduler`**

```go
func validateScheduler(cfg *SchedulerConfig) error {
	normalized, err := normalizeIANATimezone("scheduler.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	if err != nil {
		return err
	}
	if err := validateCIDRList("scheduler.security.cidrallowlist", cfg.Security.CIDRAllowlist); err != nil {
		return err
	}
	return validateCIDRList("scheduler.security.trustedproxies", cfg.Security.TrustedProxies)
}

// validateCIDRList fails when a non-empty list contains zero parseable CIDRs.
// Empty lists are valid (localhost-only / no-trusted-proxy defaults). Partial-invalid
// lists pass here and keep the existing middleware-time WARN.
func validateCIDRList(field string, list []string) error {
	if len(list) == 0 {
		return nil
	}
	var invalid []string
	valid := 0
	for _, entry := range list {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(entry)); err != nil {
			invalid = append(invalid, entry)
			continue
		}
		valid++
	}
	if valid == 0 {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field,
			Message:  fmt.Sprintf("no valid CIDR entries (all %d rejected: %v)", len(list), invalid),
			Action:   "use CIDR notation, e.g. 10.0.0.0/8; comma-separate multiple values in one env var",
		}
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./config/ -run TestValidateScheduler -v`
Expected: PASS (all-invalid → error; valid/partial/single → ok).

- [ ] **Step 6: Commit**

```bash
git add config/validation.go config/validation_test.go
git commit -m "feat(config): fail startup when scheduler CIDR list has zero valid entries (#539)"
```

---

## Task 4: `InjectInto` `[]string` support

**Files:**
- Modify: `config/converters.go` (`toStringSlice`)
- Modify: `config/injection.go` (`reflect.Slice` arm; `assignStringSliceField`; doc comment)
- Test: `config/injection_test.go`, `config/converters_test.go`

- [ ] **Step 1: Write the failing tests**

Add a struct + tests to `config/injection_test.go`:

```go
type sliceInjectConfig struct {
	Hosts    []string `config:"svc.hosts"`
	Defaults []string `config:"svc.defaults" default:"a,b,c"`
	Required []string `config:"svc.required" required:"true"`
}

func TestConfigInjectionStringSliceFromEnv(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SVC_HOSTS", "h1, h2 ,h3")
	t.Setenv("SVC_REQUIRED", "r1")
	cfg, err := Load()
	require.NoError(t, err)

	var sc sliceInjectConfig
	require.NoError(t, cfg.InjectInto(&sc))
	assert.Equal(t, []string{"h1", "h2", "h3"}, sc.Hosts)
	assert.Equal(t, []string{"a", "b", "c"}, sc.Defaults)
	assert.Equal(t, []string{"r1"}, sc.Required)
}

func TestConfigInjectionStringSliceRequiredMissing(t *testing.T) {
	clearEnvironmentVariables()
	cfg, err := Load()
	require.NoError(t, err)

	var sc sliceInjectConfig
	err = cfg.InjectInto(&sc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "svc.required")
}
```

Add to `config/converters_test.go`:

```go
func TestToStringSlice(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    []string
		wantErr bool
	}{
		{name: "string_split", input: "a, b ,c", want: []string{"a", "b", "c"}},
		{name: "string_slice", input: []string{"x", "y"}, want: []string{"x", "y"}},
		{name: "any_slice", input: []any{"x", 2}, want: []string{"x", "2"}},
		{name: "empty_string", input: "", want: []string{}},
		{name: "unsupported", input: 42, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toStringSlice(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'TestConfigInjectionStringSlice|TestToStringSlice' -v`
Expected: FAIL — `toStringSlice` undefined; injection returns "unsupported type".

- [ ] **Step 3: Add `toStringSlice` to `config/converters.go`**

```go
// toStringSlice converts a resolved config value to []string. Reuses
// splitAndTrimList for the string case so InjectInto matches Unmarshal semantics.
func toStringSlice(value any) ([]string, error) {
	switch v := value.(type) {
	case []string:
		return v, nil
	case []any: // YAML sequence
		out := make([]string, len(v))
		for i, el := range v {
			out[i] = fmt.Sprintf("%v", el)
		}
		return out, nil
	case string:
		return splitAndTrimList(v, ","), nil
	default:
		return nil, fmt.Errorf(errMsgUnsupportedType, value)
	}
}
```

- [ ] **Step 4: Add the slice arm + assignment in `config/injection.go`**

In `assignFieldValue`, add a case before `default`:

```go
	case reflect.Slice:
		if field.Type().Elem().Kind() == reflect.String {
			return c.assignStringSliceField(field, configKey, required, value)
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    configKey,
			Message:  fmt.Sprintf("unsupported type %s", field.Type()),
			Action:   "use string, int, int64, float64, bool, time.Duration, or []string",
		}
```

Add the method:

```go
func (c *Config) assignStringSliceField(field reflect.Value, configKey string, required bool, value any) error {
	slice, err := toStringSlice(value)
	if err != nil {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    configKey,
			Message:  fmt.Sprintf("'%v' is not a valid string list", value),
			Action:   "provide a comma-separated string or a YAML sequence",
		}
	}
	if required && len(slice) == 0 {
		envVar := strings.ToUpper(strings.ReplaceAll(configKey, ".", "_"))
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    configKey,
			Message:  errMessageRequired,
			Action:   fmt.Sprintf("set %s env var or add '%s' to config.yaml", envVar, configKey),
		}
	}
	field.Set(reflect.ValueOf(slice))
	return nil
}
```

Update the `InjectInto` doc comment line:

```go
// Supported field types: string, int, int64, float64, bool, time.Duration, []string
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./config/ -run 'TestConfigInjectionStringSlice|TestToStringSlice' -v`
Expected: PASS.

- [ ] **Step 6: Confirm unsupported-type still errors**

Run: `go test ./config/ -run TestConfigInjectionUnsupportedFieldType -v`
Expected: PASS (`chan int` remains unsupported).

- [ ] **Step 7: Commit**

```bash
git add config/converters.go config/injection.go config/injection_test.go config/converters_test.go
git commit -m "feat(config): support []string fields in InjectInto (#539)"
```

---

## Task 5: Documentation

**Files:**
- Modify: `CLAUDE.md` (Configuration Injection → Supported Types)
- Modify: `llms.txt` (if it enumerates `InjectInto` supported types)

- [ ] **Step 1: Update CLAUDE.md**

Change the "Supported Types" line under Configuration Injection to:

```
**Supported Types:** string, int, int64, float64, bool, time.Duration, []string (comma-separated via env, native sequence via YAML).
```

- [ ] **Step 2: Update llms.txt if applicable**

Run: `grep -n "time.Duration" llms.txt`
If a supported-types list is present, add `[]string` to it; otherwise skip.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md llms.txt
git commit -m "docs(config): document []string support for env vars and InjectInto (#539)"
```

---

## Task 6: Full verification + mandated reviews

- [ ] **Step 1: Run the full pre-commit gate**

Run: `make check`
Expected: fmt + lint + race tests all pass.

- [ ] **Step 2: Run mandated reviews on the diff (per CLAUDE.md)**

Run `/code-review` then `/security-audit` on the staged/branch diff; apply findings (code-review first), re-run `make check`.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin feature/config-env-string-slice
```
Open PR titled `config: split comma-separated env vars for []string fields (#539)`, body referencing the issue, the four changes, and the fail-fast behavior note.

---

## Self-Review

- **Spec coverage:** Change 1 → Task 1; Change 2 → Task 2; Change 3 → Task 3; Change 4 → Task 4; docs → Task 5; verification/reviews → Task 6. All spec sections mapped.
- **Placeholder scan:** none — every code step has concrete code.
- **Type consistency:** `splitAndTrimList`, `stringToTrimmedSliceHookFunc`, `buildDecoderConfig`, `validateCIDRList`, `toStringSlice`, `assignStringSliceField` names are used identically across tasks; `errMsgUnsupportedType`, `errMessageRequired`, `errCategoryInvalid`, `errCategoryMissing` reuse existing constants.
