package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	// codexUsageURL is the ChatGPT usage endpoint. It is unreachable directly
	// from the host and must go through the proxy the user configured in the
	// Codex ACP env vars (http_proxy / https_proxy).
	codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	// codexResetCreditsURL returns the per-credit detail for the rate-limit
	// reset credits (the usage endpoint only gives the aggregate count). Same
	// host as codexUsageURL, so it goes through the same Codex proxy.
	codexResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexAuthRelPath     = ".codex/auth.json"
)

type codexAuthFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type codexUsageResp struct {
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	RateLimit struct {
		PrimaryWindow   *codexWindow `json:"primary_window"`
		SecondaryWindow *codexWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	RateLimitResetCredits struct {
		AvailableCount int `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
}

type codexWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// codexResetCreditsResp is the rate-limit-reset-credits endpoint payload. Each
// credit carries its own status and expiry; only "available" ones are usable.
type codexResetCreditsResp struct {
	AvailableCount int `json:"available_count"`
	Credits        []struct {
		Status    string `json:"status"`     // "available" | "redeemed" | "expired" | ...
		ExpiresAt string `json:"expires_at"` // RFC3339, e.g. "2026-07-18T00:34:21.912338Z"
	} `json:"credits"`
}

// codexResetCredits is the parsed reset-credit detail. fetched is false when the
// supplementary call failed, so the caller can fall back to the aggregate count
// from the usage endpoint. Expiries hold unix seconds of available credits,
// ascending.
type codexResetCredits struct {
	fetched  bool
	count    int
	expiries []int64
}

func (s *serviceImpl) CodexUsage(ctx context.Context) (*model.CodexUsage, error) {
	// Fetch the Codex CLI version in parallel so it adds no serial latency.
	verCh := s.codexVersionAsync(ctx)

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir failed: %w", err)
	}
	authPath := filepath.Join(home, codexAuthRelPath)
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("read %s failed: %w", authPath, err)
	}
	var auth codexAuthFile
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, fmt.Errorf("parse %s failed: %w", authPath, err)
	}
	if auth.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("%s: tokens.access_token is empty", authPath)
	}

	client := &http.Client{
		Timeout:   25 * time.Second,
		Transport: proxyTransport(s.codexACPEnv()),
	}
	// Fetch per-credit reset detail in parallel with the usage request; it shares
	// the same proxy client and auth. Non-fatal — see codexResetCredits.
	creditsCh := s.codexResetCreditsAsync(ctx, client, auth.Tokens.AccessToken, auth.Tokens.AccountID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	if auth.Tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", auth.Tokens.AccountID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request ChatGPT usage failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ChatGPT usage returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var u codexUsageResp
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("parse ChatGPT usage response failed: %w (body: %s)", err, string(body))
	}

	// Prefer the reset-credits endpoint's count (it's the source of the expiry
	// list, so they always agree); fall back to the usage endpoint's aggregate.
	credits := <-creditsCh
	resetCount := u.RateLimitResetCredits.AvailableCount
	if credits.fetched {
		resetCount = credits.count
	}

	return &model.CodexUsage{
		Email:               u.Email,
		PlanType:            u.PlanType,
		Version:             <-verCh,
		PrimaryWindow:       toUsageWindow(u.RateLimit.PrimaryWindow),
		SecondaryWindow:     toUsageWindow(u.RateLimit.SecondaryWindow),
		ResetCredits:        resetCount,
		ResetCreditExpiries: credits.expiries,
	}, nil
}

// codexResetCreditsAsync fetches the reset-credit detail on a goroutine and
// returns a buffered channel with the result. Any failure yields a zero value
// (fetched=false) after a warning — the expiry list is supplementary and must
// not fail the whole usage request.
func (s *serviceImpl) codexResetCreditsAsync(ctx context.Context, client *http.Client, accessToken, accountID string) <-chan codexResetCredits {
	ch := make(chan codexResetCredits, 1)
	go func() { ch <- s.codexResetCredits(ctx, client, accessToken, accountID) }()
	return ch
}

func (s *serviceImpl) codexResetCredits(ctx context.Context, client *http.Client, accessToken, accountID string) codexResetCredits {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexResetCreditsURL, nil)
	if err != nil {
		logger.Warnf(ctx, "[agent.usage] build reset-credits request failed: %v", err)
		return codexResetCredits{}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Warnf(ctx, "[agent.usage] request reset-credits failed: %v", err)
		return codexResetCredits{}
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warnf(ctx, "[agent.usage] reset-credits returned HTTP %d: %s", resp.StatusCode, string(body))
		return codexResetCredits{}
	}

	var r codexResetCreditsResp
	if err := json.Unmarshal(body, &r); err != nil {
		logger.Warnf(ctx, "[agent.usage] parse reset-credits failed: %v (body: %s)", err, string(body))
		return codexResetCredits{}
	}

	var expiries []int64
	for _, c := range r.Credits {
		if c.Status != "available" {
			continue
		}
		if c.ExpiresAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, c.ExpiresAt)
		if err != nil {
			logger.Warnf(ctx, "[agent.usage] parse reset-credit expires_at %q failed: %v", c.ExpiresAt, err)
			continue
		}
		expiries = append(expiries, t.Unix())
	}
	slices.Sort(expiries)
	// available_count is authoritative when present; otherwise derive from the
	// available credits we counted.
	count := r.AvailableCount
	if count == 0 && len(expiries) > 0 {
		count = len(expiries)
	}
	return codexResetCredits{fetched: true, count: count, expiries: expiries}
}

func toUsageWindow(w *codexWindow) *model.UsageWindow {
	if w == nil {
		return nil
	}
	return &model.UsageWindow{
		UsedPercent:        w.UsedPercent,
		LimitWindowSeconds: w.LimitWindowSeconds,
		ResetAfterSeconds:  w.ResetAfterSeconds,
		ResetAt:            w.ResetAt,
	}
}

// codexACPEnv returns the effective env for the Codex ACP agent, including the
// runtime default that points the adapter at the installed Codex CLI.
func (s *serviceImpl) codexACPEnv() map[string]string {
	return s.effectiveACPEnv("codex")
}

// codexVersionAsync runs the version probe in parallel with the usage requests.
// The buffered channel keeps the goroutine from blocking when CodexUsage exits
// before reading the supplementary version.
func (s *serviceImpl) codexVersionAsync(ctx context.Context) <-chan string {
	ch := make(chan string, 1)
	go func() { ch <- s.codexVersion(ctx) }()
	return ch
}

// codexVersion asks the configured codex-acp executable to forward -V to the
// exact Codex CLI it uses. This resolves the adapter's own @openai/codex
// dependency instead of an unrelated `codex` from the host PATH, and it also
// honors the adapter's CODEX_PATH override.
func (s *serviceImpl) codexVersion(ctx context.Context) string {
	command := acpCommandByBin("codex")
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return ""
	}

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(parts[1:], "cli", "-V")
	cmd := executil.CommandContext(cctx, parts[0], args...)
	applyCommandEnv(cmd, s.codexACPEnv())
	out, err := cmd.Output()
	if err != nil {
		logger.Warnf(ctx, "[agent.usage] Codex version probe failed: command=%q err=%v", command, err)
		return ""
	}
	m := semverRe.Find(out)
	if m == nil {
		return ""
	}
	return "v" + string(m)
}
