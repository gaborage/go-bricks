package keystore

import (
	"fmt"
	"sort"

	"github.com/gaborage/go-bricks/config"
)

// validateConfig checks that the keystore configuration is valid.
// Each key pair must have a public key with exactly one source (file or value).
// Private keys are optional but must also have exactly one source if configured.
func validateConfig(cfg *config.KeyStoreConfig) error {
	if cfg == nil {
		return fmt.Errorf("keystore: config is nil")
	}

	// Sort keys for deterministic error ordering
	names := make([]string, 0, len(cfg.Keys))
	for name := range cfg.Keys {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		kp := cfg.Keys[name]
		if err := validateKeySource(kp.Public, name, "public", true); err != nil {
			return err
		}
		if err := validateKeySource(kp.Private, name, "private", false); err != nil {
			return err
		}
	}
	return nil
}

// validateKeySource checks that a key source has exactly one of file or value set.
// If required is true, at least one source must be configured.
func validateKeySource(src config.KeySourceConfig, certName, keyType string, required bool) error {
	hasFile := src.File != ""
	hasValue := src.Value != ""

	if hasFile && hasValue {
		return fmt.Errorf("keystore: key %q %s: both 'file' and 'value' set (use exactly one)", certName, keyType)
	}
	if required && !hasFile && !hasValue {
		return fmt.Errorf("keystore: key %q %s: requires either 'file' or 'value'", certName, keyType)
	}
	return nil
}
