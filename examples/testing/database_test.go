package testing_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/testing/fixtures"
	"github.com/gaborage/go-bricks/testing/mocks"
)

const (
	usersInsertClause = "INSERT INTO users (name, email) VALUES (?, ?)"
	testUser          = "John Doe"
	testEmail         = "john@example.com"
)

// Example User struct for demonstration
type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Example UserService for demonstration
type UserService struct {
	db types.Interface
}

func NewUserService(db types.Interface) *UserService {
	return &UserService{db: db}
}

func (s *UserService) GetUser(ctx context.Context, id int64) (_ *User, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("query failed: %v", r)
		}
	}()

	row := s.db.QueryRow(ctx, "SELECT id, name, email FROM users WHERE id = ?", id)
	if row == nil {
		return nil, sql.ErrNoRows
	}

	user := &User{}
	err = row.Scan(&user.ID, &user.Name, &user.Email)
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (s *UserService) CreateUser(ctx context.Context, name, email string) (*User, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result, err := tx.Exec(ctx, usersInsertClause, name, email)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return &User{ID: id, Name: name, Email: email}, nil
}

func (s *UserService) CheckHealth(ctx context.Context) error {
	return s.db.Health(ctx)
}

func (s *UserService) GetDatabaseType() string {
	return s.db.DatabaseType()
}

// Test Examples

// TestUserService_GetUser_Success demonstrates basic mock usage
// NOTE: This is a simplified example showing mock setup patterns
// In real tests, you'd use more sophisticated row mocking or fixtures
func TestUserServiceGetUserSuccess(t *testing.T) {
	// Create a mock database using fixtures for simplicity
	mockDB := fixtures.NewHealthyDatabase()

	// Create service with mock
	service := NewUserService(mockDB)

	// Test a simple operation that we know will work
	err := service.CheckHealth(context.Background())
	assert.NoError(t, err)

	// For actual GetUser testing, you'd need proper row mocking
	// See the fixtures examples below for better approaches
}

// TestUserService_CreateUser_Transaction demonstrates transaction testing
func TestUserServiceCreateUserTransaction(t *testing.T) {
	mockDB := &mocks.MockDatabase{}
	mockTx := &mocks.MockTx{}

	// Expect transaction to be started
	mockDB.ExpectTransaction(mockTx, nil)

	// Expect operations within transaction
	result := fixtures.NewMockResult(1, 1) // lastInsertId=1, rowsAffected=1
	mockTx.On("Exec", mock.Anything, usersInsertClause, mock.Anything, mock.Anything).Return(result, nil)

	// The CreateUser method uses defer tx.Rollback(), so we need to expect that
	mockTx.ExpectRollback(nil)

	// And then expect commit to be called explicitly
	mockTx.ExpectCommit(nil)

	service := NewUserService(mockDB)
	user, err := service.CreateUser(context.Background(), testUser, testEmail)

	assert.NoError(t, err)
	assert.Equal(t, int64(1), user.ID)
	assert.Equal(t, testUser, user.Name)
	assert.Equal(t, testEmail, user.Email)

	mockDB.AssertExpectations(t)
	mockTx.AssertExpectations(t)
}

// TestUserService_CreateUser_TransactionFailure demonstrates transaction failure testing
func TestUserServiceCreateUserTransactionFailure(t *testing.T) {
	mockDB := &mocks.MockDatabase{}

	// Create a failing transaction
	commitErr := assert.AnError
	mockTx := fixtures.NewFailedTransaction(commitErr)

	mockDB.ExpectTransaction(mockTx, nil)

	// Still expect the exec to succeed, but commit will fail
	result := fixtures.NewMockResult(1, 1)
	mockTx.On("Exec", mock.Anything, usersInsertClause, mock.Anything, mock.Anything).Return(result, nil)

	service := NewUserService(mockDB)
	user, err := service.CreateUser(context.Background(), testUser, testEmail)

	assert.Error(t, err)
	assert.Nil(t, user)
	assert.Equal(t, commitErr, err)

	mockDB.AssertExpectations(t)
	mockTx.AssertExpectations(t)
}

// TestUserService_HealthCheck_Unhealthy demonstrates health check failure testing
func TestUserServiceHealthCheckUnhealthy(t *testing.T) {
	// Use a pre-configured failing database fixture
	mockDB := fixtures.NewFailingDatabase(sql.ErrConnDone)

	service := NewUserService(mockDB)
	err := service.CheckHealth(context.Background())

	assert.Error(t, err)
	assert.Equal(t, sql.ErrConnDone, err)
}

// TestUserService_DatabaseTypes demonstrates database-specific testing
func TestUserServiceDatabaseTypes(t *testing.T) {
	tests := []struct {
		name         string
		mockDB       *mocks.MockDatabase
		expectedType string
	}{
		{
			name:         "PostgreSQL",
			mockDB:       fixtures.NewPostgreSQLDatabase(),
			expectedType: "postgres",
		},
		{
			name:         "Oracle",
			mockDB:       fixtures.NewOracleDatabase(),
			expectedType: "oracle",
		},
		{
			name:         "MongoDB",
			mockDB:       fixtures.NewMongoDatabase(),
			expectedType: "mongodb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewUserService(tt.mockDB)
			dbType := service.GetDatabaseType()
			assert.Equal(t, tt.expectedType, dbType)
		})
	}
}

// TestUserService_ReadOnlyDatabase demonstrates read-only database testing
func TestUserServiceReadOnlyDatabase(t *testing.T) {
	mockDB := fixtures.NewReadOnlyDatabase()

	service := NewUserService(mockDB)

	// Health check should work (it's a read operation)
	err := service.CheckHealth(context.Background())
	assert.NoError(t, err)

	// Create user should fail (it's a write operation)
	user, err := service.CreateUser(context.Background(), "John", testEmail)
	assert.Error(t, err)
	assert.Nil(t, user)
	assert.Contains(t, err.Error(), "read-only")
}

// TestUserService_ErrorScenarios demonstrates various error scenario testing
func TestUserServiceErrorScenarios(t *testing.T) {
	t.Run("health_check_failure", func(t *testing.T) {
		// Use fixture for clean, elegant error testing
		mockDB := fixtures.NewFailingDatabase(sql.ErrConnDone)
		service := NewUserService(mockDB)

		err := service.CheckHealth(context.Background())
		assert.Error(t, err)
		assert.Equal(t, sql.ErrConnDone, err)
	})

	t.Run("transaction_begin_failure", func(t *testing.T) {
		mockDB := &mocks.MockDatabase{}
		mockDB.ExpectTransaction(nil, sql.ErrConnDone)

		service := NewUserService(mockDB)
		user, err := service.CreateUser(context.Background(), "John", testEmail)

		assert.Error(t, err)
		assert.Nil(t, user)
		mockDB.AssertExpectations(t)
	})
}
