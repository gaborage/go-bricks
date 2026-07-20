package commands

import (
	"bytes"
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInfoCommandInvokesFlywayInfo(t *testing.T) {
	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "t1"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		"gobricks/migrate/t1": `{"type":"postgresql","host":"h1","port":5432,"database":"d1","username":"u1","password":"pw-tenant-1"}`,
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	stub, capture := stubFlywayCapturing(t, "info")
	cmd := NewInfoCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL, "--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL, "--aws-region", "us-east-1",
		"--flyway-path", stub, "--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())

	argv, err := os.ReadFile(capture)
	require.NoError(t, err)
	assert.Contains(t, string(argv), "info")
	assert.NotContains(t, string(argv), "migrate")
	assert.Contains(t, stdout.String(), "t1")
	assert.Contains(t, stdout.String(), "Info summary")
}
