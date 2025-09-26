package rowtracker

import (
	"sync"

	"github.com/gaborage/go-bricks/database/types"
)

// Wrap returns a types.Row wrapper that invokes finish once when Scan or Err is called.
// It is safe to pass a nil row or finish function; in those cases the original row is returned.
func Wrap(row types.Row, finish func(error)) types.Row {
	if row == nil || finish == nil {
		return row
	}
	return &trackedRow{row: row, finish: finish}
}

type trackedRow struct {
	row    types.Row
	finish func(error)
	once   sync.Once
}

func (tr *trackedRow) Scan(dest ...any) error {
	err := tr.row.Scan(dest...)
	tr.done(err)
	return err
}

func (tr *trackedRow) Err() error {
	err := tr.row.Err()
	if err != nil {
		tr.done(err)
	}
	return err
}

func (tr *trackedRow) done(err error) {
	tr.once.Do(func() {
		tr.finish(err)
	})
}
