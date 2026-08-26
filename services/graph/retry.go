package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

const (
	graphTransientRetries = 2
	graphRateLimitRetries = 3

	defaultGraphTransientRetryDelay     = 3 * time.Second
	defaultGraphRateLimitRetryBaseDelay = 30 * time.Second
)

type graphRetryResult struct {
	handler    *graphEventHandler
	usage      *usagestats.Accumulator
	retryCount int
	err        error
}

type graphRetryPolicy struct {
	name         string
	maxRetries   int
	classify     func(error) bool
	delay        func(error, int) time.Duration
	exhaustedLog func(error)
}

// runPromptWithRetries runs one Prompt/评估 Agent iteration through the shared
// transient/rate-limit retry driver. The event handler is recreated on every
// attempt so accumulated content reflects only the final (successful or last
// failed) iteration.
func (s *serviceImpl) runPromptWithRetries(ctx context.Context, runID, jobID, sessionID, nodeID string, key model.GraphInstanceKey, runner Runner, messages []*schema.Message) graphRetryResult {
	var handler *graphEventHandler
	aggregate := usagestats.NewAccumulator()
	attempt := func(ctx context.Context) error {
		handler = s.newGraphEventHandler(ctx, runID, jobID, sessionID, nodeID, key, messages)
		err := runner.RunIteration(ctx, sessionID, messages, handler)
		handler.finalizeUsageEstimate()
		aggregate.Merge(handler.usage)
		return err
	}
	retryCount, err := s.runWithRetries(ctx, runID, jobID, nodeID, attempt)
	aggregate.NormalizeTurnCoverage()
	return graphRetryResult{handler: handler, usage: aggregate, retryCount: retryCount, err: err}
}

// runWithRetries drives any node executor through the same two-stage retry
// policy used across Graph node types (§2 瞬态错误重试): first transient
// network/stream errors (固定退避), then rate-limit/429 errors (指数退避，含
// Retry-After hint). The attempt closure performs one execution and is retried
// in place; it must reset any per-attempt accumulators it captures. Returns the
// number of retries performed (0 on first-attempt success) and the final error.
func (s *serviceImpl) runWithRetries(ctx context.Context, runID, jobID, nodeID string, attempt func(context.Context) error) (int, error) {
	err := attempt(ctx)
	retryCount := 0
	if err == nil || isInterruptedGraphRun(err) {
		return retryCount, err
	}

	retryCount, err = s.retryWithPolicy(ctx, runID, jobID, nodeID, attempt, retryCount, err, graphRetryPolicy{
		name:       "transient error",
		maxRetries: graphTransientRetries,
		classify:   isTransientGraphError,
		delay: func(error, int) time.Duration {
			return s.graphTransientDelay()
		},
		exhaustedLog: func(err error) {
			logger.Errorf(ctx, "[graph] transient error persisted after %d retries: runId=%s jobId=%s nodeId=%s err=%v",
				graphTransientRetries, runID, jobID, nodeID, err)
		},
	})
	if err == nil || isInterruptedGraphRun(err) {
		return retryCount, err
	}

	retryAfterHint := parseGraphRetryAfter(err)
	retryCount, err = s.retryWithPolicy(ctx, runID, jobID, nodeID, attempt, retryCount, err, graphRetryPolicy{
		name:       "rate limit",
		maxRetries: graphRateLimitRetries,
		classify:   isRateLimitGraphError,
		delay: func(_ error, attempt int) time.Duration {
			d := s.graphRateLimitBaseDelay() * time.Duration(1<<(attempt-1))
			if retryAfterHint > d {
				return retryAfterHint
			}
			return d
		},
		exhaustedLog: func(err error) {
			logger.Errorf(ctx, "[graph] rate limit persisted after %d retries: runId=%s jobId=%s nodeId=%s err=%v",
				graphRateLimitRetries, runID, jobID, nodeID, err)
		},
	})
	return retryCount, err
}

func (s *serviceImpl) retryWithPolicy(
	ctx context.Context,
	runID, jobID, nodeID string,
	attempt func(context.Context) error,
	retryCount int,
	err error,
	policy graphRetryPolicy,
) (int, error) {
	if err == nil || isInterruptedGraphRun(err) || !policy.classify(err) {
		return retryCount, err
	}
	for attemptNum := 1; attemptNum <= policy.maxRetries; attemptNum++ {
		delay := policy.delay(err, attemptNum)
		logger.Warnf(ctx, "[graph] %s (attempt %d/%d), retrying in %s: runId=%s jobId=%s nodeId=%s err=%v",
			policy.name, attemptNum, policy.maxRetries, delay, runID, jobID, nodeID, err)
		if waitErr := waitForGraphRetry(ctx, delay); waitErr != nil {
			return retryCount, waitErr
		}
		retryCount++
		err = attempt(ctx)
		if err == nil || isInterruptedGraphRun(err) || !policy.classify(err) {
			if err == nil {
				logger.Infof(ctx, "[graph] %s recovered after %d attempt(s): runId=%s jobId=%s nodeId=%s",
					policy.name, attemptNum, runID, jobID, nodeID)
			}
			return retryCount, err
		}
	}
	if err != nil && !isInterruptedGraphRun(err) && policy.classify(err) && policy.exhaustedLog != nil {
		policy.exhaustedLog(err)
	}
	return retryCount, err
}

func (s *serviceImpl) graphTransientDelay() time.Duration {
	if s.transientRetryDelay > 0 {
		return s.transientRetryDelay
	}
	return defaultGraphTransientRetryDelay
}

func (s *serviceImpl) graphRateLimitBaseDelay() time.Duration {
	if s.rateLimitRetryBaseDelay > 0 {
		return s.rateLimitRetryBaseDelay
	}
	return defaultGraphRateLimitRetryBaseDelay
}

func waitForGraphRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isInterruptedGraphRun(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isTransientGraphError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return containsAnyGraphPattern(msg, transientGraphErrorPatterns)
}

func isRateLimitGraphError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return containsAnyGraphPattern(msg, rateLimitGraphErrorPatterns) || graphRateLimit429Re.MatchString(msg)
}

func containsAnyGraphPattern(msg string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

var transientGraphErrorPatterns = []string{
	"internal_error",
	"stream error",
	"connection reset by peer",
	"broken pipe",
	"goaway",
	"unexpected eof",
	"tls handshake timeout",
	"i/o timeout",
	"no such host",
	"connection refused",
}

var rateLimitGraphErrorPatterns = []string{
	"usage_limit_exceeded",
	"rate_limit_exceeded",
	"rate_limit",
	"quota_exceeded",
	"too many requests",
	"resource_exhausted",
	"tokens_exceeded",
}

var (
	graphRateLimit429Re = regexp.MustCompile(`(?i)\b(?:status(?:_code)?|status\s*code|statuscode|http|code)\s*[:=_-]?\s*429\b`)
	graphRetryAfterRe   = regexp.MustCompile(`(?i)retry[-_ ]?after[: ]*(\d+)`)
)

func parseGraphRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	matches := graphRetryAfterRe.FindStringSubmatch(err.Error())
	if len(matches) < 2 {
		return 0
	}
	secs, parseErr := strconv.Atoi(matches[1])
	if parseErr != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}

type graphRetryError struct {
	err        error
	retryCount int
}

func (e *graphRetryError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return fmt.Sprintf("%v (retryCount=%d)", e.err, e.retryCount)
}

func (e *graphRetryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func withGraphRetryCount(err error, retryCount int) error {
	if err == nil || retryCount <= 0 {
		return err
	}
	return &graphRetryError{err: err, retryCount: retryCount}
}

func graphRetryCount(err error) int {
	var retryErr *graphRetryError
	if errors.As(err, &retryErr) {
		return retryErr.retryCount
	}
	return 0
}

var _ agui.EventHandler = (*graphEventHandler)(nil)
