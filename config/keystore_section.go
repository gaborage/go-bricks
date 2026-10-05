package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/gaborage/go-bricks/internal/keyname"
)

// envVarNamePattern is the POSIX environment-variable name grammar. A value
// that fails it is more likely the password itself than a variable name.
var envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// normalizeKeyStore fills the nil default: an unset SecretMinLength becomes
// DefaultKeyStoreSecretMinLength (32). An explicit value is left untouched for
// check to judge. Nothing here can fail.
func normalizeKeyStore(cfg *KeyStoreConfig) {
	if cfg.SecretMinLength == nil {
		cfg.SecretMinLength = new(cfg.SecretFloor())
	}
}

// checkKeyStore judges the floor first, then the entry names, then each entry.
// A set SecretMinLength must be at least DefaultKeyStoreSecretMinLength — the
// floor is mandatory and a set value can only raise it (ADR-095) — and is
// judged before the empty-keys return, so a config no key follows is still
// rejected. nil is left alone since white-box tests call checkKeyStore
// directly, before normalize has filled it.
//
// The names are judged as a set before any entry's sources (ADR-144): each is
// a dotted path of env-reachable segments; no entry is a dotted prefix of
// another; no two differ only in '-' versus '.'; and the generation entries
// form well-named, distinct, non-nesting families. Each entry is then either
// an RSA pair (public required with exactly one source, private optional), a
// symmetric secret or a PKCS#12 bundle — a mixed entry is rejected.
func checkKeyStore(cfg *KeyStoreConfig) error {
	if cfg.SecretMinLength != nil && *cfg.SecretMinLength < DefaultKeyStoreSecretMinLength {
		err := NewValidationError(fieldKeystoreMinLength,
			fmt.Sprintf("must be at least %d: the symmetric-secret length floor is mandatory (ADR-095)", DefaultKeyStoreSecretMinLength))
		err.Action = "remove the key to take the default, or set a value at or above it to raise the floor"
		return err
	}

	if len(cfg.Keys) == 0 {
		return nil
	}

	// Sorted, so every rule reports the same name on every run.
	names := slices.Sorted(maps.Keys(cfg.Keys))
	for _, name := range names {
		if err := checkKeyName(name); err != nil {
			return err
		}
	}
	if err := checkKeyNameSet(names); err != nil {
		return err
	}
	if err := checkKeyFamilies(names); err != nil {
		return err
	}
	for _, name := range names {
		kp := cfg.Keys[name]
		if err := validateKeyEntry(&kp, name); err != nil {
			return err
		}
	}
	return nil
}

// checkKeyName holds one entry name to the dotted grammar: segments of
// [a-z0-9-] joined by '.', so the name is reachable by an environment variable
// (ADR-090, ADR-144), and no entry field name after a '.', which the
// keystore.keys tree reader would take as the entry's field.
func checkKeyName(name string) error {
	if !keyname.ValidName(name) {
		if hasEmptySegment(name) {
			// The path keystore.keys.<name> is malformed, so the parent is reported.
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldKeystoreKeys,
				Message:  fmt.Sprintf("name %q has an empty segment", name),
				Action:   "join non-empty segments with single dots, e.g. tokens.our",
			}
		}
		return unreachableDottedName(fieldKeystoreKeys, name)
	}
	if word, bad := keyname.ReservedAfterDot(name); bad {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldKeystoreKeys,
			Message:  fmt.Sprintf("name %q uses the field name %q after a '.'", name, word),
			Action:   fmt.Sprintf("rename it (e.g. %s, or another segment than %s): nested under a name, %s reads as that entry's field", strings.ReplaceAll(name, keyname.Sep+word, "-"+word), word, word),
		}
	}
	return nil
}

// hasEmptySegment reports whether a dotted name has an empty segment.
func hasEmptySegment(name string) bool {
	return slices.Contains(strings.Split(name, keyname.Sep), "")
}

// unreachableDottedName refuses a dotted name with a segment outside
// [a-z0-9-], under root (keystore.keys or messaging.seal.active). It states
// the ADR-090 reason and, when there is one, the dotted spelling the operator's
// variable already reaches.
func unreachableDottedName(root, name string) *ConfigError {
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    root + "." + name,
		Message:  fmt.Sprintf("name %q is not reachable by an environment variable", name),
		Action: "rename it using lowercase letters, digits and '-' within a segment and nesting between segments: " +
			"an environment variable lowercases and maps '_' to the config path delimiter, so any other name is unaddressable" +
			dottedSuggestion(root, name),
	}
}

// dottedSuggestion names the dotted spelling the environment variable an
// operator most likely meant already reaches: tokens_our is what
// KEYSTORE_KEYS_TOKENS_OUR_* lands on as tokens.our. Empty when that spelling
// would be refused too.
func dottedSuggestion(root, name string) string {
	want := envVarToKey(name)
	if want == name || !keyname.ValidName(want) || !suggestable(root, want) {
		return ""
	}
	return fmt.Sprintf("; write %q, which %s reaches", want, envReach(root, keyToEnvVar(root+"."+want)))
}

// suggestable reports whether a valid dotted name may be offered under root:
// an entry name that is no malformed generation (audit.v1 is refused as one),
// or a selector key that is a family, since a selector never names a
// generation.
func suggestable(root, want string) bool {
	if root == fieldKeystoreKeys {
		_, _, form := keyname.SplitGeneration(want)
		return keyname.ValidEntryName(want) && form != keyname.Malformed
	}
	return keyname.CheckLogical(want) == keyname.LogicalOK
}

// checkKeyNameSet refuses two names one path or one variable cannot tell
// apart. A name that is a dotted prefix of another cannot exist in nested YAML
// or the environment beside it, so a hand-built Config holding both is refused
// too. Two names that differ only in '-' versus '.' are refused because a POSIX
// override of the hyphenated one creates the dotted one instead, and boots
// green with the override silently unused.
//
// Only names without a Generation marker meet the look-alike rule here. A
// marked name never folds equal to an unmarked one, and a marked pair is a
// family look-alike or a malformed name, which checkKeyFamilies refuses with
// its own action: renaming a generation into the other family is a
// drain-then-cutover, never the in-place rename offered here.
func checkKeyNameSet(names []string) error {
	if prefix, name, found := keyname.FirstDottedPrefix(names); found {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(keystoreKeysFieldPrefix, prefix),
			Message:  fmt.Sprintf("entry %q is a dotted prefix of entry %q", prefix, name),
			Action:   fmt.Sprintf("rename one of them (%s → %s.default): nested YAML and the environment cannot hold an entry and a name below it", prefix, prefix),
		}
	}
	if earlier, name, found := keyname.FirstFoldClash(ordinaryNames(names)); found {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(keystoreKeysFieldPrefix, name),
			Message:  fmt.Sprintf("%q and %q differ only in '-' versus '.'", earlier, name),
			Action: fmt.Sprintf("keep one: override %q with %s_* (Docker, Kubernetes), or rename it %q everywhere (YAML, code, tags, partner kid)",
				earlier, keyToEnvVar(fieldKeystoreKeys+"."+earlier), name),
		}
	}
	return nil
}

// ordinaryNames returns the names that carry no Generation marker, in order.
func ordinaryNames(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool {
		_, _, form := keyname.SplitGeneration(name)
		return form != keyname.Ordinary
	})
}

// generationFamilies groups the well-formed generation entries among names by
// family, each with its versions in name order. Ordinary and malformed names
// are skipped; checkKeyFamilies is what refuses the malformed ones.
func generationFamilies(names []string) map[string][]string {
	families := make(map[string][]string)
	for _, name := range names {
		if logical, version, form := keyname.SplitGeneration(name); form == keyname.Generation {
			families[logical] = append(families[logical], version)
		}
	}
	return families
}

// checkKeyFamilies refuses what cannot be a family among the sorted entry
// names: a malformed generation name (with its rename), two families that
// differ only in '-' versus '.', and two families that nest once '-' is read
// as '.' (keyname.FirstFoldedPrefix), which messaging.seal.active could not
// hold a selector for side by side: a POSIX variable for one would land on
// the other's nested path and be dropped in the merge.
func checkKeyFamilies(names []string) error {
	for _, name := range names {
		if logical, version, form := keyname.SplitGeneration(name); form == keyname.Malformed {
			return malformedGenerationError(name, logical, version)
		}
	}
	families := generationFamilies(names)
	sorted := slices.Sorted(maps.Keys(families))
	if earlier, family, found := keyname.FirstFoldClash(sorted); found {
		// Sorted, '-' comes before '.', so the later family is the one a POSIX
		// variable creates when it means the earlier: name the generation it
		// meant and the variable that reaches it.
		meant := keyname.GenerationName(earlier, families[family][0])
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldKeystoreKeys,
			Message: fmt.Sprintf("families %q (%s) and %q (%s) differ only in '-' versus '.': a family rename is not a rotation",
				earlier, keyname.GenerationName(earlier, families[earlier][0]), family, keyname.GenerationName(family, families[family][0])),
			Action: fmt.Sprintf("keep one family: set %s with %s_* (Docker, Kubernetes) rather than a POSIX export; "+
				"moving to %q is a family rename, drained before the cutover, never a second family beside the first",
				meant, keyToEnvVar(fieldKeystoreKeys+"."+meant), family),
		}
	}
	if family, other, found := keyname.FirstFoldedPrefix(sorted); found {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldKeystoreKeys,
			Message: fmt.Sprintf("families %q and %q %s: messaging.seal.active cannot hold a selector for both",
				family, other, nestClause(family, other)),
			Action: "rename one family so neither is a dotted prefix of the other, reading '-' as '.'",
		}
	}
	return nil
}

// nestClause says how prefix nests over name: plainly when it is a dotted
// prefix as written, "when '-' is read as '.'" when only their folds nest, as
// a POSIX variable reads them.
func nestClause(prefix, name string) string {
	if keyname.IsDottedPrefix(prefix, name) {
		return "nest"
	}
	return "nest when '-' is read as '.'"
}

// malformedGenerationError names why a name carrying a Generation marker is no
// Generation, with the rename when the marker is the one its family does not
// take.
func malformedGenerationError(name, logical, version string) *ConfigError {
	err := &ConfigError{Category: errCategoryInvalid, Field: fmt.Sprintf(keystoreKeysFieldPrefix, name)}
	switch fault := keyname.CheckLogical(logical); {
	case fault == keyname.LogicalTooLong:
		err.Message = fmt.Sprintf("family %q is %d bytes, maximum is %d", logical, len(logical), keyname.MaxLogicalLen)
		err.Action = "shorten the family"
	case fault != keyname.LogicalOK:
		err.Message = fmt.Sprintf("ends in a generation marker, but %q before it is no family", logical)
		err.Action = "name a generation <family>.v<N> (family with '.') or <family>-v<N> (family without '.'), with a family that does not end in a marker itself"
	case !keyname.ValidVersion(version):
		err.Message = fmt.Sprintf("generation %q must be a positive integer without leading zeros (v1, not v0 or v01)", version)
		err.Action = "rename it " + keyname.GenerationName(logical, "v1") + " or another canonical version"
	case strings.Contains(logical, keyname.Sep):
		err.Message = "a dotted family names its generations with a final v<N> segment"
		err.Action = "rename it " + keyname.GenerationName(logical, version)
	default:
		err.Message = fmt.Sprintf("family %q has no '.', so its generations are named %s-v<N>", logical, logical)
		err.Action = fmt.Sprintf("rename it %s, or give the family a second segment (%s.<purpose>.%s)", keyname.GenerationName(logical, version), logical, version)
	}
	return err
}

// checkSealSelectorFamilies is the cross-section rule between
// messaging.seal.active and keystore.keys: a selector whose spelling differs
// from a provisioned family's only in '-' versus '.' selects nothing, so the
// flip it was meant to make never happens and the old generation keeps
// sealing. MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN reaches payments.sign, never
// payments-sign. A selector naming nothing provisioned is still accepted; the
// keystore judges it when sealing resolves.
func checkSealSelectorFamilies(cfg *Config) error {
	if len(cfg.Messaging.Seal.Active) == 0 || len(cfg.KeyStore.Keys) == 0 {
		return nil
	}
	families := generationFamilies(slices.Sorted(maps.Keys(cfg.KeyStore.Keys)))
	byFold := make(map[string]string, len(families))
	for family := range families {
		byFold[keyname.Fold(family)] = family
	}
	for _, selector := range slices.Sorted(maps.Keys(cfg.Messaging.Seal.Active)) {
		if _, provisioned := families[selector]; provisioned {
			continue
		}
		family, lookalike := byFold[keyname.Fold(selector)]
		if !lookalike {
			continue
		}
		msg := fmt.Sprintf("selects %q, which is not provisioned; %q is (%s)", selector, family, strings.Join(families[family], ", "))
		if envVar := envVarForKey(fieldMessagingSealActive + "." + selector); envVar != "" {
			msg += fmt.Sprintf("; %s reaches only %s", envVar, selector)
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldMessagingSealActive + "." + selector,
			Message:  msg,
			Action: fmt.Sprintf("set the %s selector in YAML or as %s, or rename the family",
				family, keyToEnvVar(fieldMessagingSealActive+"."+family)),
		}
	}
	return nil
}

// validateKeyEntry validates a single keystore entry. An entry is exactly one
// of an RSA pair (public required, private optional), a symmetric secret, or a
// PKCS#12 bundle — a mixed entry is a structural error detected here without
// an explicit discriminator.
func validateKeyEntry(kp *KeyPairConfig, name string) error {
	hasSecret := kp.Secret.IsSet()
	hasAsymmetric := kp.Public.IsSet() || kp.Private.IsSet()

	if kp.PKCS12.IsSet() {
		if hasSecret || hasAsymmetric {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fmt.Sprintf(keystoreKeysFieldPrefix, name),
				Message:  "entry has a 'pkcs12' bundle alongside 'secret' or 'public'/'private' material",
				Action:   "configure an entry as exactly one of a 'secret', an RSA 'public'/'private' pair, or a 'pkcs12' bundle",
			}
		}
		return validatePKCS12Source(&kp.PKCS12, name)
	}

	if hasSecret && hasAsymmetric {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(keystoreKeysFieldPrefix, name),
			Message:  "entry has both a symmetric 'secret' and asymmetric 'public'/'private' material",
			Action:   "configure an entry as either a 'secret' or an RSA pair, not both",
		}
	}

	if hasSecret {
		return validateKeySource(kp.Secret, name, "secret", true)
	}

	if err := validateKeySource(kp.Public, name, "public", true); err != nil {
		return err
	}
	return validateKeySource(kp.Private, name, "private", false)
}

// validateKeySource checks that a key source has exactly one of file or value set.
// If required is true, at least one source must be configured.
func validateKeySource(src KeySourceConfig, keyName, keyType string, required bool) error {
	hasFile := src.File != ""
	hasValue := src.Value != ""

	if hasFile && hasValue {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf("keystore.keys.%s.%s", keyName, keyType),
			Message:  "both 'file' and 'value' set",
			Action:   "use exactly one of 'file' or 'value'",
		}
	}
	if required && !src.IsSet() {
		field := fmt.Sprintf("keystore.keys.%s.%s", keyName, keyType)
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    field,
			Message:  "key source required",
			Action:   keySourceAction(keyName, field),
		}
	}
	return nil
}

// keySourceAction names the two variables that set a missing source. A name
// with '-' is settable that way only from Docker or Kubernetes, never by a
// POSIX export, so the action also names the dotted spelling any shell can set.
// A generation has no such spelling: its fold is malformed (audit.v1) or a
// generation of another family (payments.sign.v1), so moving it is a family
// rename.
func keySourceAction(keyName, field string) string {
	action := "set either 'file' (path) or 'value' (base64)"
	fileVar, valueVar := envVarForKey(field+".file"), envVarForKey(field+".value")
	if fileVar == "" || valueVar == "" {
		return action
	}
	action += fmt.Sprintf(", e.g. %s or %s", fileVar, valueVar)
	if !strings.Contains(keyName, "-") {
		return action
	}
	action += "; a name with '-' is settable that way from Docker or Kubernetes but not by a POSIX export"
	if _, _, form := keyname.SplitGeneration(keyName); form != keyname.Ordinary {
		return action + "; a generation spelled without '-' belongs to another family, so moving to it is a family rename, drained before the cutover"
	}
	if dotted := keyname.Fold(keyName); keyname.ValidEntryName(dotted) {
		action += fmt.Sprintf(", while a dotted name (%s) is settable from any shell", dotted)
	}
	return action
}

// validatePKCS12Source checks the bundle has exactly one source and the
// password names exactly one indirection (env or file) — a literal password
// is not expressible in config.
func validatePKCS12Source(p *PKCS12SourceConfig, name string) error {
	if err := validateKeySource(p.Bundle(), name, "pkcs12", true); err != nil {
		return err
	}
	field := fmt.Sprintf(keystoreKeysFieldPrefix+".pkcs12.password", name)
	hasEnv := p.Password.Env != ""
	hasFile := p.Password.File != ""
	switch {
	case hasEnv && hasFile:
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field,
			Message:  "both 'env' and 'file' set",
			Action:   "use exactly one of 'env' or 'file'",
		}
	case !hasEnv && !hasFile:
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    field,
			Message:  "password source required",
			Action:   "set 'env' (the variable's name) or 'file' (a path); the password itself is never written in config",
		}
	case hasEnv && !envVarNamePattern.MatchString(p.Password.Env):
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field + ".env",
			Message:  "not an environment variable name (value elided)",
			Action:   "set 'env' to the name of the variable holding the password, never the password itself",
		}
	}
	return nil
}
