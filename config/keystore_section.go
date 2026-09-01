package config

import (
	"fmt"
	"slices"
	"strings"
)

// normalizeKeyStore fills the nil default: an unset SecretMinLength becomes
// DefaultKeyStoreSecretMinLength (32). An explicit 0 or N is left untouched —
// 0 keeps the floor off (deprecated) and check rejects a negative. Nothing
// here can fail.
func normalizeKeyStore(cfg *KeyStoreConfig) {
	if cfg.SecretMinLength == nil {
		cfg.SecretMinLength = new(cfg.SecretFloor())
	}
}

// checkKeyStore returns nil if no keys are configured. A set SecretMinLength
// must be non-negative — nil is left alone since white-box tests call
// checkKeyStore directly, before normalize has filled it. Each entry is
// either an RSA pair (public required with exactly one source, private
// optional) or a symmetric secret — a mixed entry is rejected. Each entry's
// NAME is judged first, against the env-reachability grammar.
func checkKeyStore(cfg *KeyStoreConfig) error {
	if cfg.SecretMinLength != nil && *cfg.SecretMinLength < 0 {
		return NewValidationError("keystore.secretminlength", errMustBeNonNegative)
	}

	if len(cfg.Keys) == 0 {
		return nil
	}

	// Sort keys for deterministic error ordering
	names := make([]string, 0, len(cfg.Keys))
	for name := range cfg.Keys {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		// A '.' collides with koanf's path delimiter: the constructed section
		// path keystore.keys.<name>.public becomes ambiguous, and so would this
		// name's own error Field — keystore.keys.my.key reads as a "key" under
		// "my". The parent field is reported instead, as the databases and
		// static-tenant rules do, and this runs first so the ambiguous path is
		// never built.
		if strings.Contains(name, ".") {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldKeystoreKeys,
				Message:  fmt.Sprintf("key name %q cannot contain '.' (the config path delimiter)", name),
				Action:   "rename the keystore.keys entry without dots",
			}
		}
		// The name is judged before its sources: an unreachable entry cannot be
		// configured by environment variable whatever its file or value says.
		if err := checkSectionName(fmt.Sprintf(keystoreKeysFieldPrefix, name), name); err != nil {
			return err
		}
		kp := cfg.Keys[name]
		if err := validateKeyEntry(&kp, name); err != nil {
			return err
		}
	}
	return nil
}

// validateKeyEntry validates a single keystore entry. An entry is either an
// RSA pair (public required, private optional) or a symmetric secret — a mixed
// entry is a structural error detected here without an explicit discriminator.
func validateKeyEntry(kp *KeyPairConfig, name string) error {
	hasSecret := kp.Secret.IsSet()
	hasAsymmetric := kp.Public.IsSet() || kp.Private.IsSet()

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
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    fmt.Sprintf("keystore.keys.%s.%s", keyName, keyType),
			Message:  "key source required",
			Action:   "set either 'file' (path) or 'value' (base64)",
		}
	}
	return nil
}
