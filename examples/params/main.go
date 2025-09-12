package main

import (
	"fmt"
	"log"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	koanf "github.com/knadh/koanf/v2"
)

// CustomParams demonstrates how to recover custom parameters
// from a YAML configuration file using koanf.
type CustomParams struct {
	FeatureFlag bool `koanf:"feature_flag"`
	MaxItems    int  `koanf:"max_items"`

	Service struct {
		Endpoint string        `koanf:"endpoint"`
		Timeout  time.Duration `koanf:"timeout"`
	} `koanf:"service"`

	Tags []string          `koanf:"tags"`
	Meta map[string]string `koanf:"meta"`
}

func main() {
	// Create koanf instance with dot-delimited paths
	k := koanf.New(".")

	// Load example YAML from this folder
	if err := k.Load(file.Provider("examples/params/config.yaml"), yaml.Parser()); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Unmarshal only the `custom` subtree into our struct
	var params CustomParams
	if err := k.Unmarshal("custom", &params); err != nil {
		log.Fatalf("failed to unmarshal custom params: %v", err)
	}

	// Print recovered values
	fmt.Println("Custom parameters recovered from YAML:")
	fmt.Printf("  feature_flag: %v\n", params.FeatureFlag)
	fmt.Printf("  max_items: %d\n", params.MaxItems)
	fmt.Printf("  service.endpoint: %s\n", params.Service.Endpoint)
	fmt.Printf("  service.timeout: %s\n", params.Service.Timeout)
	fmt.Printf("  tags: %v\n", params.Tags)
	fmt.Printf("  meta: %v\n", params.Meta)

	// Access a single value without defining a struct (optional)
	// This reads the raw value directly by path.
	single := k.String("custom.service.endpoint")
	fmt.Printf("\nDirect access example (custom.service.endpoint): %s\n", single)
}
