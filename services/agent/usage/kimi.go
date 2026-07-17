package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/model"
)

const (
	kimiBin = "kimi"
	// kimiUsageURL is the Kimi Code quota endpoint (Bearer-authenticated).
	kimiUsageURL = "https://api.kimi.com/coding/v1/usages"
	// kimiTokenURL refreshes an expired access token from the refresh token.
	kimiTokenURL = "https://auth.kimi.com/api/oauth/token"
	// kimiClientID is the public OAuth client id the kimi CLI itself uses.
	kimiClientID = "17e5f671-d194-4dfb-9706-5516cb48c098"
	// kimiCredsRelPath is the credential file under the kimi home dir.
	kimiCredsRelPath = "credentials/kimi-code.json"
	// kimiRefreshLeeway refreshes the access token this long before expires_at.
	kimiRefreshLeeway = 30 * time.Second
)

// kimiCredentials mirrors the kimi CLI's credentials/kimi-code.json.
type kimiCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"` // unix seconds
	Scope        string `json:"scope,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

// kimiNumber accepts the API's string-or-number quota fields ("100" or 100).
type kimiNumber float64

func (n *kimiNumber) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	var v float64
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return fmt.Errorf("invalid number %q", s)
	}
	*n = kimiNumber(v)
	return nil
}

// kimiQuota is one quota bucket (usage / limits[i].detail / totalQuota).
type kimiQuota struct {
	Limit        kimiNumber `json:"limit"`
	Used         kimiNumber `json:"used"`
	Remaining    kimiNumber `json:"remaining"`
	ResetTime    string     `json:"resetTime"` // RFC3339; reset_at / resetAt also seen
	ResetAtSnake string     `json:"reset_at"`
	ResetAtCamel string     `json:"resetAt"`
}

type kimiUsageResp struct {
	Usage  *kimiQuota `json:"usage"` // 7-day quota
	Limits []struct {
		kimiQuota // fallback: some responses carry the quota on the limit entry itself
		Window    *struct {
			Duration int64  `json:"duration"`
			TimeUnit string `json:"timeUnit"` // e.g. "TIME_UNIT_MINUTE"
		} `json:"window"`
		Detail *kimiQuota `json:"detail"`
	} `json:"limits"`
	Parallel struct {
		Limit kimiNumber `json:"limit"`
	} `json:"parallel"`
	TotalQuota *kimiQuota `json:"totalQuota"` // cumulative pool, no reset
}

// KimiUsage reads the Kimi Code quota from the kimi usages endpoint, using the
// local kimi CLI's OAuth credentials (refreshing them when expired). It mirrors
// the TokenTracker reference: the response carries a 7-day window (usage), a
// 5-hour window (limits[0].detail), and a cumulative pool (totalQuota).
func (s *serviceImpl) KimiUsage(ctx context.Context) (*model.KimiUsage, error) {
	// The version probe runs in parallel — supplementary, must not add serial
	// latency; the buffered channel keeps the goroutine from blocking on early
	// error returns.
	verCh := make(chan string, 1)
	go func() { verCh <- s.binVersion(ctx, kimiBin) }()

	credsPath, creds, err := loadKimiCredentials()
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 25 * time.Second}
	token := creds.AccessToken
	if kimiTokenExpired(creds) && creds.RefreshToken != "" {
		token, err = refreshKimiToken(ctx, client, credsPath, creds)
		if err != nil {
			return nil, err
		}
	}

	body, err := fetchKimiUsage(ctx, client, token)
	if err == errKimiUnauthorized && creds.RefreshToken != "" {
		if token, err = refreshKimiToken(ctx, client, credsPath, creds); err != nil {
			return nil, err
		}
		body, err = fetchKimiUsage(ctx, client, token)
	}
	if err != nil {
		return nil, err
	}

	var u kimiUsageResp
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("parse kimi usage response failed: %w (body: %s)", err, string(body))
	}

	fiveHourSeconds := int64(5 * 3600)
	var detail *kimiQuota
	if len(u.Limits) > 0 {
		first := u.Limits[0]
		detail = first.Detail
		if detail == nil && first.Limit > 0 {
			q := first.kimiQuota
			detail = &q
		}
		if first.Window != nil {
			if secs := kimiWindowSeconds(first.Window.Duration, first.Window.TimeUnit); secs > 0 {
				fiveHourSeconds = secs
			}
		}
	}

	return &model.KimiUsage{
		Version:       <-verCh,
		ParallelLimit: int64(u.Parallel.Limit),
		Weekly:        kimiUsageWindow(u.Usage, 7*24*3600),
		FiveHour:      kimiUsageWindow(detail, fiveHourSeconds),
		Total:         kimiUsageWindow(u.TotalQuota, 0),
	}, nil
}

// kimiWindowSeconds converts the API's duration + timeUnit pair to seconds.
func kimiWindowSeconds(duration int64, timeUnit string) int64 {
	if duration <= 0 {
		return 0
	}
	switch timeUnit {
	case "TIME_UNIT_SECOND":
		return duration
	case "TIME_UNIT_MINUTE":
		return duration * 60
	case "TIME_UNIT_HOUR":
		return duration * 3600
	case "TIME_UNIT_DAY":
		return duration * 86400
	}
	return 0
}

// kimiUsageWindow builds a UsageWindow from a quota bucket: used percent from
// used/limit (derived from remaining when used is absent) and reset time from
// whichever reset field the API populated. windowSeconds is the bucket's known
// duration (0 for the cumulative pool). Returns nil without a positive limit.
func kimiUsageWindow(q *kimiQuota, windowSeconds int64) *model.UsageWindow {
	if q == nil || q.Limit <= 0 {
		return nil
	}
	used := float64(q.Used)
	if used == 0 && q.Remaining > 0 {
		used = float64(q.Limit) - float64(q.Remaining)
	}
	if used < 0 {
		used = 0
	}
	w := &model.UsageWindow{
		UsedPercent:        used / float64(q.Limit) * 100,
		LimitWindowSeconds: windowSeconds,
	}
	reset := q.ResetTime
	if reset == "" {
		reset = q.ResetAtSnake
	}
	if reset == "" {
		reset = q.ResetAtCamel
	}
	if t, err := time.Parse(time.RFC3339, reset); err == nil {
		w.ResetAt = t.Unix()
	}
	return w
}

// kimiHomeDir resolves the kimi CLI home dir, preferring the official Kimi
// Code dir (~/.kimi-code) when it holds a credential file, then the legacy
// kimi-cli dir (~/.kimi). KIMI_HOME / KIMI_CODE_HOME override both.
func kimiHomeDir() string {
	if explicit := strings.TrimSpace(os.Getenv("KIMI_HOME")); explicit != "" {
		return explicit
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	codeHome := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME"))
	if codeHome == "" {
		codeHome = filepath.Join(home, ".kimi-code")
	}
	if raw, err := os.ReadFile(filepath.Join(codeHome, kimiCredsRelPath)); err == nil {
		var creds kimiCredentials
		if json.Unmarshal(raw, &creds) == nil && creds.AccessToken != "" {
			return codeHome
		}
	}
	return filepath.Join(home, ".kimi")
}

// loadKimiCredentials reads credentials/kimi-code.json from the resolved kimi
// home. The error carries the full path so a missing login is diagnosable.
func loadKimiCredentials() (string, *kimiCredentials, error) {
	home := kimiHomeDir()
	if home == "" {
		return "", nil, fmt.Errorf("resolve home dir failed")
	}
	credsPath := filepath.Join(home, kimiCredsRelPath)
	raw, err := os.ReadFile(credsPath)
	if err != nil {
		return "", nil, fmt.Errorf("read %s failed (not logged in? run `kimi` to authenticate): %w", credsPath, err)
	}
	var creds kimiCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return "", nil, fmt.Errorf("parse %s failed: %w", credsPath, err)
	}
	if creds.AccessToken == "" {
		return "", nil, fmt.Errorf("%s: access_token is empty (run `kimi` to authenticate)", credsPath)
	}
	return credsPath, &creds, nil
}

func kimiTokenExpired(creds *kimiCredentials) bool {
	return creds.ExpiresAt > 0 && time.Unix(creds.ExpiresAt, 0).Add(-kimiRefreshLeeway).Before(time.Now())
}

var errKimiUnauthorized = fmt.Errorf("kimi access token unauthorized (HTTP 401)")

// refreshKimiToken exchanges the refresh token for a new access token and
// persists the updated credentials back to the file they were loaded from.
func refreshKimiToken(ctx context.Context, client *http.Client, credsPath string, creds *kimiCredentials) (string, error) {
	form := url.Values{
		"client_id":     {kimiClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kimiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Msh-Platform", "kimi_cli")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request kimi token refresh failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("kimi token refresh returned HTTP %d (not logged in? run `kimi` to authenticate): %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("kimi token refresh returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("parse kimi token refresh response failed: %v (body: %s)", err, string(body))
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 900
	}

	next := &kimiCredentials{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
		Scope:        tok.Scope,
		TokenType:    tok.TokenType,
		ExpiresIn:    tok.ExpiresIn,
	}
	if next.RefreshToken == "" {
		next.RefreshToken = creds.RefreshToken
	}
	if next.Scope == "" {
		next.Scope = "kimi-code"
	}
	if next.TokenType == "" {
		next.TokenType = "Bearer"
	}
	if raw, err := json.MarshalIndent(next, "", "  "); err == nil {
		// Best-effort persist; a failed write only means the next poll refreshes
		// again, so it must not fail the usage request.
		_ = os.WriteFile(credsPath, raw, 0o600)
	}
	*creds = *next
	return next.AccessToken, nil
}

// fetchKimiUsage GETs the usages endpoint with the given access token.
func fetchKimiUsage(ctx context.Context, client *http.Client, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kimiUsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request kimi usage failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errKimiUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kimi usage returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
