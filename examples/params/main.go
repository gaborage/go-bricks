//nolint:gocritic // Example code prioritizes simplicity over perfect defer handling
package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// CustomParams demonstrates how to recover custom parameters using the
// helpers that now ship with the config package.
type CustomParams struct {
	FeatureFlag bool `koanf:"feature.flag"`
	MaxItems    int  `koanf:"max.items"`

	Service struct {
		Endpoint string        `koanf:"endpoint"`
		Timeout  time.Duration `koanf:"timeout"`
	} `koanf:"service"`

	Tags []string          `koanf:"tags"`
	Meta map[string]string `koanf:"meta"`
}

func main() {
	// Seed the environment with the values required by config.Load and our
	// custom namespace. In a real application these would come from your
	// deployment environment or configuration management system.
	envVars := map[string]string{
		"DATABASE_DATABASE":       "exampledb",
		"DATABASE_USERNAME":       "example-user",
		"CUSTOM_FEATURE_FLAG":     "true",
		"CUSTOM_MAX_ITEMS":        "25",
		"CUSTOM_SERVICE_ENDPOINT": "https://api.example.com/v1",
		"CUSTOM_SERVICE_TIMEOUT":  "3s",
		"CUSTOM_TAGS_0":           "alpha",
		"CUSTOM_TAGS_1":           "beta",
		"CUSTOM_TAGS_2":           "gamma",
		"CUSTOM_META_OWNER":       "team-platform",
		"CUSTOM_META_PURPOSE":     "demo-custom-params",
		"CUSTOM_META_REGION":      "eu-central-1",
		"CUSTOM_API_KEY":          "secret-key-123",
	}

	// Set environment variables and collect keys for cleanup
	envKeys := make([]string, 0, len(envVars))
	for key, value := range envVars {
		if err := os.Setenv(key, value); err != nil {
			log.Fatalf("failed to set %s: %v", key, err)
		}
		envKeys = append(envKeys, key)
	}

	// Load the full application configuration (defaults + env overrides).
	cfg, err := config.Load()
	if err != nil {
		// Clean up environment variables before exiting
		for _, key := range envKeys {
			os.Unsetenv(key)
		}
		log.Fatalf("failed to load config: %v", err)
	}

	// Ensure cleanup happens at the end (after successful config load)
	defer func() {
		for _, key := range envKeys {
			os.Unsetenv(key)
		}
	}()

	// Unmarshal the entire custom namespace into a dedicated struct.
	var params CustomParams
	if err := cfg.Unmarshal("custom", &params); err != nil {
		log.Fatalf("failed to unmarshal custom params: %v", err)
	}

	timeoutStr := cfg.GetString("custom.service.timeout")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		log.Fatalf("invalid custom.service.timeout: %v", err)
	}

	fmt.Println("Custom parameters via helper accessors:")
	fmt.Printf("  custom.feature.flag: %v\n", cfg.GetBool("custom.feature.flag"))
	fmt.Printf("  custom.max.items: %d\n", cfg.GetInt("custom.max.items"))
	fmt.Printf("  custom.service.endpoint: %s\n", cfg.GetString("custom.service.endpoint"))
	fmt.Printf("  custom.service.timeout: %s\n", timeout)
	fmt.Printf("  custom.tags: %v\n", params.Tags)
	fmt.Printf("  custom.meta: %v\n", params.Meta)
	fmt.Printf("  custom.feature.flag exists? %v\n", cfg.Exists("custom.feature.flag"))
	fmt.Printf("  custom.nonexistent exists? %v\n", cfg.Exists("custom.nonexistent"))

	apiKey, err := cfg.GetRequiredString("custom.api.key")
	if err != nil {
		log.Fatalf("missing required custom.api.key: %v", err)
	}
	fmt.Printf("  custom.api.key: %s\n", apiKey)

	fmt.Println("\nCustom parameters unmarshalled into struct:")
	fmt.Printf("  %+v\n", params)

	// Or inspect the namespace as a generic map when you need dynamic access.
	if customMap := cfg.Custom(); customMap != nil {
		fmt.Println("\nCustom namespace map view:")
		keys := make([]string, 0, len(customMap))
		for key := range customMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  %s -> %v\n", key, customMap[key])
		}
	}
}
