package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/gaborage/go-bricks/tools/migration/internal/commands"
)

var version = "dev" // Set via -ldflags at build time.

// resolveVersion prefers the ldflags-injected version (set during `make build`),
// then falls back to module build info (populated for `go install ...@vX.Y.Z`),
// then to the literal default. debug.ReadBuildInfo is injected for testability.
func resolveVersion(ldflags string, read func() (*debug.BuildInfo, bool)) string {
	if ldflags != "" && ldflags != "dev" {
		return ldflags
	}
	if bi, ok := read(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	if ldflags != "" {
		return ldflags
	}
	return "dev"
}

func main() {
	version = resolveVersion(version, debug.ReadBuildInfo)
	root := commands.NewRootCommand()
	root.Version = version
	root.AddCommand(
		commands.NewMigrateCommand(),
		commands.NewValidateCommand(),
		commands.NewInfoCommand(),
		commands.NewListCommand(),
		commands.NewQuiesceCommand(),
		commands.NewVersionCommand(version),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
