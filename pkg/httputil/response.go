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

// InternalError sends a 500 response with code=-1. The msg is sent verbatim to
// the client.
func InternalError(c *app.RequestContext, msg string) {
	c.JSON(http.StatusInternalServerError, ErrResponse{Code: -1, Msg: msg})
}

// InternalErrorLog logs the underlying err with op context and returns the full
// error to the client. Per the project convention (AGENTS.md: "错误信息就要全量
// 给用户显示，不要隐藏任何错误信息") quartet is a single-user local/sandbox tool,
// so surfacing the real error — paths included — beats forcing the user to dig
// through backend logs.
func InternalErrorLog(ctx context.Context, c *app.RequestContext, op string, err error) {
	if op == "" {
		op = "request"
	}
	logger.Errorf(ctx, "[%s] %v", op, err)
	c.JSON(http.StatusInternalServerError, ErrResponse{Code: -1, Msg: err.Error()})
}

// ErrorMapping maps sentinel errors to HTTP status codes.
type ErrorMapping struct {
	Err    error
	Status int
}

// MapError checks err against a list of mappings and sends the appropriate
// HTTP response. Whether or not a mapping matches, the full error message is
// returned to the client (AGENTS.md: errors must be shown in full); a matched
// mapping only changes the HTTP status code.
func MapError(c *app.RequestContext, err error, mappings []ErrorMapping) {
	for _, m := range mappings {
		if errors.Is(err, m.Err) {
			c.JSON(m.Status, ErrResponse{Code: -1, Msg: err.Error()})
			return
		}
	}
	InternalError(c, err.Error())
}
