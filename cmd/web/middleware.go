package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/compress"
	"github.com/fanlv/quartet/cmd/web/handler"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/consts"
)

const minJSONGzipBytes = 1024

// jsonGzipMiddleware compresses buffered JSON responses when the client
// advertises gzip support. Streaming responses (especially SSE) and file
// downloads are deliberately untouched: only application/json bodies pass the
// content-type gate, and a body stream is always skipped so flushing semantics
// cannot change.
func jsonGzipMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		accept := strings.ToLower(string(c.Request.Header.Peek("Accept")))
		if strings.Contains(accept, "text/event-stream") ||
			!acceptsGzip(string(c.Request.Header.Peek("Accept-Encoding"))) {
			c.Next(ctx)
			return
		}

		c.Next(ctx)

		if c.Response.IsBodyStream() || len(c.Response.Header.ContentEncoding()) > 0 {
			return
		}
		body := c.Response.Body()
		if len(body) < minJSONGzipBytes {
			return
		}
		contentType := strings.ToLower(string(c.Response.Header.ContentType()))
		if strings.HasPrefix(contentType, "text/event-stream") {
			return
		}
		if !strings.HasPrefix(contentType, "application/json") && !strings.Contains(contentType, "+json") {
			return
		}

		gzipped := compress.AppendGzipBytesLevel(nil, body, compress.CompressDefaultCompression)
		c.Response.Header.SetContentEncoding("gzip")
		appendVary(&c.Response.Header, "Accept-Encoding")
		c.Response.SetBodyStream(bytes.NewReader(gzipped), len(gzipped))
	}
}

func acceptsGzip(value string) bool {
	wildcardQuality := -1.0
	for _, part := range strings.Split(value, ",") {
		fields := strings.Split(part, ";")
		encoding := strings.TrimSpace(fields[0])
		if !strings.EqualFold(encoding, "gzip") && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, param := range fields[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if len(keyValue) != 2 || !strings.EqualFold(strings.TrimSpace(keyValue[0]), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(keyValue[1]), 64)
			if err != nil {
				quality = 0
			} else {
				quality = parsed
			}
			break
		}
		if strings.EqualFold(encoding, "gzip") {
			return quality > 0
		}
		wildcardQuality = quality
	}
	return wildcardQuality > 0
}

func appendVary(header interface {
	Get(string) string
	Set(string, string)
}, value string) {
	existing := header.Get("Vary")
	for _, item := range strings.Split(existing, ",") {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return
		}
	}
	if existing == "" {
		header.Set("Vary", value)
		return
	}
	header.Set("Vary", existing+", "+value)
}

func agentAuthMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		token := string(c.GetHeader(consts.HeaderAgentAuth))
		// Fallback: allow token via query parameter (for browser-native requests like <img src>)
		if token == "" {
			token = string(c.Query("token"))
		}
		if !handler.CheckAgentAuth(token) {
			// Several common authed routes are in the access-log skip list,
			// so this line is often the only trace of the rejection — keep
			// enough context to triage without tailing handler logs.
			//
			// Split by token shape so the level matches the signal:
			//   empty token  — the client simply has no token configured
			//                  yet (first-run UI, post-rotation reload).
			//                  High-frequency and expected; log at Info.
			//   non-empty    — token was supplied but did not match. Could
			//                  be misconfiguration or a probing client;
			//                  keep at Warn so it surfaces.
			logFn := logger.Warnf
			if token == "" {
				logFn = logger.Infof
			}
			logFn(ctx, "[auth] reject %s %s remote=%s tokenPrefix=%s tokenLen=%d",
				c.Method(), c.Request.URI().Path(), c.ClientIP(),
				tokenPrefix(token), len(token))
			c.JSON(http.StatusForbidden, map[string]string{"error": "permission denied"})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

// tokenPrefix returns a short, non-reversible hint of the supplied token for
// triage logging. Empty token reports as "<empty>"; non-empty tokens are
// truncated to 4 chars so the full secret never reaches log files.
func tokenPrefix(token string) string {
	if token == "" {
		return "<empty>"
	}
	if len(token) <= 4 {
		return token + "***"
	}
	return token[:4] + "***"
}

type httpLogConfig struct {
	skipExactPaths map[string]struct{}
	skipConfigs    []*skipConfig
}
type skipConfig struct {
	prefix string
	suffix string
}

var httpLogCfg = httpLogConfig{
	skipExactPaths: map[string]struct{}{
		"/api/v1/config/eino/model/list": {},
		"/api/v1/agent/list":             {},
		"/api/v1/job/list":               {},
		"/api/v1/serve-file":             {},
		"/api/v1/file-exists":            {},
		"/api/v1/schedule/list":          {},
		"/api/v1/logs/list":              {},
		"/api/v1/logs/frontend":          {},
		"/api/v1/stats/usage":            {},
	},
	skipConfigs: []*skipConfig{
		{prefix: "/api/v1/sessions/", suffix: "/messages"},
	},
}

func (cfg httpLogConfig) shouldSkip(reqPath string) bool {
	if _, ok := cfg.skipExactPaths[reqPath]; ok {
		return true
	}

	for _, sc := range cfg.skipConfigs {
		if sc == nil || sc.prefix == "" || sc.suffix == "" {
			continue
		}
		if strings.HasPrefix(reqPath, sc.prefix) && strings.HasSuffix(reqPath, sc.suffix) {
			return true
		}
	}

	return false
}

// logHTTPBody is evaluated once at process start so toggling the env var mid-run
// has no effect. The default is OFF because body logging is expensive under
// load (extra []byte -> string allocations on every request) and risks
// persisting bearer tokens, chat content, or other sensitive material to log
// files. Set QUARTET_LOG_HTTP_BODY=1 in development to re-enable.
var logHTTPBody = func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(consts.EnvKeyLogHTTPBody)))
	return v == "1" || v == "true" || v == "yes"
}()

func loggerMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		reqPath := string(c.Request.URI().Path())
		if httpLogCfg.shouldSkip(reqPath) {
			c.Next(ctx)
			return
		}

		start := time.Now()
		c.Next(ctx)
		elapsed := time.Since(start)
		status := c.Response.StatusCode()

		// Log level by class:
		//   5xx — server errors, the only 4xx/5xx class that is always worth
		//         attention in a local, single-user tool; keep at Warn.
		//   4xx — client-side / business outcomes (e.g. "file not found",
		//         "bad request", "forbidden"). These occur on normal user
		//         flows (the UI probes read-file for missing paths, auth
		//         retries after token rotation, etc.) and become noise at
		//         Warn level, so log at Info.
		//   3xx — redirects, Info.
		//   2xx — Debug (quiet by default).
		logFn := logger.Debugf
		switch {
		case status >= 500:
			logFn = logger.Warnf
		case status >= 300:
			logFn = logger.Infof
		}

		// Surface the response-body error message for 4xx/5xx so the access
		// log alone gives enough context to triage without tailing handler
		// logs. Handlers in this project uniformly emit ErrResponse
		// ({"code":-1,"msg":"..."}) and auth middlewares emit
		// {"error":"..."} — both shapes are covered by extractErrMsg.
		var errMsg string
		if status >= 400 {
			errMsg = extractErrMsg(c.Response.Body())
		}

		if !logHTTPBody {
			if errMsg != "" {
				logFn(ctx, "[HTTP] %s %s %d %v msg=%q",
					c.Method(), reqPath, status, elapsed, errMsg)
			} else {
				logFn(ctx, "[HTTP] %s %s %d %v",
					c.Method(), reqPath, status, elapsed)
			}
			return
		}

		reqBody := truncate(string(c.Request.Body()), 500)
		respBody := truncate(string(c.Response.Body()), 500)
		if respBody == "" {
			ct := strings.ToLower(string(c.Response.Header.ContentType()))
			if strings.Contains(ct, "text/event-stream") {
				respBody = "[SSE stream]"
			}
		}
		if reqBody == "" {
			logFn(ctx, "[HTTP] %s %s %d %v\nresp=%s",
				c.Method(), reqPath, status, elapsed,
				respBody)
			return
		}
		logFn(ctx, "[HTTP] %s %s %d %v\nreq=%s\nresp=%s",
			c.Method(), reqPath, status, elapsed,
			reqBody, respBody)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// extractErrMsg pulls a short human-readable error message from a JSON
// response body. Handlers in this project return either the httputil
// ErrResponse envelope ({"code":-1,"msg":"..."}) or an ad-hoc
// {"error":"..."} map from auth/share middlewares. Anything else (binary
// payloads, SSE, empty bodies) returns "".
func extractErrMsg(body []byte) string {
	if len(body) == 0 || len(body) > 4096 {
		return ""
	}
	if body[0] != '{' {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	for _, key := range []string{"msg", "error", "message"} {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			continue
		}
		return truncate(s, 200)
	}
	return ""
}

// shareTokenMiddleware validates that the request includes a valid shareToken
// for the requested job. The jobId is read from the path param or query param.
func shareTokenMiddleware(jobSvc job.Service) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		shareToken := string(c.Query("shareToken"))
		if shareToken == "" {
			c.JSON(http.StatusForbidden, map[string]string{"error": "shareToken is required"})
			c.Abort()
			return
		}

		// jobId can come from path param (for /job/:jobId routes) or query param (for session/serve-file routes)
		jobID := c.Param("jobId")
		if jobID == "" {
			jobID = string(c.Query("jobId"))
		}
		if jobID == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "jobId is required"})
			c.Abort()
			return
		}

		j, ok := jobSvc.Get(jobID)
		if !ok {
			c.JSON(http.StatusNotFound, map[string]string{"error": "job not found"})
			c.Abort()
			return
		}

		if j.ShareToken == "" || subtle.ConstantTimeCompare([]byte(j.ShareToken), []byte(shareToken)) != 1 {
			c.JSON(http.StatusForbidden, map[string]string{"error": "invalid share token"})
			c.Abort()
			return
		}

		// Store job in context for downstream handlers
		c.Set("publicJob", j)
		c.Next(ctx)
	}
}
