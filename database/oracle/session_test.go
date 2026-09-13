package oracle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSessionReturnsNilInterfaceOnAcquisitionFailure pins the interface VALUE,
// not just its reflected nil-ness: the plain `!=` below catches a typed nil,
// which reflection-based assert.Nil/require.Nil would pass.
func TestSessionReturnsNilInterfaceOnAcquisitionFailure(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	mock.ExpectClose()
	require.NoError(t, db.Close(), "closing the pool is the acquisition-failure seam")

	sess, err := c.Session(context.Background())
	require.Error(t, err, "acquiring a session from a closed pool must fail")
	if sess != nil {
		t.Fatalf("typed-nil session returned: %T", sess)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSessionOpensOnHealthyPool covers the success arm, so the assertion above
// cannot be satisfied by always returning nil.
func TestSessionOpensOnHealthyPool(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer func() {
		mock.ExpectClose()
		_ = db.Close()
	}()

	sess, err := c.Session(context.Background())
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Equal(t, "oracle", sess.DatabaseType())
	require.NoError(t, sess.Close())
}
