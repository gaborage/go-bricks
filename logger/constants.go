package logger

// DefaultMaskValue es el valor utilizado para enmascarar datos sensibles
const DefaultMaskValue = "***"

// Log level string constants matching zerolog level names.
// Exported so other packages (server, app, config) can reuse the canonical
// level identifiers without redefining them.
const (
	LevelTrace = "trace"
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
	LevelFatal = "fatal"
	LevelPanic = "panic"
)

// Log entry field key constants.
const (
	fieldMessage = "message"
	fieldLevel   = "level"
)
