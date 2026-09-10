package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/fanlv/quartet/types/model"
)

const (
	// codebuddyQuotaURL / codebuddyUsageURL are the Token 看板 OpenAPI endpoints
	// (iWiki《Token看板数据开放接口》). Both resolve the user identity from the
	// Bearer personal access token; amounts are CNY strings for the current
	// calendar month.
	codebuddyQuotaURL = "https://openapi.token.woa.com/api/v1/quota/total"
	codebuddyUsageURL = "https://openapi.token.woa.com/api/v1/usage/total"
	// codebuddyPATEnv is the agreed key the user configures in the CodeBuddy
	// agent's ACP env vars (设置 → Agent 管理 → CodeBuddy → 环境变量). Its value
	// is a TAI personal access token (tai.it.woa.com/user/pat) authorized for
	// the Token 看板 app (app id "token"). It is a user-managed ACP env entry,
	// not a quartet process env var, so it does not belong in types/consts.
	codebuddyPATEnv = "CODEBUDDY_TOKEN_DASHBOARD_PAT"
	// codebuddyCacheTTL bounds how long a successful snapshot is reused. The
	// spend is a monthly cumulative figure, so a few minutes of staleness is
	// invisible while sparing the OpenAPI on every agent switch.
	codebuddyCacheTTL = 5 * time.Minute
)

// codebuddyAPIResp is the shared envelope of the Token 看板 OpenAPI. Quota may
// be "-" in special states (e.g. unlimited or temporarily hidden); Cost is a
// numeric string.
type codebuddyAPIResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Username string `json:"username"`
		Quota    string `json:"quota"`
		Cost     string `json:"cost"`
	} `json:"data"`
}

// CodeBuddyUsage returns the CodeBuddy CLI version plus the monthly quota and
// current-month spend from the Token 看板 OpenAPI, authenticated with the PAT
// configured in the CodeBuddy agent's ACP env vars. The PAT is opt-in: when it
// is not configured, the method returns a nil snapshot (and no error) so
// callers treat CodeBuddy as having no quota view instead of a failure — no
// upstream request is made either. A short in-process cache (keyed by the PAT,
// so rotating the token invalidates it) absorbs repeated polls; the mutex spans
// the upstream calls so concurrent requests share one in-flight fetch. The
// cache holds the version too, so a hit skips the `codebuddy --version` probe.
func (s *serviceImpl) CodeBuddyUsage(ctx context.Context) (*model.CodeBuddyUsage, error) {
	env := s.codebuddyACPEnv()
	token := env[codebuddyPATEnv]
	if token == "" {
		return nil, nil
	}

	s.codebuddyMu.Lock()
	defer s.codebuddyMu.Unlock()
	if s.codebuddyCache != nil && s.codebuddyToken == token && time.Since(s.codebuddyCachedAt) < codebuddyCacheTTL {
		cached := *s.codebuddyCache
		return &cached, nil
	}

	// The version probe runs in parallel with the upstream calls —
	// supplementary, must not add serial latency; the buffered channel keeps
	// the goroutine from blocking on early error returns.
	verCh := make(chan string, 1)
	go func() { verCh <- s.binVersion(ctx, "codebuddy") }()

	client := &http.Client{
		Timeout:   25 * time.Second,
		Transport: proxyTransport(env),
	}
	quota, err := codebuddyFetch(ctx, client, token, codebuddyQuotaURL)
	if err != nil {
		return nil, fmt.Errorf("query Token 看板 quota failed: %w", err)
	}
	usage, err := codebuddyFetch(ctx, client, token, codebuddyUsageURL)
	if err != nil {
		return nil, fmt.Errorf("query Token 看板 usage failed: %w", err)
	}

	u := &model.CodeBuddyUsage{
		Version:    <-verCh,
		Username:   quota.Data.Username,
		QuotaText:  quota.Data.Quota,
		CostText:   usage.Data.Cost,
	}
	if q, ok := codebuddyParseAmount(quota.Data.Quota); ok {
		u.Quota = &q
	}
	if c, ok := codebuddyParseAmount(usage.Data.Cost); ok {
		u.Cost = &c
	}
	// Derive remaining / percent only when both sides are numeric: a "-"
	// quota marks a special state (e.g. unlimited) that must not enter math.
	if u.Quota != nil && u.Cost != nil && *u.Quota > 0 {
		remaining := *u.Quota - *u.Cost
		percent := *u.Cost / *u.Quota * 100
		u.Remaining = &remaining
		u.UsedPercent = &percent
	}

	s.codebuddyCache = u
	s.codebuddyToken = token
	s.codebuddyCachedAt = time.Now()
	return u, nil
}

// codebuddyACPEnv returns the effective env for the CodeBuddy ACP agent,
// including the runtime default that points the adapter at the installed CLI.
func (s *serviceImpl) codebuddyACPEnv() map[string]string {
	return s.effectiveACPEnv("codebuddy")
}

// codebuddyFetch performs one GET against the Token 看板 OpenAPI with the PAT.
// Errors carry the full upstream status / body / API message per the project's
// show-everything error convention.
func codebuddyFetch(ctx context.Context, client *http.Client, token, url string) (*codebuddyAPIResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var r codebuddyAPIResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse response failed: %w (body: %s)", err, string(body))
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("API code %d: %s", r.Code, r.Message)
	}
	return &r, nil
}

// codebuddyParseAmount converts the API's string amounts ("1400", "625.7965")
// to float. "-" or any unparseable value reports ok=false and stays out of
// numeric math.
func codebuddyParseAmount(v string) (float64, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
