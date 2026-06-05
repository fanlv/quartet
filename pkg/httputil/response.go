package httputil

import (
	"context"
	"errors"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/logger"
)

// ErrResponse is the standard error response envelope.
// Uses "msg" field to match existing frontend convention.
type ErrResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// BadRequest sends a 400 response with code=-1.
func BadRequest(c *app.RequestContext, msg string) {
	c.JSON(http.StatusBadRequest, ErrResponse{Code: -1, Msg: msg})
}

// NotFound sends a 404 response with code=-1.
func NotFound(c *app.RequestContext, msg string) {
	c.JSON(http.StatusNotFound, ErrResponse{Code: -1, Msg: msg})
}

// Conflict sends a 409 response with code=-1.
func Conflict(c *app.RequestContext, msg string) {
	c.JSON(http.StatusConflict, ErrResponse{Code: -1, Msg: msg})
}

// InternalError sends a 500 response with code=-1.
//
// Callers should prefer InternalErrorLog for errors that might contain
// filesystem paths, stack traces, or other internal detail — the argument to
// this function is sent verbatim to the client.
func InternalError(c *app.RequestContext, msg string) {
	c.JSON(http.StatusInternalServerError, ErrResponse{Code: -1, Msg: msg})
}

// InternalErrorLog logs the underlying err with op context and returns a
// generic 500 to the client. Use this instead of InternalError(c, err.Error())
// so we don't leak paths or system internals.
func InternalErrorLog(ctx context.Context, c *app.RequestContext, op string, err error) {
	if op == "" {
		op = "request"
	}
	logger.Errorf(ctx, "[%s] %v", op, err)
	c.JSON(http.StatusInternalServerError, ErrResponse{Code: -1, Msg: "internal error"})
}

// ErrorMapping maps sentinel errors to HTTP status codes.
type ErrorMapping struct {
	Err    error
	Status int
}

// MapError checks err against a list of mappings and sends the appropriate
// HTTP response. If no mapping matches, it sends a generic 500 Internal Server Error
// to avoid leaking internal details (file paths, stack traces, etc.) to clients.
func MapError(c *app.RequestContext, err error, mappings []ErrorMapping) {
	for _, m := range mappings {
		if errors.Is(err, m.Err) {
			c.JSON(m.Status, ErrResponse{Code: -1, Msg: err.Error()})
			return
		}
	}
	InternalError(c, "internal error")
}
