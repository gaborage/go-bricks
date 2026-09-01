package config

import (
	"fmt"
	"slices"

	"github.com/gaborage/go-bricks/logger"
)

// checkLog rejects an unsupported log level, listing the allowed values.
func checkLog(cfg *LogConfig) error {
	validLevels := []string{logger.LevelTrace, logger.LevelDebug, logger.LevelInfo, logger.LevelWarn, logger.LevelError, logger.LevelFatal, logger.LevelPanic}
	if !slices.Contains(validLevels, cfg.Level) {
		return NewInvalidFieldError(fieldLogLevel, fmt.Sprintf(errNotSupportedFmt, cfg.Level), validLevels)
	}

	return nil
}
