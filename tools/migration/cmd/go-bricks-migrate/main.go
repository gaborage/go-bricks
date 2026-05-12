package main

import (
	"fmt"
	"os"

	"github.com/gaborage/go-bricks/tools/migration/internal/commands"
)

var version = "dev" // Set via -ldflags at build time.

func main() {
	root := commands.NewRootCommand()
	root.Version = version
	root.AddCommand(
		commands.NewMigrateCommand(),
		commands.NewValidateCommand(),
		commands.NewInfoCommand(),
		commands.NewListCommand(),
		commands.NewVersionCommand(version),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
