package commands

import "os"

// Planted lint violation for the S17 drill (scratch PR, never merged):
// os.Remove's error is unchecked.
func lintDrill() { os.Remove("s17-lint-drill") }
