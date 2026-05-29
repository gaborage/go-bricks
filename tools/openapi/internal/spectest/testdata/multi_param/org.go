// Package org manages organization projects.
package org

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module is the org module.
type Module struct{}

func (m *Module) Name() string                    { return "org" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error                 { return nil }

// GetProjectReq identifies a project within an organization, exercising
// multiple path parameters in a single route template.
type GetProjectReq struct {
	OrgID     string `param:"orgID" validate:"required"`
	ProjectID string `param:"projectID" validate:"required"`
}

// Project is an organization project.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RegisterRoutes registers the module's HTTP routes.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/orgs/:orgID/projects/:projectID", m.getProject, server.WithTags("org"))
}

func (m *Module) getProject(req GetProjectReq, ctx server.HandlerContext) (server.Result[Project], server.IAPIError) {
	return server.OK(Project{}), nil
}
