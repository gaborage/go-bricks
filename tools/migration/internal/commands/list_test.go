package commands

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCommandHTTPSourceText(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stdhttp.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"tenants":     []map[string]string{{"id": "tenant-a"}, {"id": "tenant-b"}},
				"next_cursor": "",
			},
			"meta": map[string]any{},
		})
	}))
	defer srv.Close()

	cmd := NewListCommand()
	cmd.SetArgs([]string{
		"--source-url", srv.URL,
	})

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	require.NoError(t, cmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "tenant-a")
	assert.Contains(t, out, "tenant-b")
}

func TestListCommandHTTPSourceJSON(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stdhttp.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"tenants":     []map[string]string{{"id": "t1"}},
				"next_cursor": "",
			},
			"meta": map[string]any{},
		})
	}))
	defer srv.Close()

	cmd := NewListCommand()
	cmd.SetArgs([]string{
		"--source-url", srv.URL,
		"--json",
	})

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	require.NoError(t, cmd.Execute())

	var parsed map[string]any
	require.NoError(t, json.NewDecoder(strings.NewReader(stdout.String())).Decode(&parsed))
	require.Contains(t, parsed, "tenants")
	tenants, ok := parsed["tenants"].([]any)
	require.True(t, ok, "tenants should decode to []any, got %T", parsed["tenants"])
	assert.Equal(t, []any{"t1"}, tenants)
}

func TestListCommandRequiresSource(t *testing.T) {
	cmd := NewListCommand()
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source-url")
}

func TestListCommandSourceMutualExclusion(t *testing.T) {
	cmd := NewListCommand()
	cmd.SetArgs([]string{
		"--source-url", "https://example.com",
		"--source-config", "/tmp/whatever.yaml",
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestListCommandRejectsWhitespaceOnlySelectors(t *testing.T) {
	// Whitespace-only flag values should be treated as unset; otherwise
	// `--tenant '   '` would pass the selector-count check but produce a
	// confusing downstream error.
	cmd := NewListCommand()
	cmd.SetArgs([]string{"--tenant", "   "})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestListCommandTenantFlag(t *testing.T) {
	cmd := NewListCommand()
	cmd.SetArgs([]string{"--tenant", "single-tenant"})

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	require.NoError(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "single-tenant")
}
