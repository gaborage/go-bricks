// Package main provides example implementations demonstrating the enhanced handler system.
package main

import (
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// UserModule demonstrates the new enhanced handler system.
type UserModule struct {
	deps *app.ModuleDeps
}

// NewUserModule creates a new user module instance.
func NewUserModule() *UserModule {
	return &UserModule{}
}

// Name returns the module name.
func (m *UserModule) Name() string {
	return "user"
}

// Init initializes the module with dependencies.
func (m *UserModule) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

// RegisterRoutes registers module routes using the enhanced handler system.
func (m *UserModule) RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo) {
	// Register enhanced handlers with type safety
	server.GET(hr, e, "/users", m.getUsers)
	server.GET(hr, e, "/users/:id", m.getUserByID)
	server.POST(hr, e, "/users", m.createUser)
	server.PUT(hr, e, "/users/:id", m.updateUser)
	server.DELETE(hr, e, "/users/:id", m.deleteUser)
}

// RegisterMessaging registers messaging handlers.
func (m *UserModule) RegisterMessaging(_ *messaging.Registry) {
	// No messaging for this example
}

// Shutdown cleans up module resources.
func (m *UserModule) Shutdown() error {
	return nil
}

// Request/Response types for type-safe handlers

// GetUsersRequest represents the request for getting users with pagination.
type GetUsersRequest struct {
	Page    int    `query:"page" validate:"min=1"`
	PerPage int    `query:"per_page" validate:"min=1,max=100"`
	Search  string `query:"search"`
}

// GetUsersResponse represents the response for getting users.
type GetUsersResponse struct {
	Users      []User     `json:"users"`
	Pagination Pagination `json:"pagination"`
}

// GetUserByIDRequest represents the request for getting a user by ID.
type GetUserByIDRequest struct {
	ID int `param:"id" validate:"min=1"`
}

// CreateUserRequest represents the request for creating a new user.
type CreateUserRequest struct {
	Name     string `json:"name" validate:"required,min=2,max=100"`
	Email    string `json:"email" validate:"required,email"`
	Age      int    `json:"age" validate:"min=13,max=120"`
	IsActive bool   `json:"is_active"`
}

// UpdateUserRequest represents the request for updating a user.
type UpdateUserRequest struct {
	ID       int    `param:"id" validate:"min=1"`
	Name     string `json:"name" validate:"omitempty,min=2,max=100"`
	Email    string `json:"email" validate:"omitempty,email"`
	Age      int    `json:"age" validate:"omitempty,min=13,max=120"`
	IsActive *bool  `json:"is_active"`
}

// DeleteUserRequest represents the request for deleting a user.
type DeleteUserRequest struct {
	ID int `param:"id" validate:"min=1"`
}

// User represents a user entity.
type User struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Age       int       `json:"age"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Pagination represents pagination metadata.
type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// Business logic handlers

// getUsers handles GET /users with type-safe request/response.
func (m *UserModule) getUsers(req GetUsersRequest, _ server.HandlerContext) (GetUsersResponse, server.IAPIError) {
	// Set defaults
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PerPage == 0 {
		req.PerPage = 10
	}

	m.deps.Logger.Info().
		Int("page", req.Page).
		Int("per_page", req.PerPage).
		Str("search", req.Search).
		Msg("Fetching users")

	// Simulate database query
	users := []User{
		{
			ID:        1,
			Name:      "John Doe",
			Email:     "john@example.com",
			Age:       30,
			IsActive:  true,
			CreatedAt: time.Now().Add(-24 * time.Hour),
			UpdatedAt: time.Now().Add(-1 * time.Hour),
		},
		{
			ID:        2,
			Name:      "Jane Smith",
			Email:     "jane@example.com",
			Age:       25,
			IsActive:  true,
			CreatedAt: time.Now().Add(-48 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		},
	}

	// Apply search filter if provided
	if req.Search != "" {
		var filteredUsers []User
		for _, user := range users {
			if containsIgnoreCase(user.Name, req.Search) || containsIgnoreCase(user.Email, req.Search) {
				filteredUsers = append(filteredUsers, user)
			}
		}
		users = filteredUsers
	}

	total := len(users)
	totalPages := (total + req.PerPage - 1) / req.PerPage

	// Apply pagination
	start := (req.Page - 1) * req.PerPage
	end := start + req.PerPage
	if start >= total {
		users = []User{}
	} else {
		if end > total {
			end = total
		}
		users = users[start:end]
	}

	return GetUsersResponse{
		Users: users,
		Pagination: Pagination{
			Page:       req.Page,
			PerPage:    req.PerPage,
			Total:      total,
			TotalPages: totalPages,
		},
	}, nil
}

// getUserByID handles GET /users/:id with type-safe request/response.
func (m *UserModule) getUserByID(req GetUserByIDRequest, _ server.HandlerContext) (User, server.IAPIError) {
	m.deps.Logger.Info().
		Int("user_id", req.ID).
		Msg("Fetching user by ID")

	// Simulate database query
	if req.ID == 1 {
		return User{
			ID:        1,
			Name:      "John Doe",
			Email:     "john@example.com",
			Age:       30,
			IsActive:  true,
			CreatedAt: time.Now().Add(-24 * time.Hour),
			UpdatedAt: time.Now().Add(-1 * time.Hour),
		}, nil
	}

	if req.ID == 2 {
		return User{
			ID:        2,
			Name:      "Jane Smith",
			Email:     "jane@example.com",
			Age:       25,
			IsActive:  true,
			CreatedAt: time.Now().Add(-48 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}, nil
	}

	return User{}, server.NewNotFoundError("User")
}

// createUser handles POST /users with type-safe request/response.
func (m *UserModule) createUser(req CreateUserRequest, _ server.HandlerContext) (server.Result[User], server.IAPIError) {
	m.deps.Logger.Info().
		Str("name", req.Name).
		Str("email", req.Email).
		Int("age", req.Age).
		Msg("Creating new user")

		// Simulate email uniqueness check
	if req.Email == "john@example.com" || req.Email == "jane@example.com" {
		return server.Result[User]{}, server.NewConflictError("User with this email already exists")
	}

	// Simulate database insertion
	user := User{
		ID:        99, // Simulated auto-generated ID
		Name:      req.Name,
		Email:     req.Email,
		Age:       req.Age,
		IsActive:  req.IsActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	return server.Created(user), nil
}

// updateUser handles PUT /users/:id with type-safe request/response.
func (m *UserModule) updateUser(req UpdateUserRequest, _ server.HandlerContext) (server.Result[User], server.IAPIError) {
	m.deps.Logger.Info().
		Int("user_id", req.ID).
		Msg("Updating user")

		// Simulate user existence check
	if req.ID != 1 && req.ID != 2 {
		return server.Result[User]{}, server.NewNotFoundError("User")
	}

	// Simulate email uniqueness check if email is being updated
	if req.Email != "" {
		if (req.Email == "john@example.com" && req.ID != 1) ||
			(req.Email == "jane@example.com" && req.ID != 2) {
			return server.Result[User]{}, server.NewConflictError("User with this email already exists")
		}
	}

	// Simulate database update
	user := User{
		ID:        req.ID,
		Name:      "Updated Name",
		Email:     "updated@example.com",
		Age:       30,
		IsActive:  true,
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
	}

	// Apply updates
	if req.Name != "" {
		user.Name = req.Name
	}
	if req.Email != "" {
		user.Email = req.Email
	}
	if req.Age != 0 {
		user.Age = req.Age
	}
	if req.IsActive != nil {
		user.IsActive = *req.IsActive
	}

	return server.Accepted(user), nil
}

// deleteUser handles DELETE /users/:id with type-safe request/response.
func (m *UserModule) deleteUser(req DeleteUserRequest, _ server.HandlerContext) (server.NoContentResult, server.IAPIError) {
	m.deps.Logger.Info().
		Int("user_id", req.ID).
		Msg("Deleting user")

		// Simulate user existence check
	if req.ID != 1 && req.ID != 2 {
		return server.NoContentResult{}, server.NewNotFoundError("User")
	}

	// Simulate business rule: cannot delete active users
	if req.ID == 1 {
		return server.NoContentResult{}, server.NewBusinessLogicError("CANNOT_DELETE_ACTIVE_USER", "Cannot delete active user")
	}

	// Simulate database deletion
	return server.NoContent(), nil
}

// Helper functions

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			substr == "" ||
			strings.Contains(strings.ToLower(s), strings.ToLower(substr)))
}
