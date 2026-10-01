//go:build !windows

package auth

import (
	"errors"
	"syscall"
)

// isConnectionRefused reports a dial the issuer's host actively refused.
func isConnectionRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
