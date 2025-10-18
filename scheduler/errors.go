package scheduler

import "fmt"

// ValidationError represents a validation error during job registration.
// Per Constitution VII: Error messages follow format "scheduler: <field> <message> <action>"
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("scheduler: %s %s", e.Field, e.Message)
}
