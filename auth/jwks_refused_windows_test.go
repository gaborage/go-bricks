//go:build windows

package auth

import (
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsConnectionRefusedRecognizesWinsockRefusal(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", errWinsockConnRefused)}

	assert.True(t, isConnectionRefused(refused))
	assert.Equal(t, fetchFailure{class: fetchFailureOutage, stage: fetchStageConnect}, classifyFetchFailure(refused))
}
