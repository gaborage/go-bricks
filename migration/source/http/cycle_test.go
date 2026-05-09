package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTenantsRejectsRepeatedCursor(t *testing.T) {
	// Server that bounces between cursor "a" and cursor "b" forever.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		hits++
		var next string
		switch cursor {
		case "":
			next = "a"
		case "a":
			next = "b"
		default:
			next = "a"
		}
		body := map[string]any{
			"data": map[string]any{
				"tenants":     []map[string]string{{"id": "t-" + cursor}},
				"next_cursor": next,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{})
	require.NoError(t, err)
	_, err = src.ListTenants(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle detected")
	// Cycle should be detected within the first few pages.
	assert.LessOrEqual(t, hits, 4)
}
