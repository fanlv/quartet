package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	// claudeDefaultBase is used when ~/.claude/settings.json has no
	// ANTHROPIC_BASE_URL. This host is directly reachable (it is in no_proxy),
	// so the Claude requests never go through a proxy.
	claudeDefaultBase     = "https://9mwkeekm.fn.sinf.net"
	claudeSettingsRelPath = ".claude/settings.json"
	claudeRetries         = 3
)

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
	// Fetch the Claude CLI version in parallel so it adds no serial latency.
	verCh := s.claudeVersionAsync(ctx)

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

	// Direct client (no proxy): explicitly disable proxy so a global proxy
	// setting in the process env can't misroute it. Force IPv6 because the sinf IPv4
	// load-balancer path intermittently stalls during the TLS handshake.
	dialer := &net.Dialer{}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _ string, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp6", address)
			},
		},
	}

	total, err := s.claudeCost(ctx, client, base, suffix, "")
	if err != nil {
		return nil, fmt.Errorf("query total usage failed: %w", err)
	}
	// The claude_code daily usage buckets are keyed by UTC date, so "today"
	// must be computed in UTC. Using local time (UTC+8) would query tomorrow's
	// empty bucket during the 00:00–08:00 local window and report $0.
	today := time.Now().UTC().Format("2006-01-02")
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

// claudeVersionAsync runs the version probe in parallel with the usage calls.
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
	out, err := executil.CommandContext(cctx, parts[0], args...).Output()
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
