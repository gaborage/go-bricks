package commands

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// NewVersionCommand creates the version command
func NewVersionCommand(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long:  "Display version information for go-bricks-openapi tool",
		Run: func(_ *cobra.Command, _ []string) {
			printVersion(version)
		},
	}

	return cmd
}

func printVersion(version string) {
	fmt.Printf("go-bricks-openapi version %s\n", version)
	fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Println("OpenAPI specification version: 3.0.1")
}
