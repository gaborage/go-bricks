package keystore

import (
	"fmt"

	"github.com/gaborage/go-bricks/config"
)

// validateConfig checks that the keystore configuration is valid.
// Each key pair must have a public key with exactly one source (file or value).
// Private keys are optional but must also have exactly one source if configured.
func validateConfig(cfg *config.KeyStoreConfig) error {
	for name, kp := range cfg.Keys {
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
