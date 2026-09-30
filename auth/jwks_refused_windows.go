//go:build windows

package auth

import (
	"errors"
	"syscall"
)

// wsaeConnRefused is Winsock's WSAECONNREFUSED. A refused dial on Windows
// surfaces as this errno, which errors.Is does not match to syscall.ECONNREFUSED.
const wsaeConnRefused = syscall.Errno(10061)

// isConnectionRefused reports a dial the issuer's host actively refused.
func isConnectionRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaeConnRefused)
}
