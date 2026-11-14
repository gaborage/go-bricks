//go:build integration

package containers

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

func TestPostgreSQLOptionsZeroValueAddsNoCmdOrTmpfs(t *testing.T) {
	req := requestFromOptions(t, postgreSQLOptions(DefaultPostgreSQLConfig()))

	assert.Empty(t, req.Cmd)
	assert.Empty(t, req.Tmpfs)
}

func TestPostgreSQLOptionsRenderSettingsAndTmpfs(t *testing.T) {
	cfg := DefaultPostgreSQLConfig()
	cfg.Settings = map[string]string{"work_mem": "7MB", "max_connections": "42"}
	cfg.DataTmpfs = "256m"

	req := requestFromOptions(t, postgreSQLOptions(cfg))

	assert.Equal(t, []string{"-c", "max_connections=42", "-c", "work_mem=7MB"}, req.Cmd)
	assert.Equal(t, map[string]string{"/var/lib/postgresql": "rw,size=256m"}, req.Tmpfs)
}

func TestStartPostgreSQLContainerRejectsInvalidSettingBeforeStart(t *testing.T) {
	cfg := DefaultPostgreSQLConfig()
	cfg.Settings = map[string]string{"work mem": "7MB"}

	got, err := startPostgreSQLContainerInternal(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), `"work mem"`)
	assert.NotContains(t, err.Error(), "failed to start PostgreSQL container")
}

func TestStartPostgreSQLContainerAppliesSettings(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultPostgreSQLConfig()
	cfg.Settings = map[string]string{"work_mem": "7MB"}

	pg := MustStartPostgreSQLContainer(ctx, t, cfg).WithCleanup(t)

	db, err := pg.admin()
	require.NoError(t, err)
	var workMem string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW work_mem").Scan(&workMem))
	assert.Equal(t, "7MB", workMem)
}

func TestStartPostgreSQLContainerBacksDataDirectoryWithTmpfs(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultPostgreSQLConfig()
	cfg.DataTmpfs = "256m"

	pg := MustStartPostgreSQLContainer(ctx, t, cfg).WithCleanup(t)

	db, err := pg.admin()
	require.NoError(t, err)
	var dataDir string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW data_directory").Scan(&dataDir))

	code, out, err := pg.container.Exec(ctx, []string{"stat", "-f", "-c", "%T", dataDir}, tcexec.Multiplexed())
	require.NoError(t, err)
	fsType, err := io.ReadAll(out)
	require.NoError(t, err)
	require.Equal(t, 0, code, "stat output: %s", fsType)
	assert.Equal(t, "tmpfs", strings.TrimSpace(string(fsType)))
}
