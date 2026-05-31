package legacy

import (
	"net/http"

	"github.com/gaborage/go-bricks/server"
)

// Handler holds the legacy storefront's HTTP handlers.
type Handler struct{}

// LegacyUser is the raw response payload. Routes registered WithRawResponse emit
// this schema directly (no data/meta envelope) for Strangler-Fig compatibility
// with a pre-existing JSON shape.
type LegacyUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GetLegacyReq identifies a legacy user by path parameter.
type GetLegacyReq struct {
	ID int64 `param:"id" validate:"required"`
}

// EnqueueReq is a validated request body for the async job endpoint.
type EnqueueReq struct {
	JobType string `json:"jobType" validate:"required"`
}

// JobAck is the acknowledgement returned when a job is accepted for async work.
type JobAck struct {
	JobID string `json:"jobId"`
}

// getLegacyUser returns the bare legacy shape (no envelope). Registered
// WithRawResponse, so the generator emits a 200 whose schema is a direct $ref to
// LegacyUser and whose error responses use the minimal RawErrorResponse.
func (h *Handler) getLegacyUser(req GetLegacyReq, ctx server.HandlerContext) (server.Result[LegacyUser], server.IAPIError) {
	return server.NewResult(http.StatusOK, LegacyUser{}), nil
}

// enqueueJob accepts a job for asynchronous processing. It returns
// server.Accepted (202) and is registered WithHandlerName to override the
// generated operationId.
func (h *Handler) enqueueJob(req EnqueueReq, ctx server.HandlerContext) (server.Result[JobAck], server.IAPIError) {
	return server.Accepted(JobAck{}), nil
}
