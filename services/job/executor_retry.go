package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
)

// loopTransientRetries is the maximum number of automatic retries for transient
// errors (network resets, HTTP/2 stream errors) before failing the loop job.
const loopTransientRetries = 2

// defaultLoopTransientRetryDelay is the production backoff between transient retries.
const defaultLoopTransientRetryDelay = 3 * time.Second

// loopRateLimitRetries is the maximum number of retries for rate-limit/quota errors.
// Higher than transient retries because the provider explicitly tells us to wait.
const loopRateLimitRetries = 3

// defaultLoopRateLimitBaseDelay is the production initial backoff for
// rate-limit retries. Each retry doubles the delay (exponential backoff).
const defaultLoopRateLimitBaseDelay = 30 * time.Second

const (
	runErrorCodeInternal  = "INTERNAL"
	runErrorCodeTimeout   = "TIMEOUT"
	runErrorCodeNetwork   = "NETWORK"
	runErrorCodeRateLimit = "RATE_LIMIT"
	runErrorCodeShell     = "SHELL"
	runErrorCodePanic     = "PANIC"
)

type codedRunError struct {
	code string
	err  error
}

func (e *codedRunError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *codedRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func withRunErrorCode(err error, code string) error {
	if err == nil || code == "" {
		return err
	}
	return &codedRunError{code: code, err: err}
}

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
// Prefer structured/wrapped signals and fall back to the same upstream-message
// heuristics used by retry so logs, retry behavior, and frontend-visible codes
// stay consistent.
func classifyRunErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded *codedRunError
	if errors.As(err, &coded) && coded.code != "" {
		return coded.code
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
// network issue that is likely to succeed on retry (HTTP/2 stream resets,
// connection resets, DNS temporary failures, etc.).
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
	// exhaustion (fd limit, process limit, NFS stale handle) should be
	// retried rather than immediately killing the job.
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
// hit a rate limit or usage quota that will recover after a cooldown period.
// These errors are distinct from transient network errors: the request did
// reach the server, but the server is refusing to process it temporarily.
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
	"no such host", // transient DNS (debatable, but safer to retry)
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

// retryAfterRe matches machine-oriented retry hints like "retry after 60", "retry_after: 120", or "Retry-After: 30".
var retryAfterRe = regexp.MustCompile(`(?i)retry[-_ ]?after[: ]*(\d+)`)

// parseRetryAfter attempts to extract a retry-after duration hint from error
// messages like "retry after 60s" or "retry_after: 120". Returns 0 if no
// parseable hint is found. Note: human-readable hints like "try again at
// 10:40 PM" are NOT parsed — the caller falls back to default backoff.
func parseRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	msg := err.Error()
	if matches := retryAfterRe.FindStringSubmatch(msg); len(matches) >= 2 {
		if secs, e := strconv.Atoi(matches[1]); e == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func isInterruptedRun(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *serviceImpl) runIterationWithRetries(
	ctx context.Context,
	jobID, sessionID string,
	path []int,
	runner JobRunner,
	messages []*schema.Message,
	handler *loopEventHandler,
	isLoopRun bool,
) (*loopEventHandler, time.Time, time.Duration, error) {
	start := time.Now()
	err := runner.RunIteration(ctx, sessionID, messages, handler)
	duration := time.Since(start)
	if !isLoopRun || err == nil || isInterruptedRun(err) {
		return handler, start, duration, err
	}

	handler, start, duration, err = s.retryIterationFailures(ctx, jobID, sessionID, path, runner, messages, handler, start, duration, err, retryPolicy{
		name:       "transient error",
		maxRetries: loopTransientRetries,
		classify:   isTransientNetworkError,
		delay: func(error, int) time.Duration {
			return s.transientRetryDelay()
		},
		exhaustedLog: func(err error) {
			logger.Errorf(ctx, "[step] transient error persisted after %d retries: jobId=%s path=%v err=%v",
				loopTransientRetries, jobID, path, err)
		},
	})
	if err == nil || isInterruptedRun(err) {
		return handler, start, duration, err
	}

	// Rate-limit retry for loop runs: upstream providers (Claude, OpenAI, etc.)
	// may return usage_limit_exceeded or 429 with a "try again later" hint.
	// Unlike transient network errors, these need longer backoff (30s/60s/120s).
	retryAfterHint := parseRetryAfter(err)
	return s.retryIterationFailures(ctx, jobID, sessionID, path, runner, messages, handler, start, duration, err, retryPolicy{
		name:       "rate limit",
		maxRetries: loopRateLimitRetries,
		classify:   isRateLimitError,
		delay: func(_ error, attempt int) time.Duration {
			d := s.rateLimitBaseDelay() * time.Duration(1<<(attempt-1)) // 30s, 60s, 120s
			if retryAfterHint > 0 && retryAfterHint > d {
				return retryAfterHint
			}
			return d
		},
		exhaustedLog: func(err error) {
			logger.Errorf(ctx, "[step] rate limit persisted after %d retries (total wait ~%s): jobId=%s path=%v err=%v",
				loopRateLimitRetries, s.rateLimitBaseDelay()*time.Duration((1<<loopRateLimitRetries)-1), jobID, path, err)
		},
	})
}

func (s *serviceImpl) transientRetryDelay() time.Duration {
	if s.loopTransientRetryDelay > 0 {
		return s.loopTransientRetryDelay
	}
	return defaultLoopTransientRetryDelay
}

func (s *serviceImpl) rateLimitBaseDelay() time.Duration {
	if s.loopRateLimitBaseDelay > 0 {
		return s.loopRateLimitBaseDelay
	}
	return defaultLoopRateLimitBaseDelay
}

type retryPolicy struct {
	name         string
	maxRetries   int
	classify     func(error) bool
	delay        func(error, int) time.Duration
	exhaustedLog func(error)
}

func (s *serviceImpl) retryIterationFailures(
	ctx context.Context,
	jobID, sessionID string,
	path []int,
	runner JobRunner,
	messages []*schema.Message,
	handler *loopEventHandler,
	start time.Time,
	duration time.Duration,
	err error,
	policy retryPolicy,
) (*loopEventHandler, time.Time, time.Duration, error) {
	if err == nil || isInterruptedRun(err) || !policy.classify(err) {
		return handler, start, duration, err
	}
	for attempt := 1; attempt <= policy.maxRetries; attempt++ {
		delay := policy.delay(err, attempt)
		logger.Warnf(ctx, "[step] %s (attempt %d/%d), retrying in %s: jobId=%s path=%v err=%v",
			policy.name, attempt, policy.maxRetries, delay, jobID, path, err)

		if waitErr := waitForRetry(ctx, delay); waitErr != nil {
			return handler, start, duration, waitErr
		}

		// Reset handler for the retry — accumulator must start fresh.
		handler = newLoopEventHandler(ctx, jobID, sessionID, path, s)
		start = time.Now()
		err = runner.RunIteration(ctx, sessionID, messages, handler)
		duration = time.Since(start)

		if err == nil || isInterruptedRun(err) || !policy.classify(err) {
			if err == nil {
				logger.Infof(ctx, "[step] %s recovered after %d attempt(s): jobId=%s path=%v", policy.name, attempt, jobID, path)
			}
			return handler, start, duration, err
		}
	}
	if err != nil && !isInterruptedRun(err) && policy.classify(err) && policy.exhaustedLog != nil {
		policy.exhaustedLog(err)
	}
	return handler, start, duration, err
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
