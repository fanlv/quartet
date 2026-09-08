package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	claudeUsageURL          = "https://api.anthropic.com/api/oauth/usage"
	claudeOAuthBeta         = "oauth-2025-04-20"
	claudeSessionWindowSecs = int64(5 * 60 * 60)
	claudeWeeklyWindowSecs  = int64(7 * 24 * 60 * 60)
	claudeUsageMaxAttempts  = 3
	claudeRetryDelayCap     = 5 * time.Second
)

type claudeCodeCredentials struct {
	ClaudeAIOAuth struct {
		AccessToken      any `json:"accessToken"`
		SubscriptionType any `json:"subscriptionType"`
		RateLimitTier    any `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeUsageResp struct {
	FiveHour     *claudeUsageWindow `json:"five_hour"`
	SevenDay     *claudeUsageWindow `json:"seven_day"`
	SevenDayOpus *claudeUsageWindow `json:"seven_day_opus"`
	Limits       []struct {
		Kind     string   `json:"kind"`
		Percent  *float64 `json:"percent"`
		ResetsAt string   `json:"resets_at"`
		Scope    *struct {
			Model *struct {
				ID          *string `json:"id"`
				DisplayName *string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	} `json:"limits"`
	ExtraUsage *model.ClaudeExtraUsage `json:"extra_usage"`
}

// ClaudeUsage reads Claude Code's own OAuth credentials and queries Anthropic's
// public usage endpoint. It does not consult any custom API base configured for
// model traffic, so it works for regular Claude Code accounts on macOS, Linux,
// and Windows.
func (s *serviceImpl) ClaudeUsage(ctx context.Context) (*model.ClaudeUsage, error) {
	credentials, source, err := readClaudeCodeCredentials(ctx)
	if err != nil {
		return nil, err
	}
	token, _ := credentials.ClaudeAIOAuth.AccessToken.(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("Claude Code OAuth access token is missing in %s; run 'claude' to sign in", source)
	}

	verCh := s.claudeVersionAsync(ctx)
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: proxyTransport(s.effectiveACPEnv("claude")),
	}
	body, err := fetchClaudeUsage(ctx, client, token)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	return &model.ClaudeUsage{
		PlanType:      claudeCredentialString(credentials.ClaudeAIOAuth.SubscriptionType),
		RateLimitTier: claudeCredentialString(credentials.ClaudeAIOAuth.RateLimitTier),
		Version:       <-verCh,
		FiveHour:      convertClaudeWindow(body.FiveHour, claudeSessionWindowSecs, now),
		SevenDay:      convertClaudeWindow(body.SevenDay, claudeWeeklyWindowSecs, now),
		SevenDayOpus:  convertClaudeWindow(body.SevenDayOpus, claudeWeeklyWindowSecs, now),
		WeeklyScoped:  convertClaudeScopedWindows(body, now),
		ExtraUsage:    body.ExtraUsage,
	}, nil
}

func claudeCredentialString(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strings.TrimSpace(fmt.Sprint(value))
	case bool:
		return fmt.Sprint(value)
	default:
		return ""
	}
}

func fetchClaudeUsage(ctx context.Context, client *http.Client, token string) (*claudeUsageResp, error) {
	for attempt := 1; attempt <= claudeUsageMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build Claude OAuth usage request failed: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-beta", claudeOAuthBeta)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request Claude OAuth usage failed: %w", err)
		}
		body, readErr := readAllLimited(resp)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read Claude OAuth usage response failed: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var usage claudeUsageResp
			if err := json.Unmarshal(body, &usage); err != nil {
				return nil, fmt.Errorf("parse Claude OAuth usage response failed: %w (body: %s)", err, string(body))
			}
			return &usage, nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("Claude Code OAuth token expired; run 'claude' to sign in again (HTTP %d: %s)", resp.StatusCode, string(body))
		}

		delay, serverDelay := claudeRetryDelay(resp.Header.Get("Retry-After"), attempt, time.Now())
		// Do not hammer a 429: Claude's usage endpoint has a shared budget with
		// Claude Code itself, and immediate retries can extend the cooldown. A
		// short 503 is safe to retry; long server-directed delays are surfaced.
		retryable := resp.StatusCode == http.StatusServiceUnavailable
		if !retryable || attempt == claudeUsageMaxAttempts || delay > claudeRetryDelayCap {
			retrySuffix := ""
			if serverDelay {
				retrySuffix = fmt.Sprintf("; Retry-After=%s", resp.Header.Get("Retry-After"))
			}
			return nil, fmt.Errorf("Claude OAuth usage returned HTTP %d%s: %s", resp.StatusCode, retrySuffix, string(body))
		}
		if err := waitClaudeRetry(ctx, delay); err != nil {
			return nil, fmt.Errorf("wait to retry Claude OAuth usage after HTTP %d failed: %w", resp.StatusCode, err)
		}
	}
	return nil, fmt.Errorf("Claude OAuth usage request exhausted all attempts")
}

func claudeRetryDelay(retryAfter string, attempt int, now time.Time) (time.Duration, bool) {
	retryAfter = strings.TrimSpace(retryAfter)
	if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return max(0, at.Sub(now)), true
	}
	return time.Duration(attempt) * 1500 * time.Millisecond, false
}

func waitClaudeRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func convertClaudeWindow(window *claudeUsageWindow, durationSeconds int64, now time.Time) *model.UsageWindow {
	if window == nil || window.Utilization == nil {
		return nil
	}
	usedPercent := max(0, min(100, *window.Utilization))
	resetAt := parseClaudeResetAt(window.ResetsAt)
	resetAfter := int64(0)
	if resetAt > now.Unix() {
		resetAfter = resetAt - now.Unix()
	}
	return &model.UsageWindow{
		UsedPercent:        usedPercent,
		LimitWindowSeconds: durationSeconds,
		ResetAfterSeconds:  resetAfter,
		ResetAt:            resetAt,
	}
}

func convertClaudeScopedWindows(body *claudeUsageResp, now time.Time) []model.ClaudeScopedUsageWindow {
	if body == nil {
		return nil
	}
	out := make([]model.ClaudeScopedUsageWindow, 0)
	seen := make(map[string]bool)
	for _, limit := range body.Limits {
		if limit.Kind != "weekly_scoped" || limit.Percent == nil || limit.Scope == nil || limit.Scope.Model == nil {
			continue
		}
		label := firstNonEmpty(pointerString(limit.Scope.Model.DisplayName), pointerString(limit.Scope.Model.ID))
		if label == "" {
			continue
		}
		if body.SevenDayOpus != nil && strings.EqualFold(label, "opus") {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		window := convertClaudeWindow(&claudeUsageWindow{
			Utilization: limit.Percent,
			ResetsAt:    limit.ResetsAt,
		}, claudeWeeklyWindowSecs, now)
		if window == nil {
			continue
		}
		out = append(out, model.ClaudeScopedUsageWindow{Label: label, UsageWindow: *window})
	}
	return out
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func parseClaudeResetAt(value string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

// claudeVersionAsync runs the version probe in parallel with the usage call.
func (s *serviceImpl) claudeVersionAsync(ctx context.Context) <-chan string {
	ch := make(chan string, 1)
	go func() { ch <- s.claudeVersion(ctx) }()
	return ch
}

// claudeVersion asks claude-agent-acp to report the Claude CLI version behind
// the wrapper. Version display is supplementary and does not fail usage reads.
func (s *serviceImpl) claudeVersion(ctx context.Context) string {
	command := acpCommandByBin("claude")
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return ""
	}

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(parts[1:], "--cli", "--version")
	cmd := executil.CommandContext(cctx, parts[0], args...)
	applyCommandEnv(cmd, s.effectiveACPEnv("claude"))
	out, err := cmd.Output()
	if err != nil {
		logger.Warnf(ctx, "[agent.usage] Claude version probe failed: command=%q err=%v", command, err)
		return ""
	}
	m := semverRe.Find(out)
	if m == nil {
		return ""
	}
	return "v" + string(m)
}
