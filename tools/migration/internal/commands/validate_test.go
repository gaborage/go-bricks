package commands

import (
	"bytes"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFlywayCapturing writes a shell script that records its argv and its
// environment to separate files and emits a success envelope for the given
// operation. Credentials reach Flyway by environment only, so the env dump is the
// only place an overlaid credential is observable. The envelope carries
// targetSchemaVersion, so it models an applied run rather than a no-op rerun for
// every caller. Skips on Windows.
func stubFlywayCapturing(t *testing.T, operation string) (stubPath, argvPath, envPath string) {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	dir := t.TempDir()
	argvPath = filepath.Join(dir, "argv.txt")
	envPath = filepath.Join(dir, "env.txt")
	envelope := `{"operation":"` + operation + `","success":true,"targetSchemaVersion":"2","flywayVersion":"12.8.1"}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"" + argvPath + "\"\nenv >> \"" + envPath + "\"\necho '" + envelope + "'\nexit 0\n"
	stubPath = filepath.Join(dir, "flyway-capture.sh")
	require.NoError(t, os.WriteFile(stubPath, []byte(script), 0o755))
	return stubPath, argvPath, envPath
}

func TestValidateCommandInvokesFlywayValidate(t *testing.T) {
	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "t1"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		secretName("t1"): canonicalTenantSecret("h1", "d1", "u1"),
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	stub, capture, _ := stubFlywayCapturing(t, "validate")
	cmd := NewValidateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL, "--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL, "--aws-region", "us-east-1",
		"--flyway-path", stub, "--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(t.Context())
	require.NoError(t, cmd.Execute())

	argv, err := os.ReadFile(capture)
	require.NoError(t, err)
	args := strings.Split(strings.TrimSpace(string(argv)), "\n")
	assert.Contains(t, args, "validate")
	assert.NotContains(t, args, "migrate")
	assert.Contains(t, stdout.String(), "t1")
	assert.Contains(t, stdout.String(), "Validate summary")
}
