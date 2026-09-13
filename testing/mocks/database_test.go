package mocks

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database/types"
)

var (
	_ types.Interface = (*MockDatabase)(nil)
	_ types.Session   = (*stubSession)(nil)
)

// stubSession is the handle the mock hands back; the mock stores it untouched,
// so only the Session surface matters here.
type stubSession struct{}

func (s *stubSession) Query(context.Context, string, ...any) (*sql.Rows, error) { return nil, nil }
func (s *stubSession) QueryRow(context.Context, string, ...any) types.Row       { return nil }
func (s *stubSession) Exec(context.Context, string, ...any) (sql.Result, error) { return nil, nil }
func (s *stubSession) DatabaseType() string                                     { return types.PostgreSQL }
func (s *stubSession) Begin(context.Context) (types.Tx, error)                  { return nil, nil }
func (s *stubSession) BeginTx(context.Context, *sql.TxOptions) (types.Tx, error) {
	return nil, nil
}
func (s *stubSession) Close() error { return nil }

func TestMockDatabaseSessionReturnsConfiguredSession(t *testing.T) {
	want := &stubSession{}
	db := &MockDatabase{}
	db.ExpectSession(want, nil)

	got, err := db.Session(t.Context())
	require.NoError(t, err)
	assert.Same(t, want, got)
	db.AssertExpectations(t)
}

func TestMockDatabaseSessionReturnsError(t *testing.T) {
	wantErr := errors.New("no free connection")
	db := &MockDatabase{}
	db.ExpectSession(nil, wantErr)

	got, err := db.Session(t.Context())
	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
	db.AssertExpectations(t)
}
