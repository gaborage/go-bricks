module github.com/gaborage/go-bricks/tools/openapi

go 1.25

require (
	github.com/spf13/cobra v1.10.1
	github.com/stretchr/testify v1.11.1
	golang.org/x/mod v0.29.0
	gopkg.in/yaml.v3 v3.0.1
)

// Local development - will be replaced with versioned import
replace go-bricks => ../../

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
)
