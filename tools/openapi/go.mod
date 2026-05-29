module github.com/gaborage/go-bricks/tools/openapi

go 1.25.0

require (
	github.com/getkin/kin-openapi v0.139.0
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/mod v0.36.0
	gopkg.in/yaml.v3 v3.0.1
)

// Local development - will be replaced with versioned import
replace go-bricks => ../../

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mohae/deepcopy v0.0.0-20170929034955-c48cc78d4826 // indirect
	github.com/oasdiff/yaml v0.1.0 // indirect
	github.com/oasdiff/yaml3 v0.0.13 // indirect
	github.com/perimeterx/marshmallow v1.1.5 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/woodsbury/decimal128 v1.3.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)
