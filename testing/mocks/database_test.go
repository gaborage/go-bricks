package mocks

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/database/types"
)

var _ types.Interface = (*MockDatabase)(nil)

// TestMockDatabaseSessionReturnsConfiguredSessionOrError covers both arms of the
// mock's session door: it hands back the types.Session the test supplied, or the
// error, without touching either. The session double is the framework's own
// TestSession rather than a hand-written stub.
func TestMockDatabaseSessionReturnsConfiguredSessionOrError(t *testing.T) {
	want := dbtesting.NewTestDB(types.PostgreSQL).ExpectSession()
	db := &MockDatabase{}
	db.ExpectSession(want, nil)

	got, err := db.Session(t.Context())
	require.NoError(t, err)
	assert.Same(t, want, got)
	db.AssertExpectations(t)

	wantErr := errors.New("no free connection")
	failing := &MockDatabase{}
	failing.ExpectSession(nil, wantErr)

	got, err = failing.Session(t.Context())
	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
	failing.AssertExpectations(t)
}
