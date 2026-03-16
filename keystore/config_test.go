package keystore

import (
	"testing"

	"github.com/gaborage/go-bricks/config"
	"github.com/stretchr/testify/assert"
)

func TestValidateConfigValid(t *testing.T) {
	cfg := &config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"signing": {
				Public:  config.KeySourceConfig{File: "pub.der"},
				Private: config.KeySourceConfig{Value: "base64data"},
			},
		},
	}
	assert.NoError(t, validateConfig(cfg))
}

func TestValidateConfigPublicKeyRequired(t *testing.T) {
	cfg := &config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"missing": {
				Public: config.KeySourceConfig{}, // Neither file nor value
			},
		},
	}
	err := validateConfig(cfg)
	assert.ErrorContains(t, err, "requires either 'file' or 'value'")
}

func TestValidateConfigPublicBothSourcesSet(t *testing.T) {
	cfg := &config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"both": {
				Public: config.KeySourceConfig{File: "a.der", Value: "also"},
			},
		},
	}
	err := validateConfig(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestValidateConfigPrivateBothSourcesSet(t *testing.T) {
	cfg := &config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"both-priv": {
				Public:  config.KeySourceConfig{File: "pub.der"},
				Private: config.KeySourceConfig{File: "priv.der", Value: "also"},
			},
		},
	}
	err := validateConfig(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestValidateConfigPrivateKeyOptional(t *testing.T) {
	cfg := &config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{
			"pub-only": {
				Public: config.KeySourceConfig{File: "pub.der"},
				// Private not set — valid
			},
		},
	}
	assert.NoError(t, validateConfig(cfg))
}

func TestValidateConfigEmptyKeys(t *testing.T) {
	cfg := &config.KeyStoreConfig{
		Keys: map[string]config.KeyPairConfig{},
	}
	assert.NoError(t, validateConfig(cfg))
}
