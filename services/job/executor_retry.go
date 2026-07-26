package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
)

const (
	runErrorCodeInternal  = "INTERNAL"
	runErrorCodeTimeout   = "TIMEOUT"
	runErrorCodeNetwork   = "NETWORK"
	runErrorCodeRateLimit = "RATE_LIMIT"
	runErrorCodeShell     = "SHELL"
	runErrorCodePanic     = "PANIC"
)

type runPanicError struct {
	value any
}

func newRunPanicError(value any) error {
	return &runPanicError{value: value}
}

func (e *runPanicError) Error() string {
	if e == nil {
		return "job panicked: <nil>"
	}
	return "job panicked: " + fmt.Sprint(e.value)
}

// classifyRunErrorCode returns the stable protocol code attached to RUN_ERROR.
// Prefer structured/wrapped signals and fall back to upstream-message
// heuristics so logs and frontend-visible codes stay consistent.
func classifyRunErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return runErrorCodeTimeout
	}
	var panicErr *runPanicError
	if errors.As(err, &panicErr) {
		return runErrorCodePanic
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return runErrorCodeShell
	}
	if isRateLimitError(err) {
		return runErrorCodeRateLimit
	}
	if isTransientNetworkError(err) {
		return runErrorCodeNetwork
	}
	return runErrorCodeInternal
}

// isTransientNetworkError returns true if the error looks like a temporary
// network issue (HTTP/2 stream resets, connection resets, DNS temporary
// failures, etc.).
func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	// net.Error with Temporary() or Timeout()
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}
	// String-based heuristics for errors that don't implement net.Error
	// (e.g. wrapped http2 stream errors from upstream).
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if containsAnyPattern(msg, transientNetworkErrorPatterns) {
		return true
	}
	// Subprocess spawn failures (fork/exec) due to transient resource
	// exhaustion (fd limit, process limit, NFS stale handle).
	if isTransientProcessError(err) {
		return true
	}
	return false
}

// isTransientProcessError returns true if the error looks like a subprocess
// spawn failure that may be caused by transient resource exhaustion (e.g.
// too many open files, process limit reached, temporary filesystem issues).
// These errors are distinct from permanent failures like "permission denied"
// or "no such file or directory".
func isTransientProcessError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// fork/exec failures with "invalid argument", "resource temporarily
	// unavailable", "too many open files", or "cannot allocate memory"
	// are typically transient resource-exhaustion issues.
	if !strings.Contains(msg, "fork/exec") {
		return false
	}
	return containsAnyPattern(msg, transientProcessErrorPatterns)
}

// isRateLimitError returns true if the error indicates the upstream provider
// hit a rate limit or usage quota.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if containsAnyPattern(msg, rateLimitErrorPatterns) {
		return true
	}
	if rateLimit429Re.MatchString(msg) {
		return true
	}
	return false
}

var transientNetworkErrorPatterns = []string{
	"internal_error",           // HTTP/2 stream reset
	"stream error",             // HTTP/2 stream error
	"connection reset by peer", // TCP RST
	"broken pipe",              // write to closed connection
	"goaway",                   // HTTP/2 GOAWAY
	"unexpected eof",           // unexpected connection close
	"tls handshake timeout",
	"i/o timeout",
	"no such host", // transient DNS (debatable, but safer to classify as network)
	"connection refused",
}

var transientProcessErrorPatterns = []string{
	"invalid argument",                 // often caused by fd/process exhaustion
	"resource temporarily unavailable", // EAGAIN
	"too many open files",              // EMFILE/ENFILE
	"cannot allocate memory",           // ENOMEM
	"text file busy",                   // binary being replaced
}

var rateLimitErrorPatterns = []string{
	"usage_limit_exceeded",
	"rate_limit_exceeded",
	"rate_limit",
	"quota_exceeded",
	"too many requests",
	"resource_exhausted",
	"tokens_exceeded",
}

func containsAnyPattern(msg string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

var rateLimit429Re = regexp.MustCompile(`(?i)\b(?:status(?:_code)?|status\s*code|statuscode|http|code)\s*[:=_-]?\s*429\b`)

func isInterruptedRun(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
