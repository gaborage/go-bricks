package testutil

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnsetPostgresSSLEnv(t *testing.T) {
	for _, k := range PostgresSSLEnvVars {
		t.Setenv(k, "ambient")
	}
	UnsetPostgresSSLEnv()
	for _, k := range PostgresSSLEnvVars {
		_, set := os.LookupEnv(k)
		assert.False(t, set, k)
	}
}
