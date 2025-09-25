// Package user demonstrates a go-bricks module with enhanced OpenAPI metadata.
// This module shows both backward-compatible usage and new features for documentation generation.
package user

import (
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

const (
	getUserRoute  = "/users/:id"
	getUsersRoute = "/users"
	testUserName  = "John Doe"
	testEmail     = "john.doe@example.com"
)

// Module implements the go-bricks Module interface and optionally the Describer interface
type Module struct {
	deps *app.ModuleDeps
}

// Name returns the module name
func (m *Module) Name() string {
	return "user"
}

// Init initializes the module with dependencies
func (m *Module) Init(deps *app.ModuleDeps) error {
	m.deps = deps
	return nil
}

// RegisterRoutes demonstrates both old and new registration styles
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Backward compatible - no options (still works)
	server.GET(hr, r, getUserRoute, m.getUser)

	// Enhanced with metadata for OpenAPI generation
	server.POST(hr, r, getUsersRoute, m.createUser,
		server.WithModule("user"),
		server.WithTags("users", "management"),
		server.WithSummary("Create a new user"),
		server.WithDescription("Creates a new user account with the provided information"))

	server.PUT(hr, r, getUserRoute, m.updateUser,
		server.WithModule("user"),
		server.WithTags("users", "management"),
		server.WithSummary("Update user information"),
		server.WithDescription("Updates an existing user's information"))

	server.DELETE(hr, r, getUserRoute, m.deleteUser,
		server.WithModule("user"),
		server.WithTags("users", "management"),
		server.WithSummary("Delete a user"),
		server.WithDescription("Permanently deletes a user account"))

	server.GET(hr, r, getUsersRoute, m.listUsers,
		server.WithModule("user"),
		server.WithTags("users", "listing"),
		server.WithSummary("List users"),
		server.WithDescription("Retrieves a paginated list of users with optional filtering"))
}

// DeclareMessaging declares messaging infrastructure for this module
func (m *Module) DeclareMessaging(_ *messaging.Declarations) {
	// No messaging in this example
}

// Shutdown cleans up module resources
func (m *Module) Shutdown() error {
	return nil
}

// DescribeModule implements the optional Describer interface
func (m *Module) DescribeModule() app.ModuleDescriptor {
	return app.ModuleDescriptor{
		Name:        "user",
		Version:     "1.0.0",
		Description: "User management operations including CRUD operations and user listings",
		Tags:        []string{"users", "management", "authentication"},
		BasePath:    getUsersRoute,
	}
}

// DescribeRoutes implements the optional Describer interface
func (m *Module) DescribeRoutes() []server.RouteDescriptor {
	// Return empty slice for now - this will be populated by the CLI tool
	// during static analysis in Phase 1
	return []server.RouteDescriptor{}
}

// Request/Response types with validation tags for OpenAPI generation

// GetUserReq represents a request to get a user by ID
type GetUserReq struct {
	ID int `param:"id" validate:"required,min=1" doc:"User ID" example:"123"`
}

// CreateUserReq represents a request to create a new user
type CreateUserReq struct {
	Name     string            `json:"name" validate:"required,min=2,max=100" doc:"User's full name" example:"testUserName"`
	Email    string            `json:"email" validate:"required,email" doc:"User's email address" example:"testEmail"`
	Age      *int              `json:"age,omitempty" validate:"omitempty,min=13,max=120" doc:"User's age (optional)" example:"30"`
	Role     string            `json:"role" validate:"oneof=admin user guest" doc:"User role" example:"user"`
	Active   bool              `json:"active" doc:"Whether the user account is active" example:"true"`
	Metadata map[string]string `json:"metadata,omitempty" doc:"Additional user metadata"`
}

// UpdateUserReq represents a request to update user information
type UpdateUserReq struct {
	ID     int    `param:"id" validate:"required,min=1" doc:"User ID" example:"123"`
	Name   string `json:"name,omitempty" validate:"omitempty,min=2,max=100" doc:"User's full name" example:"testUserName"`
	Email  string `json:"email,omitempty" validate:"omitempty,email" doc:"User's email address" example:"testEmail"`
	Age    *int   `json:"age,omitempty" validate:"omitempty,min=13,max=120" doc:"User's age" example:"30"`
	Role   string `json:"role,omitempty" validate:"omitempty,oneof=admin user guest" doc:"User role" example:"user"`
	Active *bool  `json:"active,omitempty" doc:"Whether the user account is active" example:"true"`
}

// DeleteUserReq represents a request to delete a user
type DeleteUserReq struct {
	ID    int  `param:"id" validate:"required,min=1" doc:"User ID to delete" example:"123"`
	Force bool `query:"force" doc:"Force deletion even if user has associated data" example:"false"`
}

// ListUsersReq represents a request to list users with pagination and filtering
type ListUsersReq struct {
	Page     int      `query:"page" validate:"omitempty,min=1" doc:"Page number (1-based)" example:"1"`
	PageSize int      `query:"page_size" validate:"omitempty,min=1,max=100" doc:"Number of users per page" example:"20"`
	Search   string   `query:"search" doc:"Search term for name or email" example:"john"`
	Roles    []string `query:"roles" doc:"Filter by user roles (repeat query param: roles=user&roles=admin)" example:"user,admin"`
	Active   *bool    `query:"active" doc:"Filter by active status" example:"true"`
}

// Response represents a user in API responses
type Response struct {
	ID        int               `json:"id" doc:"Unique user identifier" example:"123"`
	Name      string            `json:"name" doc:"User's full name" example:"testUserName"`
	Email     string            `json:"email" doc:"User's email address" example:"testEmail"`
	Age       *int              `json:"age,omitempty" doc:"User's age" example:"30"`
	Role      string            `json:"role" doc:"User role" example:"user"`
	Active    bool              `json:"active" doc:"Whether the user account is active" example:"true"`
	CreatedAt time.Time         `json:"created_at" doc:"Account creation timestamp" example:"2023-01-15T10:30:00Z"`
	UpdatedAt time.Time         `json:"updated_at" doc:"Last update timestamp" example:"2023-01-15T14:45:00Z"`
	Metadata  map[string]string `json:"metadata,omitempty" doc:"Additional user metadata"`
}

// ListResponse represents a paginated list of users
type ListResponse struct {
	Users []Response `json:"users" doc:"List of users for the current page"`
	Total int        `json:"total" doc:"Total number of users matching the filter" example:"150"`
	Page  int        `json:"page" doc:"Current page number" example:"1"`
	Pages int        `json:"pages" doc:"Total number of pages" example:"8"`
}

// Handler implementations

// getUser retrieves a user by ID
func (m *Module) getUser(req GetUserReq, _ server.HandlerContext) (Response, server.IAPIError) {
	// Simulate database lookup
	if req.ID <= 0 {
		return Response{}, server.NewBadRequestError("Invalid user ID")
	}

	// Mock user data
	user := Response{
		ID:        req.ID,
		Name:      testUserName,
		Email:     testEmail,
		Age:       intPtr(30),
		Role:      "user",
		Active:    true,
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata:  map[string]string{"department": "engineering"},
	}

	return user, nil
}

// createUser creates a new user
func (m *Module) createUser(req CreateUserReq, _ server.HandlerContext) (server.Result[Response], server.IAPIError) {
	// Simulate user creation
	user := Response{
		ID:        123, // Would be generated by database
		Name:      req.Name,
		Email:     req.Email,
		Age:       req.Age,
		Role:      req.Role,
		Active:    req.Active,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  req.Metadata,
	}

	// Return 201 Created
	return server.Created(user), nil
}

// updateUser updates an existing user
func (m *Module) updateUser(req UpdateUserReq, _ server.HandlerContext) (Response, server.IAPIError) {
	// Simulate user update
	user := Response{
		ID:        req.ID,
		Name:      testUserName, // Would come from database
		Email:     testEmail,
		Age:       intPtr(30),
		Role:      "user",
		Active:    true,
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
	if req.Age != nil {
		user.Age = req.Age
	}
	if req.Role != "" {
		user.Role = req.Role
	}
	if req.Active != nil {
		user.Active = *req.Active
	}

	return user, nil
}

// deleteUser deletes a user
func (m *Module) deleteUser(req DeleteUserReq, _ server.HandlerContext) (server.NoContentResult, server.IAPIError) {
	// Simulate user deletion
	if req.ID <= 0 {
		return server.NoContentResult{}, server.NewBadRequestError("Invalid user ID")
	}

	// Check if user exists (mock)
	// In real implementation, check database

	// Return 204 No Content
	return server.NoContent(), nil
}

// listUsers retrieves a paginated list of users
func (m *Module) listUsers(req ListUsersReq, _ server.HandlerContext) (ListResponse, server.IAPIError) {
	// Set defaults
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	// Mock data
	users := []Response{
		{
			ID:        1,
			Name:      testUserName,
			Email:     testEmail,
			Age:       intPtr(30),
			Role:      "user",
			Active:    true,
			CreatedAt: time.Now().Add(-24 * time.Hour),
			UpdatedAt: time.Now(),
		},
		{
			ID:        2,
			Name:      "Jane Smith",
			Email:     "jane.smith@example.com",
			Age:       intPtr(28),
			Role:      "admin",
			Active:    true,
			CreatedAt: time.Now().Add(-48 * time.Hour),
			UpdatedAt: time.Now(),
		},
	}

	total := 150
	pages := (total + req.PageSize - 1) / req.PageSize

	return ListResponse{
		Users: users,
		Total: total,
		Page:  req.Page,
		Pages: pages,
	}, nil
}

// Helper function
func intPtr(i int) *int {
	return &i
}

// Compile-time check that Module implements required interfaces
var _ app.Module = (*Module)(nil)
var _ app.Describer = (*Module)(nil)
