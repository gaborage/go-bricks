package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// stubKind drives a probeDescription through every branch of the judge without a manager.
type stubKind struct {
	acquireErr  error
	liveErr     error
	stats       map[string]any
	acquired    int
	released    int
	statsCalls  int
	statsBefore int // statsCalls observed at release time — proves the snapshot is taken while held
}

func (s *stubKind) description(name string, critical, absent, perTenant, leaseless bool) probeDescription {
	d := probeDescription{name: name, critical: critical, absent: absent, perTenant: perTenant}
	d.stats = func() map[string]any {
		s.statsCalls++
		return s.stats
	}
	live := func(context.Context) error { return s.liveErr }
	if leaseless {
		d.live = live
		return d
	}
	d.acquire = func(context.Context) (func(context.Context) error, func(), error) {
		s.acquired++
		if s.acquireErr != nil {
			return nil, nil, s.acquireErr
		}
		return live, func() {
			s.released++
			s.statsBefore = s.statsCalls
		}, nil
	}
	return d
}

func TestProbeDescriptionJudge(t *testing.T) {
	notConfigured := config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
	require.True(t, config.IsNotConfigured(notConfigured), "fixture must be a not-configured error")
	boom := errors.New("dial tcp: connection refused")

	tests := []struct {
		name         string
		stub         stubKind
		critical     bool
		absent       bool
		perTenant    bool
		leaseless    bool
		wantStatus   string
		wantErr      error
		wantAcquired int
		wantReleased int
	}{
		{name: "healthy_when_lease_and_liveness_succeed", stub: stubKind{stats: map[string]any{"active": 1}}, critical: true, wantStatus: healthyStatus, wantAcquired: 1, wantReleased: 1},
		{name: "unhealthy_with_err_when_liveness_fails", stub: stubKind{liveErr: boom}, critical: true, wantStatus: unhealthyStatus, wantErr: boom, wantAcquired: 1, wantReleased: 1},
		{name: "unhealthy_with_err_when_lease_fails", stub: stubKind{acquireErr: boom}, wantStatus: unhealthyStatus, wantErr: boom, wantAcquired: 1},
		{name: "not_configured_when_lease_is_not_configured", stub: stubKind{acquireErr: notConfigured}, wantStatus: notConfiguredStatus, wantAcquired: 1},
		{name: "per_tenant_when_lease_is_not_configured_and_per_tenant", stub: stubKind{acquireErr: notConfigured}, perTenant: true, wantStatus: perTenantStatus, wantAcquired: 1},
		{name: "not_configured_without_leasing_when_absent", stub: stubKind{acquireErr: boom}, absent: true, wantStatus: notConfiguredStatus},
		{name: "per_tenant_without_leasing_when_absent_and_per_tenant", stub: stubKind{acquireErr: boom}, absent: true, perTenant: true, wantStatus: perTenantStatus},
		{name: "leaseless_kind_healthy", stub: stubKind{}, leaseless: true, wantStatus: healthyStatus},
		{name: "leaseless_kind_unhealthy_with_err", stub: stubKind{liveErr: boom}, leaseless: true, wantStatus: unhealthyStatus, wantErr: boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := tt.stub
			d := stub.description("kind", tt.critical, tt.absent, tt.perTenant, tt.leaseless)

			got := d.Run(context.Background())

			assert.Equal(t, "kind", got.Name)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.critical, got.Critical)
			assert.Equal(t, tt.wantStatus, got.Details[statusKey], "details.status mirrors the verdict")
			if tt.wantErr != nil {
				assert.ErrorIs(t, got.Err, tt.wantErr)
			} else {
				assert.NoError(t, got.Err)
			}
			assert.Equal(t, tt.wantAcquired, stub.acquired, "lease attempts")
			assert.Equal(t, tt.wantReleased, stub.released, "lease releases")
			if tt.wantReleased == 1 {
				assert.Equal(t, 1, stub.statsBefore, "stats snapshot is taken while the lease is held")
			}
			if tt.stub.stats != nil {
				assert.Equal(t, 1, got.Details["active"], "kind statistics are carried into details")
			}
		})
	}
}

func TestProbeDescriptionUnhealthyAlwaysCarriesAnError(t *testing.T) {
	// A liveness check that reports "not live" without an error still needs an Err on the
	// status — the vocabulary rule that lets one predicate serve /ready and the debug summary.
	d := probeDescription{name: "kind", live: func(context.Context) error { return errStreamsNotOpen }}
	got := d.Run(context.Background())
	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errStreamsNotOpen)
}

func TestProbeDescriptionDisabled(t *testing.T) {
	got := disabledProbe(componentCache).Run(context.Background())
	assert.Equal(t, HealthStatus{
		Name:    componentCache,
		Status:  disabledStatus,
		Details: map[string]any{statusKey: disabledStatus},
	}, got)
}

func TestProbeDescriptionCopiesStats(t *testing.T) {
	source := map[string]any{"errors": 0}
	d := probeDescription{name: "kind", live: func(context.Context) error { return nil }, stats: func() map[string]any { return source }}
	got := d.Run(context.Background())
	got.Details["errors"] = 99
	assert.Equal(t, 0, source["errors"], "Run must not hand the caller the kind's own map")
	assert.NotContains(t, source, statusKey, "the status key is stamped on the copy, never on the source")
}

func TestProbeDescriptionNilStats(t *testing.T) {
	d := probeDescription{name: "kind", live: func(context.Context) error { return nil }, stats: func() map[string]any { return nil }}
	got := d.Run(context.Background())
	assert.Equal(t, map[string]any{statusKey: healthyStatus}, got.Details)
}
