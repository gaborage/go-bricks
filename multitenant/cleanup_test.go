package multitenant

import (
	"context"
	"errors"
	"testing"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDB is a non-nil dbtypes.Interface whose methods are never called by
// FanOutRetentionCleanup (it only nil-checks the db and hands it to the delete
// closure, which ignores it). The embedded nil interface keeps the stub one line;
// importing database/testing here would create an import cycle (it imports multitenant).
type stubDB struct{ dbtypes.Interface }

func TestFanOutRetentionCleanupFansOutAndIsolatesErrors(t *testing.T) {
	log := logger.New("disabled", true)
	getDB := func(context.Context) (dbtypes.Interface, error) { return stubDB{}, nil }

	var seen []string
	del := func(ctx context.Context, _ dbtypes.Interface, _ time.Time) (int64, error) {
		tid, _ := GetTenant(ctx)
		seen = append(seen, tid)
		if tid == "bad" {
			return 0, errors.New("boom")
		}
		return 0, nil
	}

	err := FanOutRetentionCleanup(context.Background(), log, []string{"good", "bad"}, getDB, time.Hour, "outbox", del)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `outbox cleanup: tenant "bad": delete failed: boom`)
	assert.Equal(t, []string{"good", "bad"}, seen, "every tenant is attempted despite one failing")
}

func TestFanOutRetentionCleanupSingleTenantRunsOnce(t *testing.T) {
	log := logger.New("disabled", true)
	getDB := func(context.Context) (dbtypes.Interface, error) { return stubDB{}, nil }

	calls := 0
	del := func(context.Context, dbtypes.Interface, time.Time) (int64, error) { calls++; return 5, nil }

	require.NoError(t, FanOutRetentionCleanup(context.Background(), log, []string{""}, getDB, time.Hour, "inbox", del))
	assert.Equal(t, 1, calls, "single-tenant runs the delete exactly once")
}

func TestFanOutRetentionCleanupReportsDBUnavailable(t *testing.T) {
	log := logger.New("disabled", true)

	nilDB := func(context.Context) (dbtypes.Interface, error) { return nil, nil }
	err := FanOutRetentionCleanup(context.Background(), log, []string{""}, nilDB, time.Hour, "outbox", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")

	errDB := func(context.Context) (dbtypes.Interface, error) { return nil, errors.New("conn refused") }
	err = FanOutRetentionCleanup(context.Background(), log, []string{""}, errDB, time.Hour, "outbox", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
	assert.Contains(t, err.Error(), "conn refused")
}
