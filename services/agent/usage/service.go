package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/config"
	"github.com/fanlv/quartet/types/model"
)

const (
	// codexUsageURL is the ChatGPT usage endpoint. It is unreachable directly
	// from the host and must go through the proxy the user configured in the
	// Codex ACP env vars (http_proxy / https_proxy).
	codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	// claudeDefaultBase is used when ~/.claude/settings.json has no
	// ANTHROPIC_BASE_URL. This host is directly reachable (it is in no_proxy),
	// so the Claude requests never go through a proxy.
	claudeDefaultBase = "https://9mwkeekm.fn.sinf.net"

	codexAuthRelPath      = ".codex/auth.json"
	claudeSettingsRelPath = ".claude/settings.json"

	claudeRetries = 3
)

// Service fetches the current subscription / quota info for the Codex and
// Claude ACP agents. Each call fetches live data — the Home page requests a
// refresh on every agent-type switch, so nothing is cached here.
type Service interface {
	CodexUsage(ctx context.Context) (*model.CodexUsage, error)
	ClaudeUsage(ctx context.Context) (*model.ClaudeUsage, error)
}

type serviceImpl struct {
	settings config.SettingsService
}

// NewService builds the usage service. It depends on the settings service to
// read the Codex ACP env vars (proxy) that the ChatGPT usage request needs.
func NewService(settings config.SettingsService) Service {
	return &serviceImpl{settings: settings}
}

// ---- Codex ----

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

func (s *serviceImpl) CodexUsage(ctx context.Context) (*model.CodexUsage, error) {
	// Fetch the acp version in parallel so it adds no serial latency.
	verCh := s.acpVersionAsync(ctx, "codex")

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
		Transport: proxyTransport(s.codexProxyEnv()),
	}
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

	return &model.CodexUsage{
		Email:           u.Email,
		PlanType:        u.PlanType,
		Version:         <-verCh,
		PrimaryWindow:   toUsageWindow(u.RateLimit.PrimaryWindow),
		SecondaryWindow: toUsageWindow(u.RateLimit.SecondaryWindow),
		ResetCredits:    u.RateLimitResetCredits.AvailableCount,
	}, nil
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

// codexProxyEnv returns the proxy-related env vars the user configured for the
// Codex ACP agent. ACP env vars are keyed by the agent's full serve command,
// so resolve Codex's command from the known-agent registry.
func (s *serviceImpl) codexProxyEnv() map[string]string {
	command := acpCommandByBin("codex")
	if command == "" {
		return nil
	}
	return s.settings.GetACPEnvVars(command)
}

// acpCommandByBin resolves an ACP agent's full serve command from the
// known-agent registry (e.g. "codex" -> "npx @agentclientprotocol/codex-acp").
func acpCommandByBin(bin string) string {
	for _, a := range probe.KnownACPAgents {
		if a.Bin == bin {
			return a.Command
		}
	}
	return ""
}

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// acpVersionAsync runs the version probe on a goroutine and returns a buffered
// channel with the result, so the caller can do it in parallel with the usage
// HTTP request. The buffer means the goroutine never blocks even if an early
// error return skips the read.
func (s *serviceImpl) acpVersionAsync(ctx context.Context, bin string) <-chan string {
	ch := make(chan string, 1)
	command := acpCommandByBin(bin)
	go func() { ch <- s.acpVersion(ctx, command) }()
	return ch
}

// acpVersion runs `<command> --cli --version` and returns the version prefixed
// with "v" (e.g. "v1.1.0"), or "" on any failure. Version display is
// supplementary — a probe failure must not fail the whole usage request.
// Reads stdout only so npx stderr warnings can't false-match a version.
func (s *serviceImpl) acpVersion(ctx context.Context, command string) string {
	if command == "" {
		return ""
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(parts[1:], "--cli", "--version")
	out, err := exec.CommandContext(cctx, parts[0], args...).Output()
	if err != nil {
		logger.Warnf(ctx, "[agent.usage] version probe failed: command=%q err=%v", command, err)
		return ""
	}
	m := semverRe.Find(out)
	if m == nil {
		return ""
	}
	return "v" + string(m)
}

// ---- Claude ----

type claudeSettingsFile struct {
	Env struct {
		AuthToken string `json:"ANTHROPIC_AUTH_TOKEN"`
		BaseURL   string `json:"ANTHROPIC_BASE_URL"`
	} `json:"env"`
}

type claudeUsageResp struct {
	Date  string `json:"date"`
	Users []struct {
		Name      string  `json:"name"`
		KeySuffix string  `json:"key_suffix"`
		Cost      float64 `json:"cost"`
	} `json:"users"`
}

func (s *serviceImpl) ClaudeUsage(ctx context.Context) (*model.ClaudeUsage, error) {
	// Fetch the acp version in parallel so it adds no serial latency.
	verCh := s.acpVersionAsync(ctx, "claude")

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir failed: %w", err)
	}
	settingsPath := filepath.Join(home, claudeSettingsRelPath)
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s failed: %w", settingsPath, err)
	}
	var cs claudeSettingsFile
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, fmt.Errorf("parse %s failed: %w", settingsPath, err)
	}
	token := cs.Env.AuthToken
	if token == "" {
		return nil, fmt.Errorf("%s: env.ANTHROPIC_AUTH_TOKEN is empty", settingsPath)
	}
	suffix := token
	if len(token) > 8 {
		suffix = token[len(token)-8:]
	}
	base := cs.Env.BaseURL
	if base == "" {
		base = claudeDefaultBase
	}

	// Direct client (no proxy): the usage host is in no_proxy and reachable
	// without the byted proxy. Explicitly disable proxy so a global http_proxy
	// in the process env can't misroute it.
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}

	total, err := s.claudeCost(ctx, client, base, suffix, "")
	if err != nil {
		return nil, fmt.Errorf("query total usage failed: %w", err)
	}
	today := time.Now().Format("2006-01-02")
	todayCost, err := s.claudeCost(ctx, client, base, suffix, today)
	if err != nil {
		return nil, fmt.Errorf("query today usage failed: %w", err)
	}

	return &model.ClaudeUsage{
		Name:      total.name,
		KeySuffix: suffix,
		Version:   <-verCh,
		TodayCost: todayCost.cost,
		TotalCost: total.cost,
	}, nil
}

type claudeCostResult struct {
	name string
	cost float64
}

// claudeCost fetches one usage view and returns the current key's row. When
// date is empty it queries type=total; otherwise type=claude_code for that
// date. The endpoint occasionally times out, so it retries.
func (s *serviceImpl) claudeCost(ctx context.Context, client *http.Client, base, suffix, date string) (claudeCostResult, error) {
	q := url.Values{}
	if date == "" {
		q.Set("type", "total")
	} else {
		q.Set("type", "claude_code")
		q.Set("date", date)
	}
	endpoint := base + "/v1/usage?" + q.Encode()

	var lastErr error
	for attempt := 1; attempt <= claudeRetries; attempt++ {
		res, err := s.fetchClaudeUsage(ctx, client, endpoint)
		if err == nil {
			for _, u := range res.Users {
				if u.KeySuffix == suffix {
					return claudeCostResult{name: u.Name, cost: u.Cost}, nil
				}
			}
			// No matching key: not an error worth retrying — the token simply
			// isn't tracked by this usage backend. Report zero with the empty
			// name so the UI still renders.
			return claudeCostResult{}, nil
		}
		lastErr = err
		logger.Warnf(ctx, "[agent.usage] claude usage attempt %d/%d failed: %v", attempt, claudeRetries, err)
	}
	return claudeCostResult{}, lastErr
}

func (s *serviceImpl) fetchClaudeUsage(ctx context.Context, client *http.Client, endpoint string) (*claudeUsageResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := readAllLimited(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var u claudeUsageResp
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("parse usage response failed: %w (body: %s)", err, string(body))
	}
	return &u, nil
}
