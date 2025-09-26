package tracking

import (
	"sync"

	"github.com/gaborage/go-bricks/database/types"
)

// trackedRow wraps types.Row to delay tracking until Scan or Err is invoked.
type trackedRow struct {
	row    types.Row
	finish func(error)
	once   sync.Once
}

func wrapRowWithTracker(row types.Row, finish func(error)) types.Row {
	if row == nil || finish == nil {
		return row
	}
	return &trackedRow{row: row, finish: finish}
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
