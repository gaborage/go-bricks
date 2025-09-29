package rowtracker

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

const (
	finishCallCountErrorMsg = "finish should be called exactly once"
)

// MockRow implements types.Row for testing
type MockRow struct {
	mock.Mock
}

func (m *MockRow) Scan(dest ...any) error {
	args := m.Called(dest)
	return args.Error(0)
}

func (m *MockRow) Err() error {
	args := m.Called()
	return args.Error(0)
}

func TestWrap(t *testing.T) {
	t.Run("nil row returns original", func(t *testing.T) {
		finishCalled := false
		finish := func(error) { finishCalled = true }

		result := Wrap(nil, finish)

		assert.Nil(t, result)
		assert.False(t, finishCalled, "finish should not be called for nil row")
	})

	t.Run("nil finish returns original row", func(t *testing.T) {
		mockRow := &MockRow{}

		result := Wrap(mockRow, nil)

		assert.Equal(t, mockRow, result)
	})

	t.Run("both nil returns nil", func(t *testing.T) {
		result := Wrap(nil, nil)
		assert.Nil(t, result)
	})

	t.Run("valid row and finish returns trackedRow", func(t *testing.T) {
		mockRow := &MockRow{}
		finish := func(error) {
			// no-op
		}

		result := Wrap(mockRow, finish)

		assert.NotNil(t, result)
		assert.IsType(t, &trackedRow{}, result)
	})
}

func TestTrackedRowScan(t *testing.T) {
	t.Run("successful scan calls finish once", func(t *testing.T) {
		mockRow := &MockRow{}
		var finishErr error
		finishCallCount := 0
		finish := func(err error) {
			finishCallCount++
			finishErr = err
		}

		mockRow.On("Scan", mock.Anything).Return(nil)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		var dest []any
		err := trackedRow.Scan(dest...)

		assert.NoError(t, err)
		assert.Equal(t, 1, finishCallCount, finishCallCountErrorMsg)
		assert.NoError(t, finishErr)
		mockRow.AssertExpectations(t)
	})

	t.Run("scan error calls finish with error", func(t *testing.T) {
		mockRow := &MockRow{}
		expectedErr := errors.New("scan error")
		var finishErr error
		finishCallCount := 0
		finish := func(err error) {
			finishCallCount++
			finishErr = err
		}

		mockRow.On("Scan", mock.Anything).Return(expectedErr)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		var dest []any
		err := trackedRow.Scan(dest...)

		assert.Equal(t, expectedErr, err)
		assert.Equal(t, 1, finishCallCount, finishCallCountErrorMsg)
		assert.Equal(t, expectedErr, finishErr, "finish should receive the same error")
		mockRow.AssertExpectations(t)
	})

	t.Run("multiple scan calls only trigger finish once", func(t *testing.T) {
		mockRow := &MockRow{}
		finishCallCount := 0
		finish := func(_ error) {
			finishCallCount++
		}

		mockRow.On("Scan", mock.Anything).Return(nil)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		var dest []any
		_ = trackedRow.Scan(dest...)
		_ = trackedRow.Scan(dest...)
		_ = trackedRow.Scan(dest...)

		assert.Equal(t, 1, finishCallCount, "finish should only be called once despite multiple scans")
		mockRow.AssertExpectations(t)
	})
}

func TestTrackedRowErr(t *testing.T) {
	t.Run("successful err call", func(t *testing.T) {
		mockRow := &MockRow{}
		expectedErr := errors.New("row error")
		var finishErr error
		finishCallCount := 0
		finish := func(err error) {
			finishCallCount++
			finishErr = err
		}

		mockRow.On("Err").Return(expectedErr)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		err := trackedRow.Err()

		assert.Equal(t, expectedErr, err)
		assert.Equal(t, 1, finishCallCount, finishCallCountErrorMsg)
		assert.Equal(t, expectedErr, finishErr, "finish should receive the same error")
		mockRow.AssertExpectations(t)
	})

	t.Run("nil error from underlying row", func(t *testing.T) {
		mockRow := &MockRow{}
		var finishErr error
		finishCallCount := 0
		finish := func(err error) {
			finishCallCount++
			finishErr = err
		}

		mockRow.On("Err").Return(nil)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		err := trackedRow.Err()

		assert.NoError(t, err)
		assert.Equal(t, 1, finishCallCount, finishCallCountErrorMsg)
		assert.NoError(t, finishErr)
		mockRow.AssertExpectations(t)
	})
}

func TestTrackedRowConcurrency(t *testing.T) {
	t.Run("concurrent calls ensure finish called only once", func(t *testing.T) {
		mockRow := &MockRow{}
		finishCallCount := 0
		var finishMutex sync.Mutex
		finish := func(_ error) {
			finishMutex.Lock()
			finishCallCount++
			finishMutex.Unlock()
		}

		mockRow.On("Scan", mock.Anything).Return(nil)
		mockRow.On("Err").Return(nil)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		// Launch multiple concurrent operations
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			idx := i // Capture loop variable to avoid data race
			go func() {
				defer wg.Done()
				if idx%2 == 0 {
					var dest []any
					_ = trackedRow.Scan(dest...)
				} else {
					_ = trackedRow.Err()
				}
			}()
		}

		wg.Wait()

		finishMutex.Lock()
		finalCount := finishCallCount
		finishMutex.Unlock()

		assert.Equal(t, 1, finalCount, "finish should only be called once despite concurrent access")
		mockRow.AssertExpectations(t)
	})
}

func TestTrackedRowMixedOperations(t *testing.T) {
	t.Run("scan then err only calls finish once", func(t *testing.T) {
		mockRow := &MockRow{}
		finishCallCount := 0
		finish := func(_ error) {
			finishCallCount++
		}

		mockRow.On("Scan", mock.Anything).Return(nil)
		mockRow.On("Err").Return(nil)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		var dest []any
		_ = trackedRow.Scan(dest...)
		_ = trackedRow.Err()

		assert.Equal(t, 1, finishCallCount, "finish should only be called once")
		mockRow.AssertExpectations(t)
	})

	t.Run("err then scan only calls finish once", func(t *testing.T) {
		mockRow := &MockRow{}
		finishCallCount := 0
		finish := func(_ error) {
			finishCallCount++
		}

		mockRow.On("Err").Return(nil)
		mockRow.On("Scan", mock.Anything).Return(nil)

		trackedRow := Wrap(mockRow, finish).(*trackedRow)

		_ = trackedRow.Err()
		var dest []any
		_ = trackedRow.Scan(dest...)

		assert.Equal(t, 1, finishCallCount, "finish should only be called once")
		mockRow.AssertExpectations(t)
	})
}
