package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

const (
	codexUsageURL      = "https://chatgpt.com/backend-api/wham/usage"
	codexResetCardsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
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
	SpendControl          *codexSpendControl `json:"spend_control"`
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

type codexSpendControl struct {
	Reached         bool `json:"reached"`
	IndividualLimit *struct {
		Unit              string          `json:"unit"`
		Limit             json.RawMessage `json:"limit"`
		Used              json.RawMessage `json:"used"`
		Remaining         json.RawMessage `json:"remaining"`
		UsedPercent       json.RawMessage `json:"used_percent"`
		ResetAt           int64           `json:"reset_at"`
		ResetAfterSeconds int64           `json:"reset_after_seconds"`
	} `json:"individual_limit"`
}

type codexResetCardsResponse struct {
	AvailableCount *int `json:"available_count"`
	Credits        []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`     // "available" | "redeemed" | "expired" | ...
		ExpiresAt string `json:"expires_at"` // RFC3339
	} `json:"credits"`
}

type codexResetCards struct {
	count    int
	expiries []int64
	cards    []model.CodexResetCard
}

// CodexClient reads usage and redeems banked resets for a single ChatGPT login.
// All calls use the same account header and Codex ACP proxy.
type CodexClient struct {
	http        *http.Client
	accessToken string
	workspaceID string
}

func NewCodexClient(env map[string]string, accessToken, workspaceID string) *CodexClient {
	return &CodexClient{
		http: &http.Client{
			Timeout:   25 * time.Second,
			Transport: proxyTransport(env),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		accessToken: accessToken,
		workspaceID: workspaceID,
	}
}

func (c *CodexClient) Close() {
	c.http.CloseIdleConnections()
}

func (c *CodexClient) request(ctx context.Context, method, url, operation string, payload []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Accept", "application/json")
	if c.workspaceID != "" {
		req.Header.Set("ChatGPT-Account-Id", c.workspaceID)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s failed: %w", operation, err)
	}
	defer resp.Body.Close()

	body, err := readAllLimited(resp)
	if err != nil {
		return fmt.Errorf("read %s response failed: %w", operation, err)
	}
	safeBody := func() string {
		return strings.ReplaceAll(string(body), c.accessToken, "[redacted]")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d: %s", operation, resp.StatusCode, safeBody())
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("parse %s response failed: %w (body: %s)", operation, err, safeBody())
	}
	return nil
}

func (s *serviceImpl) CodexUsage(ctx context.Context) (*model.CodexUsage, error) {
	env := s.codexACPEnv()
	home, err := typepath.CodexHome(env)
	if err != nil {
		return nil, fmt.Errorf("get home dir failed: %w", err)
	}
	authPath := filepath.Join(home, "auth.json")
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

	// Fetch the Codex CLI version in parallel so it adds no serial latency.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	verCh := s.codexVersionAsync(ctx)
	client := NewCodexClient(env, auth.Tokens.AccessToken, auth.Tokens.AccountID)
	defer client.Close()
	snapshot, err := client.Usage(ctx)
	if err != nil {
		return nil, err
	}
	snapshot.Usage.Version = <-verCh
	return &snapshot.Usage, nil
}

func (c *CodexClient) Usage(ctx context.Context) (*model.CodexAccountUsageResponse, error) {
	// Supplementary card details may fail independently of the quota read.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type cardResult struct {
		details codexResetCards
		err     error
	}
	cardsCh := make(chan cardResult, 1)
	go func() {
		details, err := c.readResetCards(ctx)
		cardsCh <- cardResult{details: details, err: err}
	}()

	quota, err := c.Quota(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := &model.CodexAccountUsageResponse{Usage: quota}
	cards := <-cardsCh
	if cards.err != nil {
		logger.Warnf(ctx, "[agent.usage] %v", cards.err)
		snapshot.ResetCardsError = cards.err.Error()
	} else {
		snapshot.Usage.ResetCredits = cards.details.count
		snapshot.Usage.ResetCreditExpiries = cards.details.expiries
		snapshot.ResetCards = cards.details.cards
	}
	return snapshot, nil
}

func (c *CodexClient) Quota(ctx context.Context) (model.CodexUsage, error) {
	var data codexUsageResp
	if err := c.request(ctx, http.MethodGet, codexUsageURL, "ChatGPT usage", nil, &data); err != nil {
		return model.CodexUsage{}, err
	}
	return model.CodexUsage{
		Email:           data.Email,
		PlanType:        data.PlanType,
		PrimaryWindow:   toUsageWindow(data.RateLimit.PrimaryWindow),
		SecondaryWindow: toUsageWindow(data.RateLimit.SecondaryWindow),
		SpendLimit:      data.SpendControl.usageLimit(),
		ResetCredits:    data.RateLimitResetCredits.AvailableCount,
	}, nil
}

func (s *codexSpendControl) usageLimit() *model.CodexSpendLimit {
	if s == nil {
		return nil
	}
	quota := model.CodexSpendLimit{Reached: s.Reached}
	if limit := s.IndividualLimit; limit != nil {
		quota.Unit = strings.TrimSpace(limit.Unit)
		quota.Limit = codexSpendAmount(limit.Limit)
		quota.Used = codexSpendAmount(limit.Used)
		quota.Remaining = codexSpendAmount(limit.Remaining)
		quota.UsedPercent = codexSpendAmount(limit.UsedPercent)
		if quota.Limit != nil && quota.Used != nil {
			if *quota.Limit > 0 {
				percent := math.Min(100, *quota.Used / *quota.Limit * 100)
				quota.UsedPercent = &percent
			}
			if quota.Remaining == nil {
				remaining := max(0, *quota.Limit-*quota.Used)
				quota.Remaining = &remaining
			}
		}
		quota.ResetAt = limit.ResetAt
		if quota.ResetAt <= 0 && limit.ResetAfterSeconds > 0 {
			quota.ResetAt = time.Now().Unix() + limit.ResetAfterSeconds
		}
	}
	if quota.Limit == nil && quota.Used == nil && quota.Remaining == nil && quota.UsedPercent == nil && !quota.Reached {
		return nil
	}
	return &quota
}

// The usage endpoint may encode spend amounts as strings or JSON numbers.
func codexSpendAmount(raw json.RawMessage) *float64 {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil
		}
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return nil
	}
	return &amount
}

func (c *CodexClient) readResetCards(ctx context.Context) (codexResetCards, error) {
	var data codexResetCardsResponse
	if err := c.request(ctx, http.MethodGet, codexResetCardsURL, "ChatGPT reset cards", nil, &data); err != nil {
		return codexResetCards{}, err
	}
	if data.AvailableCount == nil && data.Credits == nil {
		return codexResetCards{}, fmt.Errorf("ChatGPT reset cards response had neither available_count nor credits")
	}

	var result codexResetCards
	now := time.Now()
	for _, card := range data.Credits {
		if card.Status != "available" {
			continue
		}
		var expiresAt int64
		if card.ExpiresAt != "" {
			expiry, err := time.Parse(time.RFC3339, card.ExpiresAt)
			if err != nil {
				logger.Warnf(ctx, "[agent.usage] parse reset card expiry: %v", err)
			} else {
				if !expiry.After(now) {
					continue
				}
				expiresAt = expiry.Unix()
				result.expiries = append(result.expiries, expiresAt)
			}
		}
		result.count++
		if card.ID != "" {
			result.cards = append(result.cards, model.CodexResetCard{ID: card.ID, ExpiresAt: expiresAt})
		}
	}
	if data.AvailableCount != nil {
		result.count = max(*data.AvailableCount, result.count)
	}
	slices.Sort(result.expiries)
	slices.SortStableFunc(result.cards, func(a, b model.CodexResetCard) int {
		switch {
		case a.ExpiresAt == b.ExpiresAt:
			return 0
		case b.ExpiresAt == 0 || a.ExpiresAt != 0 && a.ExpiresAt < b.ExpiresAt:
			return -1
		default:
			return 1
		}
	})
	return result, nil
}

func (c *CodexClient) ResetCards(ctx context.Context) ([]model.CodexResetCard, int, error) {
	result, err := c.readResetCards(ctx)
	return result.cards, result.count, err
}

func (c *CodexClient) RedeemReset(ctx context.Context, creditID, operationID string) (model.CodexAccountResetResponse, error) {
	// The selected credit and the operation UUID are both required by the
	// ChatGPT consume endpoint. Reusing the UUID makes a retry idempotent.
	body, err := json.Marshal(map[string]string{"credit_id": creditID, "redeem_request_id": operationID})
	if err != nil {
		return model.CodexAccountResetResponse{}, err
	}
	var data struct {
		Code         string `json:"code"`
		WindowsReset int    `json:"windows_reset"`
	}
	if err := c.request(ctx, http.MethodPost, codexResetCardsURL+"/consume", "ChatGPT reset card redemption", body, &data); err != nil {
		return model.CodexAccountResetResponse{}, err
	}
	if data.Code == "" {
		return model.CodexAccountResetResponse{}, fmt.Errorf("ChatGPT reset card redemption returned no outcome")
	}
	return model.CodexAccountResetResponse{
		Outcome:      strings.ReplaceAll(data.Code, c.accessToken, "[redacted]"),
		WindowsReset: data.WindowsReset,
	}, nil
}

func toUsageWindow(w *codexWindow) *model.UsageWindow {
	if w == nil {
		return nil
	}
	resetAt := w.ResetAt
	if resetAt <= 0 && w.ResetAfterSeconds > 0 {
		resetAt = time.Now().Unix() + w.ResetAfterSeconds
	}
	return &model.UsageWindow{
		UsedPercent:        w.UsedPercent,
		LimitWindowSeconds: w.LimitWindowSeconds,
		ResetAfterSeconds:  w.ResetAfterSeconds,
		ResetAt:            resetAt,
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
