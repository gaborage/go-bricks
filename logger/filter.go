// Package logger provides filtering capabilities for sensitive data in log output.
package logger

import (
	"fmt"
	"reflect"
	"strings"
	"unsafe"
)

const (
	// DefaultMaxDepth is the default maximum recursion depth for filtering
	DefaultMaxDepth = 8
)

// Common sensitive field names used in DefaultFilterConfig.
const (
	sensitiveFieldPassword = "password"
	sensitiveFieldAPIKey   = "api_key"
	sensitiveFieldToken    = "token"
	sensitiveFieldSecret   = "secret"
)

// FilterConfig defines the configuration for sensitive data filtering
type FilterConfig struct {
	// SensitiveFields contains field names that should be masked in logs
	SensitiveFields []string
	// MaskValue is the value used to replace sensitive data (default: "***")
	MaskValue string
	// ErrorRedactor, when non-nil, replaces the message written by
	// LogEvent.Err(err) with its return value. Field-name masking cannot see
	// inside an error message, and the framework calls Err with
	// consumer-authored errors at dozens of sites, so a consumer-side scrub
	// helper never reaches those lines without this seam. Nil (the default,
	// and what DefaultFilterConfig returns) keeps Err byte-identical to
	// zerolog's own rendering. Code door only: app.Options.LoggerFilterConfig,
	// which replaces the whole config — start from DefaultFilterConfig() and
	// set this field. The YAML log.sensitivefields merge path leaves it nil,
	// the value being a function.
	ErrorRedactor func(error) string
}

// DefaultFilterConfig returns a default configuration with common sensitive field names
func DefaultFilterConfig() *FilterConfig {
	return &FilterConfig{
		SensitiveFields: []string{
			sensitiveFieldPassword, "passwd", "pwd",
			// Key material is named needle by needle, in both spellings: matching is
			// case-insensitive SUBSTRING, so "api_key" does not contain "apikey" and
			// neither contains the other. A bare "key" needle used to stand in for
			// all of them and masked every field merely containing the word —
			// "keys", "tenant_key", "cache_key", and the framework's own "key"
			// identifier — with no way to unmask one short of replacing this whole
			// list (#1037). "secret_key" and "secretkey" need no entry of their own;
			// the "secret" needle already covers both. The hyphenated spellings are
			// here because httpclient logs whole http.Header maps through this
			// filter under LogPayloads, and a header is spelled "X-Api-Key", which
			// no underscore needle matches. A bare "-key" would cover every such
			// header and also mask "Idempotency-Key", an identifier consumers send
			// on every payment POST, so the same rule applies as above: name the
			// shape, not the word. A
			// spelling this list does not name — "license_key", "hmac_key",
			// "Ocp-Apim-Subscription-Key" — logs in clear until a consumer adds it
			// via log.sensitivefields.
			sensitiveFieldSecret, sensitiveFieldAPIKey, "apikey", "api-key",
			"private_key", "privatekey", "private-key",
			"signing_key", "signingkey", "signing-key",
			"encryption_key", "encryptionkey", "encryption-key",
			sensitiveFieldToken, "access_token", "refresh_token",
			"auth", "authorization",
			"credential", "credentials",
			"broker_url", "database_url", "db_url",
			// Card data (PCI) and adjacent PII. Matching is case-insensitive
			// substring, so bare "pan"/"card"/"pin"/"track" are deliberately
			// absent — they would mask span_id, discard_reason, pinned_at,
			// tracking_id. Consumers with differently-named PAN fields add them
			// via log.sensitivefields (see wiki/observability.md).
			"cardholder", "card_number", "cardnumber", "primary_account_number",
			"cvv", "cvc", "track1", "track2", "track_data",
			"iban", "otp",
		},
		MaskValue: DefaultMaskValue,
	}
}

// loweredNeedles holds the precomputed lowered needle list behind a pointer so
// SensitiveDataFilter itself stays comparable (a []string field would not be).
type loweredNeedles struct {
	fields      []string
	byFirstByte [256][]string
}

// SensitiveDataFilter filters sensitive data from logs. Filtering is enforced by the
// adapter layer (LogEventAdapter) — never add a Zerolog() accessor to ZeroLogger as
// that would create a bypass path around this filter boundary.
type SensitiveDataFilter struct {
	config *FilterConfig
	// needles is config.SensitiveFields normalized once at construction (see
	// normalizeNeedles: lowercase, trim, drop-empty, de-duplicate). The
	// list is a snapshot: mutating the caller's FilterConfig.SensitiveFields
	// after NewSensitiveDataFilter returns has no effect.
	needles *loweredNeedles
}

// NewSensitiveDataFilter creates a new filter with the given configuration
func NewSensitiveDataFilter(config *FilterConfig) *SensitiveDataFilter {
	if config == nil {
		config = DefaultFilterConfig()
	}
	if config.MaskValue == "" {
		config.MaskValue = DefaultMaskValue
	}
	lowered := normalizeNeedles(config.SensitiveFields)
	needles := &loweredNeedles{fields: lowered}
	for _, n := range lowered {
		needles.byFirstByte[n[0]] = append(needles.byFirstByte[n[0]], n)
	}
	return &SensitiveDataFilter{config: config, needles: needles}
}

// redactError applies the configured ErrorRedactor, reporting whether one ran.
// The decision lives on the filter, next to FilterString and FilterValue, so the
// adapter never reads FilterConfig's fields itself. Nil-receiver safe: a logger
// built without a filter has no redactor.
func (f *SensitiveDataFilter) redactError(err error) (string, bool) {
	if f == nil || f.config.ErrorRedactor == nil {
		return "", false
	}
	return f.config.ErrorRedactor(err), true
}

// normalizeNeedles lowercases and trims a needle list, dropping entries that are
// empty afterwards and de-duplicating the rest. An empty needle is not a
// harmless no-op: strings.Contains reports true against it for every field name,
// so a single one masks the entire log stream. Normalizing here, where a list
// becomes a filter, is what gives the rule to EVERY construction door — including
// app.Options.LoggerFilterConfig, which replaces the whole config and therefore
// reached the matcher un-normalized.
func normalizeNeedles(fields []string) []string {
	normalized := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		n := strings.ToLower(strings.TrimSpace(f))
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		normalized = append(normalized, n)
	}
	return normalized
}

// FilterString filters sensitive data from string values
func (f *SensitiveDataFilter) FilterString(key, value string) string {
	if f.isSensitiveField(key) {
		return f.maskString(value)
	}
	return value
}

// FilterValue filters sensitive data from any values
func (f *SensitiveDataFilter) FilterValue(key string, value any) any {
	visited := make(map[uintptr]struct{})
	return f.filterValueWithProtection(key, value, visited, DefaultMaxDepth)
}

// filterValueWithProtection performs filtering with cycle detection and depth limiting
func (f *SensitiveDataFilter) filterValueWithProtection(key string, value any, visited map[uintptr]struct{}, maxDepth int) any {
	if f.isSensitiveField(key) {
		return f.config.MaskValue
	}

	if value == nil {
		return nil
	}

	// Check depth limit — fail-closed: mask the subtree rather than leaking sensitive
	// leaves the recursion budget didn't reach. Contrast with cycle detection (below)
	// which returns value because masking a cycle root discards the rest of the tree;
	// depth exhaustion can always safely substitute the mask.
	if maxDepth <= 0 {
		return f.config.MaskValue
	}

	return f.filterByTypeWithProtection(key, value, visited, maxDepth)
}

// filterByTypeWithProtection dispatches to appropriate handler with cycle detection
func (f *SensitiveDataFilter) filterByTypeWithProtection(key string, value any, visited map[uintptr]struct{}, maxDepth int) any {
	// Handle typed map first (most common case)
	if m, ok := value.(map[string]any); ok {
		if m == nil {
			// Preserve typed-nil parity with the reflect.Map branch below
			// (filterStringMapWithProtection would otherwise return {} via make).
			return nil
		}
		return f.filterStringMapWithProtection(m, visited, maxDepth)
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return f.filterSliceOrArrayWithProtection(key, rv, visited, maxDepth)
	case reflect.Struct:
		return f.filterStructWithProtection(value, visited, maxDepth)
	case reflect.Pointer:
		if !rv.IsNil() && rv.Type().Elem().Kind() == reflect.Struct {
			return f.filterStructWithProtection(value, visited, maxDepth)
		}
		return value
	case reflect.Map:
		// Covers map[string]string, map[string][]string (http.Header), map[string]int, etc.
		// map[string]any is handled by the fast-path type-assertion above; this arm catches
		// every other concrete map type. Keys are stringified so non-string-keyed maps still
		// get their keys sensitivity-checked. Output is always map[string]any — the log
		// consumer does not require the original value type.
		if rv.IsNil() {
			// A typed nil map (e.g. var h http.Header = nil) must stay nil in the log output.
			// Without this guard, rv.Len()==0 would produce {} instead of null.
			return nil
		}
		result := make(map[string]any, rv.Len())
		for _, k := range rv.MapKeys() {
			var keyStr string
			if k.Kind() == reflect.String {
				keyStr = k.String()
			} else {
				keyStr = fmt.Sprintf("%v", k.Interface())
			}
			result[keyStr] = f.filterValueWithProtection(keyStr, rv.MapIndex(k).Interface(), visited, maxDepth-1)
		}
		return result
	default:
		// All other types pass through unchanged
		return value
	}
}

// filterStringMapWithProtection handles map[string]any filtering with cycle detection
func (f *SensitiveDataFilter) filterStringMapWithProtection(m map[string]any, visited map[uintptr]struct{}, maxDepth int) map[string]any {
	filtered := make(map[string]any, len(m))
	for k, v := range m {
		filtered[k] = f.filterValueWithProtection(k, v, visited, maxDepth-1)
	}
	return filtered
}

// filterSliceOrArrayWithProtection handles slice and array filtering with cycle detection
func (f *SensitiveDataFilter) filterSliceOrArrayWithProtection(key string, rv reflect.Value, visited map[uintptr]struct{}, maxDepth int) any {
	// A typed nil slice stays nil, matching the two map branches above: rebuilding
	// it would emit [] where the log line carried null, which is wire-visible to
	// anything parsing the output. Arrays cannot be nil, hence the Kind test.
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		return rv.Interface()
	}

	// Decide passthrough-vs-copy from the ELEMENT TYPE, before descending. The
	// previous form compared each filtered element with the original to detect
	// changes, which panics the moment an element holds an uncomparable dynamic
	// type — a map or a slice inside an []any, i.e. every JSON list of objects.
	// A slice whose elements the walker cannot rewrite is returned as-is, which
	// is what keeps []string a []string and []byte base64 in the output. Depth
	// is part of the decision: at maxDepth 1 the elements are masked, and a mask
	// is a rewrite whatever the element type says. Decided first: the cycle
	// bookkeeping below never fires for a slice anyway — reflect.ValueOf never
	// returns an addressable Value, so CanAddr is always false here, and slice
	// cycles terminate on depth. Struct cycles are caught by the reachable
	// visited map in filterStructWithProtection.
	if maxDepth > 1 && !rewritesType(rv.Type().Elem()) {
		return rv.Interface()
	}

	// Check if we can get a pointer to track this slice/array for cycles
	if rv.CanAddr() {
		ptr := uintptr(unsafe.Pointer(rv.UnsafeAddr()))
		if _, exists := visited[ptr]; exists {
			return rv.Interface() // Return original if cycle detected
		}
		visited[ptr] = struct{}{}
		defer delete(visited, ptr)
	}

	length := rv.Len()
	filtered := make([]any, length)

	for i := range length {
		filtered[i] = f.filterValueWithProtection(key, rv.Index(i).Interface(), visited, maxDepth-1)
	}

	return filtered
}

// rewritesType reports whether the walker can rewrite a value of type t into a
// different shape. Interface elements count because their concrete type is only
// known per value; pointers count conservatively — rebuilding a slice as []any
// is always safe, it only loses the concrete slice type. Everything else is a
// leaf the walker returns untouched.
func rewritesType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface, reflect.Struct, reflect.Map,
		reflect.Slice, reflect.Array, reflect.Pointer:
		return true
	default:
		return false
	}
}

// FilterFields filters a map of fields for sensitive data
func (f *SensitiveDataFilter) FilterFields(fields map[string]any) map[string]any {
	filtered := make(map[string]any, len(fields))
	for key, value := range fields {
		filtered[key] = f.FilterValue(key, value)
	}
	return filtered
}

// isSensitiveField checks if a field name is considered sensitive
func (f *SensitiveDataFilter) isSensitiveField(fieldName string) bool {
	if f.needles == nil {
		return false
	}
	lower := strings.ToLower(fieldName)
	for i := range len(lower) {
		for _, n := range f.needles.byFirstByte[lower[i]] {
			if strings.HasPrefix(lower[i:], n) {
				return true
			}
		}
	}
	return false
}

// maskString masks sensitive string values
func (f *SensitiveDataFilter) maskString(value string) string {
	if value == "" {
		return value
	}

	// SECURITY: A URL value on the sensitive path is masked in full, never structure-preserved.
	// URL query strings and fragments routinely carry the secret itself (client_secret=, apikey=,
	// token=); partial masking (e.g. only user-info password) would leave those verbatim.
	// For all sensitive strings, completely mask the value — no partial disclosure.
	return f.config.MaskValue
}

// filterStructWithProtection filters sensitive fields with cycle detection and depth limiting
func (f *SensitiveDataFilter) filterStructWithProtection(value any, visited map[uintptr]struct{}, maxDepth int) any {
	if value == nil {
		return nil
	}

	// Check depth limit — fail-closed; see filterValueWithProtection for rationale.
	if maxDepth <= 0 {
		return f.config.MaskValue
	}

	structVal, structType, ptr := f.extractStructValueWithPointer(value)
	if !structVal.IsValid() {
		return value
	}

	// Check for cycles using the pointer
	if ptr != 0 {
		if _, exists := visited[ptr]; exists {
			return value // Return original if cycle detected
		}
		visited[ptr] = struct{}{}
		defer delete(visited, ptr)
	}

	return f.buildFilteredStructMapWithProtection(structVal, structType, visited, maxDepth)
}

// extractStructValueWithPointer handles pointer dereferencing and returns tracking pointer
func (f *SensitiveDataFilter) extractStructValueWithPointer(value any) (reflect.Value, reflect.Type, uintptr) {
	val := reflect.ValueOf(value)
	typ := reflect.TypeOf(value)
	var trackingPtr uintptr

	// Handle pointer types and capture the first non-nil pointer for tracking
	for typ.Kind() == reflect.Pointer {
		if val.IsNil() {
			return reflect.Value{}, nil, 0
		}

		// Capture the pointer value for cycle detection on the first pointer
		if trackingPtr == 0 && val.CanAddr() {
			trackingPtr = val.Pointer()
		} else if trackingPtr == 0 {
			// If we can't get address, use the pointer value directly
			trackingPtr = val.Pointer()
		}

		val = val.Elem()
		typ = typ.Elem()
	}

	// If we have a struct value that can be addressed and we haven't captured a pointer yet
	if trackingPtr == 0 && val.CanAddr() {
		trackingPtr = uintptr(unsafe.Pointer(val.UnsafeAddr()))
	}

	// Validate it's a struct
	if typ.Kind() != reflect.Struct {
		return reflect.Value{}, nil, 0
	}

	return val, typ, trackingPtr
}

// buildFilteredStructMapWithProtection creates a map representation with cycle detection
func (f *SensitiveDataFilter) buildFilteredStructMapWithProtection(structVal reflect.Value, structType reflect.Type, visited map[uintptr]struct{}, maxDepth int) map[string]any {
	// Pre-allocate result map with capacity for all fields to reduce allocations
	result := make(map[string]any, structVal.NumField())

	for i := 0; i < structVal.NumField(); i++ {
		field := structType.Field(i)
		fieldValue := structVal.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Only process fields that can be converted to interface{}
		if !fieldValue.CanInterface() {
			continue
		}

		// Extract field name (empty string means skip)
		fieldName := f.extractFieldName(&field)
		if fieldName == "" {
			continue
		}

		result[fieldName] = f.filterValueWithProtection(fieldName, fieldValue.Interface(), visited, maxDepth-1)
	}

	return result
}

// extractFieldName determines the field name to use, preferring json tags
// Returns empty string to signal the field should be skipped
func (f *SensitiveDataFilter) extractFieldName(field *reflect.StructField) string {
	tag := field.Tag.Get("json")

	// Skip fields marked with json:"-"
	if tag == "-" {
		return ""
	}

	// Use struct field name if no json tag
	if tag == "" {
		return field.Name
	}

	// Handle comma-separated json tags (e.g., "name,omitempty")
	if idx := strings.Index(tag, ","); idx != -1 {
		fieldName := tag[:idx]
		// Use struct field name if tag part is empty (e.g., ",omitempty")
		if fieldName == "" {
			return field.Name
		}
		return fieldName
	}

	return tag
}
