package tenantstore

import (
	"context"
	"errors"
	"testing"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore implements TableCreator with a controllable CreateTable outcome.
type fakeStore struct {
	tableName string
	createErr error
	creates   int
}

func (s *fakeStore) CreateTable(context.Context, dbtypes.Interface) error {
	s.creates++
	return s.createErr
}

// newTestDeps builds Deps backed by the given GetDB; newStore tracks calls.
func newTestDeps(getDB func(context.Context) (dbtypes.Interface, error), autoCreate bool, createErr error) (deps *Deps[*fakeStore], storeVendors *[]string) {
	vendors := []string{}
	d := &Deps[*fakeStore]{
		Name:            "testmod",
		TableName:       "test_table",
		AutoCreateTable: autoCreate,
		Logger:          logger.New("info", false),
		GetDB:           getDB,
		NewStore: func(vendor, tableName string) (*fakeStore, error) {
			vendors = append(vendors, vendor)
			return &fakeStore{tableName: tableName, createErr: createErr}, nil
		},
		WarnMsg: "test table creation failed",
	}
	return d, &vendors
}

func singleDB(db dbtypes.Interface) func(context.Context) (dbtypes.Interface, error) {
	return func(context.Context) (dbtypes.Interface, error) { return db, nil }
}

func TestCacheGetCreatesStorePerTenant(t *testing.T) {
	tenants := dbtesting.NewTenantDBMap()
	tenants.ForTenantWithVendor("tenant-a", dbtypes.PostgreSQL)
	tenants.ForTenantWithVendor("tenant-b", dbtypes.Oracle)
	d, vendors := newTestDeps(tenants.AsGetDBFunc(), false, nil)
	c := &Cache[*fakeStore]{}

	storeA, err := c.Get(multitenant.SetTenant(t.Context(), "tenant-a"), d)
	require.NoError(t, err)
	storeB, err := c.Get(multitenant.SetTenant(t.Context(), "tenant-b"), d)
	require.NoError(t, err)

	assert.NotSame(t, storeA, storeB, "different tenants receive isolated stores")
	assert.Equal(t, []string{dbtypes.PostgreSQL, dbtypes.Oracle}, *vendors, "each tenant's store is built for its own vendor")
	assert.Equal(t, "test_table", storeA.tableName, "table name passes through to the store factory")
}

func TestCacheGetReturnsCachedStoreOnHit(t *testing.T) {
	d, vendors := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	c := &Cache[*fakeStore]{}

	first, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	second, err := c.Get(t.Context(), d)
	require.NoError(t, err)

	assert.Same(t, first, second, "the empty-tenant store is reused across calls")
	assert.Len(t, *vendors, 1, "NewStore runs once per tenant")
}

func TestCacheGetReturnsGetDBError(t *testing.T) {
	cause := errors.New("connection refused")
	d, _ := newTestDeps(func(context.Context) (dbtypes.Interface, error) { return nil, cause }, false, nil)
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "testmod: database unavailable")
	assert.Nil(t, store)
	_, ok := c.Cached("")
	assert.False(t, ok, "a failed init caches nothing, so it can retry")
}

func TestCacheGetReturnsNewStoreError(t *testing.T) {
	cause := errors.New("bad table name")
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	calls := 0
	d.NewStore = func(_, _ string) (*fakeStore, error) {
		calls++
		return nil, cause
	}
	c := &Cache[*fakeStore]{}

	_, err := c.Get(t.Context(), d)
	require.ErrorIs(t, err, cause, "factory errors propagate unwrapped (the factory owns its prefix)")

	_, err = c.Get(t.Context(), d)
	require.ErrorIs(t, err, cause)
	assert.Equal(t, 2, calls, "a failed init retries instead of caching the failure")
}

func TestCacheGetAutoCreateTableWarnOnly(t *testing.T) {
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), true, errors.New("ORA-00955"))
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err, "a CreateTable failure is warn-only; Get still succeeds")
	assert.Equal(t, 1, store.creates)

	again, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	assert.Same(t, store, again, "the store is cached despite the CreateTable failure")
	assert.Equal(t, 1, store.creates, "one CreateTable attempt per tenant")
}

func TestCacheGetSkipsCreateTableWhenDisabled(t *testing.T) {
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	assert.Zero(t, store.creates, "CreateTable is not attempted when AutoCreateTable is false")
}

func TestCacheCached(t *testing.T) {
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	c := &Cache[*fakeStore]{}

	_, ok := c.Cached("")
	assert.False(t, ok, "miss before first Get")

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err)

	cached, ok := c.Cached("")
	assert.True(t, ok, "hit after Get")
	assert.Same(t, store, cached)

	_, ok = c.Cached("other-tenant")
	assert.False(t, ok, "miss for a tenant never initialized")
}
