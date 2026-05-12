package commands

import (
	"bytes"
	"context"
	"encoding/json"
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

const windowsOS = "windows"

// writeEnvelope writes a go-bricks-style APIResponse envelope (status 200) to w.
func writeEnvelope(w stdhttp.ResponseWriter, data map[string]any) {
	body := map[string]any{"meta": map[string]any{}}
	if data != nil {
		body["data"] = data
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// stubFlyway writes a shell script that always exits 0 to a temp file and
// returns its path. Skips on Windows.
func stubFlyway(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-stub.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	return path
}

// stubFlywayFailing writes a script that exits 1 (always failing).
func stubFlywayFailing(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell script stub not supported on windows CI")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway-fail.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho stub-failure 1>&2\nexit 1\n"), 0o755))
	return path
}

// fakeSecretsManager spins up a fake AWS SM endpoint that returns the supplied
// secret-name → JSON-payload map. It serves the /secretsmanager.GetSecretValue
// JSON-1.1 protocol that the AWS SDK uses.
func fakeSecretsManager(t *testing.T, secrets map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req struct {
			SecretID string `json:"SecretId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
			return
		}
		payload, ok := secrets[req.SecretID]
		if !ok {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.WriteHeader(stdhttp.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"__type":  "ResourceNotFoundException",
				"message": "secret not found: " + req.SecretID,
			})
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(stdhttp.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ARN":          "arn:aws:secretsmanager:us-east-1:000:secret:" + req.SecretID,
			"Name":         req.SecretID,
			"SecretString": payload,
			"VersionId":    "v1",
		})
	}))
	return srv
}

func TestMigrateCommandSuccessAcrossPagesAndShapes(t *testing.T) {
	flyway := stubFlyway(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			writeEnvelope(w, map[string]any{
				"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}},
				"next_cursor": "p2",
			})
		default:
			writeEnvelope(w, map[string]any{
				"tenants":     []map[string]string{{"id": "t3"}},
				"next_cursor": "",
			})
		}
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		// Canonical shape
		"gobricks/migrate/t1": `{"type":"postgresql","host":"h1","port":5432,"database":"d1","username":"u1","password":"p1"}`,
		"gobricks/migrate/t2": `{"type":"postgresql","host":"h2","port":5432,"database":"d2","username":"u2","password":"p2"}`,
		// RDS rotation fallback
		"gobricks/migrate/t3": `{"engine":"postgres","host":"h3","port":5432,"dbname":"d3","username":"u3","password":"p3"}`,
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	require.NoError(t, cmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "t2")
	assert.Contains(t, out, "t3")
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "summary")
}

func TestMigrateCommandFailFastStopsAfterFirstFailure(t *testing.T) {
	flyway := stubFlywayFailing(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}, {"id": "t3"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		"gobricks/migrate/t1": `{"type":"postgresql","host":"h","port":5432,"database":"d","username":"u","password":"p"}`,
		"gobricks/migrate/t2": `{"type":"postgresql","host":"h","port":5432,"database":"d","username":"u","password":"p"}`,
		"gobricks/migrate/t3": `{"type":"postgresql","host":"h","port":5432,"database":"d","username":"u","password":"p"}`,
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)

	// Default fail-fast should stop after t1 — t2/t3 should NOT appear in output.
	out := stdout.String()
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "FAIL")
	assert.NotContains(t, out, "t2")
	assert.NotContains(t, out, "t3")
}

func TestMigrateCommandContinueOnErrorListsAllFailures(t *testing.T) {
	flyway := stubFlywayFailing(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		"gobricks/migrate/t1": `{"type":"postgresql","host":"h","port":5432,"database":"d","username":"u","password":"p"}`,
		"gobricks/migrate/t2": `{"type":"postgresql","host":"h","port":5432,"database":"d","username":"u","password":"p"}`,
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
		"--continue-on-error",
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err) // exit non-zero because there were failures

	out := stdout.String()
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "t2")
	assert.Equal(t, 2, strings.Count(out, "FAIL"))
}

func TestMigrateCommandMalformedSecretMentionsTenant(t *testing.T) {
	flyway := stubFlyway(t)

	listSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeEnvelope(w, map[string]any{
			"tenants":     []map[string]string{{"id": "broken"}},
			"next_cursor": "",
		})
	}))
	defer listSrv.Close()

	smSrv := fakeSecretsManager(t, map[string]string{
		"gobricks/migrate/broken": `not a json`,
	})
	defer smSrv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	cmd := NewMigrateCommand()
	cmd.SetArgs([]string{
		"--source-url", listSrv.URL,
		"--allow-insecure-scheme",
		"--aws-endpoint", smSrv.URL,
		"--aws-region", "us-east-1",
		"--flyway-path", flyway,
		"--flyway-config", flywayConfPath(t),
		"--migrations-dir", makeTempDir(t),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	out := stdout.String()
	assert.Contains(t, out, "broken")
	assert.Contains(t, out, "gobricks/migrate/broken")
}

// flywayConfPath creates an empty flyway.conf in a temp directory and returns its path.
func flywayConfPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flyway.conf")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))
	return path
}

// makeTempDir creates an empty temp directory and returns its path.
func makeTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}
