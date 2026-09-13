package oracle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSessionReturnsNilInterfaceOnAcquisitionFailure pins the interface VALUE,
// not just its reflected nil-ness: Session returns (types.Session, error) while
// the underlying OpenSession returns (*wrapper.Session, error), so a bare
// `return c.OpenSession(...)` hands the caller a NON-nil types.Session
// interface wrapping a nil *wrapper.Session. The usual `if sess != nil { defer
// sess.Close() }` then panics.
//
// The comparison below is a PLAIN `!=` on the interface — assert.Nil/require.Nil
// are reflection-based and pass on a typed nil, so they cannot catch this.
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

// TestSessionOpensOnHealthyPool covers the success arm of the same method, so
// the nil-interface fix above cannot be satisfied by always returning nil.
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
