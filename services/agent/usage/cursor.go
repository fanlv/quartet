package usage

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	// cursorSummaryURL is cursor.com's usage-summary endpoint. It is
	// authenticated by the WorkosCursorSessionToken web cookie built from the
	// cursor-agent CLI's locally stored login token.
	cursorSummaryURL = "https://cursor.com/api/usage-summary"
	// cursorSandAccessURL / cursorSandUsageURL are Cursor's first-party Connect
	// RPCs for the Grok Bot (internal codename "Sand") quota. They are
	// authenticated by the raw JWT as a Bearer token and answer in protobuf.
	cursorSandAccessURL = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetSandAccessStatus"
	cursorSandUsageURL  = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetSandUsageStatus"
	// cursorRPCMaxResponseBytes caps the protobuf response we accept; the real
	// payloads are a few hundred bytes.
	cursorRPCMaxResponseBytes = 64 * 1024
	// cursorUserAgent / cursorReferer mirror the headers Cursor's own dashboard
	// sends; the API rejects requests without a browser-like User-Agent.
	cursorUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	cursorReferer   = "https://www.cursor.com/settings"
)

// cursorAuthFile is the cursor-agent CLI's login-token file (auth.json). The
// accessToken is the raw JWT used both for the web cookie and the Bearer RPCs.
type cursorAuthFile struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// cursorUsageSummary is the usage-summary payload. The lane percents use
// pointers so a null/absent value (window dropped) stays distinct from a real
// 0 (window at 0%); plan/onDemand used/limit are raw counters Cursor reports
// in cents or request units depending on the account.
type cursorUsageSummary struct {
	BillingCycleStart string `json:"billingCycleStart"`
	BillingCycleEnd   string `json:"billingCycleEnd"`
	MembershipType    string `json:"membershipType"`
	LimitType         string `json:"limitType"`
	IndividualUsage   struct {
		Plan struct {
			Used             float64  `json:"used"`
			Limit            float64  `json:"limit"`
			AutoPercentUsed  *float64 `json:"autoPercentUsed"`
			ApiPercentUsed   *float64 `json:"apiPercentUsed"`
			TotalPercentUsed *float64 `json:"totalPercentUsed"`
		} `json:"plan"`
		OnDemand struct {
			Used  float64 `json:"used"`
			Limit float64 `json:"limit"`
		} `json:"onDemand"`
	} `json:"individualUsage"`
	TeamUsage struct {
		OnDemand struct {
			Used  float64 `json:"used"`
			Limit float64 `json:"limit"`
		} `json:"onDemand"`
	} `json:"teamUsage"`
}

// CursorUsage returns the Cursor plan quota and cursor-agent CLI version. The
// web summary provides the three monthly billing-cycle windows; the two Grok
// Bot RPCs are fetched in parallel and their failures are isolated (accounts
// without Grok Bot must keep the three plan windows).
func (s *serviceImpl) CursorUsage(ctx context.Context) (*model.CursorUsage, error) {
	// Version probe runs in parallel so it adds no serial latency.
	verCh := make(chan string, 1)
	go func() { verCh <- s.binVersion(ctx, "cursor-agent") }()

	authPath := cursorCLIAuthPath()
	if authPath == "" {
		return nil, errors.New("resolve cursor-agent auth file path failed: home dir unavailable")
	}
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("read %s failed: %w (log in with cursor-agent first)", authPath, err)
	}
	var auth cursorAuthFile
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, fmt.Errorf("parse %s failed: %w", authPath, err)
	}
	if auth.AccessToken == "" {
		return nil, fmt.Errorf("%s: accessToken is empty", authPath)
	}

	userID, err := readCursorUserID(auth.AccessToken)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout:   25 * time.Second,
		Transport: proxyTransport(s.effectiveACPEnv("cursor-agent")),
	}

	// The Grok Bot RPCs are independent of the web summary; fetch all three
	// concurrently. Sand failures only log a warning and yield nil, so older
	// accounts keep their three monthly windows.
	sandAccessCh := s.cursorSandAccessAsync(ctx, client, auth.AccessToken)
	sandUsageCh := s.cursorSandUsageAsync(ctx, client, auth.AccessToken)

	summary, err := fetchCursorUsageSummary(ctx, client, cursorWebCookie(userID, auth.AccessToken))
	if err != nil {
		return nil, err
	}

	usage := cursorUsageFromSummary(summary, time.Now())
	usage.Version = <-verCh
	usage.GrokBotWindow = cursorGrokWindow(<-sandAccessCh, <-sandUsageCh, time.Now())
	return usage, nil
}

// readCursorUserID resolves the Cursor user id for the web session cookie:
// cli-config.json's authInfo.authId first, then the JWT sub claim. Both go
// through normalizeCursorSubject, which mirrors the subject formats Cursor
// accepts in the WorkosCursorSessionToken cookie.
func readCursorUserID(accessToken string) (string, error) {
	if id := normalizeCursorSubject(readCursorCliConfigAuthID()); id != "" {
		return id, nil
	}
	if id := normalizeCursorSubject(cursorJWTSubject(accessToken)); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("determine Cursor user id failed: no authInfo.authId in %s and no usable sub claim in the auth token", cursorCLIConfigPath())
}

func cursorCLIConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cursor", "cli-config.json")
}

func readCursorCliConfigAuthID() string {
	raw, err := os.ReadFile(cursorCLIConfigPath())
	if err != nil {
		return ""
	}
	var cfg struct {
		AuthInfo struct {
			AuthID string `json:"authId"`
		} `json:"authInfo"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return cfg.AuthInfo.AuthID
}

// cursorNativeSubjectRe matches the native Cursor subject "auth0|user_XXXXX",
// which the cookie wants as the bare "user_XXXXX".
var cursorNativeSubjectRe = regexp.MustCompile(`\|(user_[A-Za-z0-9_]+)$`)

// cursorWorkOSSubjectRe matches WorkOS-bridged OAuth subjects Cursor accepts
// verbatim in the session cookie (verified against cursor.com by TokenTracker,
// issue #88).
var cursorWorkOSSubjectRe = regexp.MustCompile(`^(google-oauth2|github|oidc|auth0)\|[^|]+$`)

func normalizeCursorSubject(subject string) string {
	if subject == "" {
		return ""
	}
	if m := cursorNativeSubjectRe.FindStringSubmatch(subject); m != nil {
		return m[1]
	}
	if cursorWorkOSSubjectRe.MatchString(subject) {
		return subject
	}
	return ""
}

func cursorJWTSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Sub
}

// cursorWebCookie builds the cursor.com session cookie from the user id and
// the raw JWT. Only the "::" separator is percent-encoded: the JWT is
// base64url, the normalized user id is cookie-safe, and Cursor's frontend
// sends WorkOS subjects (e.g. "github|123") with a literal "|".
func cursorWebCookie(userID, accessToken string) string {
	return "WorkosCursorSessionToken=" + userID + "%3A%3A" + accessToken
}

func fetchCursorUsageSummary(ctx context.Context, client *http.Client, cookie string) (*cursorUsageSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cursorSummaryURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", cursorReferer)
	req.Header.Set("User-Agent", cursorUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Cursor usage summary failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Cursor session expired (HTTP %d) — re-login in Cursor to refresh", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Cursor usage summary returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	var u cursorUsageSummary
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("parse Cursor usage summary failed: %w (body: %s)", err, string(body))
	}
	return &u, nil
}

// cursorUsageFromSummary converts the usage-summary payload into the plan
// windows. The primary percent follows TokenTracker's preference chain:
// totalPercentUsed, then the Auto/API lane average, then either lane alone,
// then raw plan / individual-onDemand / team-onDemand counter ratios — with
// team/enterprise accounts preferring the pooled teamUsage.onDemand headline
// when the individual lanes read empty.
func cursorUsageFromSummary(s *cursorUsageSummary, now time.Time) *model.CursorUsage {
	plan := s.IndividualUsage.Plan
	auto := clampCursorPercent(plan.AutoPercentUsed)
	api := clampCursorPercent(plan.ApiPercentUsed)

	planPercent := clampCursorPercent(plan.TotalPercentUsed)
	if planPercent == nil {
		switch {
		case auto != nil && api != nil:
			planPercent = clampCursorPercent(cursorF64ptr((*auto + *api) / 2))
		case api != nil:
			planPercent = api
		case auto != nil:
			planPercent = auto
		default:
			planPercent = cursorCentsPercent(plan.Used, plan.Limit)
		}
	}
	indOnDemand := s.IndividualUsage.OnDemand
	teamOnDemand := s.TeamUsage.OnDemand
	if planPercent == nil {
		planPercent = cursorCentsPercent(indOnDemand.Used, indOnDemand.Limit)
	}
	if planPercent == nil {
		planPercent = cursorCentsPercent(teamOnDemand.Used, teamOnDemand.Limit)
	}
	// Individual lanes can read 0% while the real usage sits in a pool; only a
	// strictly positive pooled ratio replaces a 0.
	if planPercent != nil && *planPercent == 0 {
		if v := cursorCentsPercent(indOnDemand.Used, indOnDemand.Limit); v != nil && *v > 0 {
			planPercent = v
		}
	}
	if planPercent != nil && *planPercent == 0 {
		if v := cursorCentsPercent(teamOnDemand.Used, teamOnDemand.Limit); v != nil && *v > 0 {
			planPercent = v
		}
	}
	// Team / enterprise: the headline usage is the pooled quota
	// (teamUsage.onDemand), not the individual lanes.
	preferTeamPool := s.LimitType == "team" || s.MembershipType == "enterprise" || s.MembershipType == "team"
	if preferTeamPool {
		if v := cursorCentsPercent(teamOnDemand.Used, teamOnDemand.Limit); v != nil && (planPercent == nil || *planPercent == 0) {
			planPercent = v
		}
	}

	var resetAt int64
	if end, err := time.Parse(time.RFC3339, s.BillingCycleEnd); err == nil {
		resetAt = end.Unix()
	}
	var cycleSeconds int64
	if start, err := time.Parse(time.RFC3339, s.BillingCycleStart); err == nil {
		if end, err := time.Parse(time.RFC3339, s.BillingCycleEnd); err == nil && end.After(start) {
			cycleSeconds = int64(math.Round(end.Sub(start).Seconds()))
		}
	}

	return &model.CursorUsage{
		MembershipType:  s.MembershipType,
		PrimaryWindow:   cursorWindow(planPercent, resetAt, cycleSeconds, now),
		SecondaryWindow: cursorWindow(auto, resetAt, cycleSeconds, now),
		TertiaryWindow:  cursorWindow(api, resetAt, cycleSeconds, now),
	}
}

// cursorWindow builds one billing-cycle window; nil when the percent is absent
// (the API distinguishes an omitted lane from a real 0).
func cursorWindow(percent *float64, resetAt, cycleSeconds int64, now time.Time) *model.UsageWindow {
	p := clampCursorPercent(percent)
	if p == nil {
		return nil
	}
	return &model.UsageWindow{
		UsedPercent:        *p,
		LimitWindowSeconds: cycleSeconds,
		ResetAt:            resetAt,
		ResetAfterSeconds:  cursorResetAfter(resetAt, now),
	}
}

// cursorGrokWindow builds the Grok Bot window from the two Sand RPC results.
// Nil when the account has no Grok Bot access, the included limit is zero, or
// the percent / reset time is unusable.
func cursorGrokWindow(access *cursorSandAccess, usage *cursorSandUsage, now time.Time) *model.UsageWindow {
	if access == nil || !access.granted || usage == nil || usage.includedLimitZero {
		return nil
	}
	var percent *float64
	if usage.hasUsagePercent {
		percent = cursorF64ptr(usage.usagePercent)
	}
	p := clampCursorPercent(percent)
	if p == nil || usage.nextResetAt <= 0 {
		return nil
	}
	w := &model.UsageWindow{
		UsedPercent:       *p,
		ResetAt:           usage.nextResetAt,
		ResetAfterSeconds: cursorResetAfter(usage.nextResetAt, now),
	}
	if usage.currentPeriodStart > 0 && usage.nextResetAt > usage.currentPeriodStart {
		w.LimitWindowSeconds = usage.nextResetAt - usage.currentPeriodStart
	}
	return w
}

func cursorResetAfter(resetAt int64, now time.Time) int64 {
	if d := resetAt - now.Unix(); d > 0 {
		return d
	}
	return 0
}

func cursorF64ptr(v float64) *float64 { return &v }

// clampCursorPercent clamps a percent into [0,100]; nil (or non-finite) stays
// nil so an omitted lane produces no window.
func clampCursorPercent(v *float64) *float64 {
	if v == nil {
		return nil
	}
	p := *v
	if math.IsNaN(p) || math.IsInf(p, 0) {
		return nil
	}
	switch {
	case p <= 0:
		p = 0
	case p >= 100:
		p = 100
	}
	return &p
}

// cursorCentsPercent derives a percent from raw used/limit counters. Nil when
// the ratio is unusable (no limit, or non-finite counters).
func cursorCentsPercent(used, limit float64) *float64 {
	if !(limit > 0) { // also rejects NaN
		return nil
	}
	return clampCursorPercent(cursorF64ptr(used / limit * 100))
}

// ── Grok Bot ("Sand") Connect RPCs ──

// cursorSandAccess is the decoded GetSandAccessStatus: field 1 is the state
// enum, granted == 1.
type cursorSandAccess struct {
	granted bool
}

// cursorSandUsage is the decoded GetSandUsageStatus: field 1/2 are
// google.protobuf.Timestamp (period start / next reset), field 3 the usage
// percent as a fixed64 double, field 4 the includedLimitZero flag.
type cursorSandUsage struct {
	currentPeriodStart int64   // unix seconds, 0 when absent
	nextResetAt        int64   // unix seconds, 0 when absent
	usagePercent       float64 // valid only when hasUsagePercent
	hasUsagePercent    bool
	includedLimitZero  bool
}

func (s *serviceImpl) cursorSandAccessAsync(ctx context.Context, client *http.Client, accessToken string) <-chan *cursorSandAccess {
	ch := make(chan *cursorSandAccess, 1)
	go func() {
		body, err := fetchCursorSandRPC(ctx, client, cursorSandAccessURL, accessToken)
		if err != nil {
			logger.Warnf(ctx, "[agent.usage] Cursor Grok Bot access status failed: %v", err)
			ch <- nil
			return
		}
		access, err := decodeCursorSandAccess(body)
		if err != nil {
			logger.Warnf(ctx, "[agent.usage] decode Cursor Grok Bot access status failed: %v", err)
			ch <- nil
			return
		}
		ch <- access
	}()
	return ch
}

func (s *serviceImpl) cursorSandUsageAsync(ctx context.Context, client *http.Client, accessToken string) <-chan *cursorSandUsage {
	ch := make(chan *cursorSandUsage, 1)
	go func() {
		body, err := fetchCursorSandRPC(ctx, client, cursorSandUsageURL, accessToken)
		if err != nil {
			logger.Warnf(ctx, "[agent.usage] Cursor Grok Bot usage status failed: %v", err)
			ch <- nil
			return
		}
		usage, err := decodeCursorSandUsage(body)
		if err != nil {
			logger.Warnf(ctx, "[agent.usage] decode Cursor Grok Bot usage status failed: %v", err)
			ch <- nil
			return
		}
		ch <- usage
	}()
	return ch
}

// fetchCursorSandRPC posts one empty Connect-Protocol request and returns the
// raw protobuf body.
func fetchCursorSandRPC(ctx context.Context, client *http.Client, url, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Cursor session expired (HTTP %d) — re-login in Cursor to refresh", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d: %s", url, resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/proto") {
		return nil, fmt.Errorf("%s returned an unexpected response type %q", url, ct)
	}
	if len(body) > cursorRPCMaxResponseBytes {
		return nil, fmt.Errorf("%s returned an oversized response (%d bytes)", url, len(body))
	}
	return body, nil
}

// ── Minimal protobuf wire-format decoding (only what the Sand RPCs need) ──

var errCursorProtoMalformed = errors.New("Cursor API returned malformed protobuf")

// cursorProtoField is one decoded protobuf wire-format field. fixed32 values
// are skipped (advanced over) because no decoded message uses them.
type cursorProtoField struct {
	number    int
	wireType  int
	varint    uint64 // wireType 0
	fixedBits uint64 // wireType 1, raw bits (double via math.Float64frombits)
	data      []byte // wireType 2
}

func readCursorProtoVarint(b []byte, offset int) (uint64, int, error) {
	var value uint64
	var shift uint
	for i := offset; i < len(b) && i < offset+10; i++ {
		by := b[i]
		value |= uint64(by&0x7f) << shift
		if by&0x80 == 0 {
			return value, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, errCursorProtoMalformed
}

func readCursorProtoFields(b []byte) ([]cursorProtoField, error) {
	var fields []cursorProtoField
	offset := 0
	for offset < len(b) {
		tag, next, err := readCursorProtoVarint(b, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		number := int(tag >> 3)
		wireType := int(tag & 0x07)
		if number <= 0 {
			return nil, errCursorProtoMalformed
		}
		switch wireType {
		case 0:
			v, next, err := readCursorProtoVarint(b, offset)
			if err != nil {
				return nil, err
			}
			offset = next
			fields = append(fields, cursorProtoField{number: number, wireType: wireType, varint: v})
		case 1:
			if offset+8 > len(b) {
				return nil, errCursorProtoMalformed
			}
			fields = append(fields, cursorProtoField{number: number, wireType: wireType, fixedBits: binary.LittleEndian.Uint64(b[offset:])})
			offset += 8
		case 2:
			l, next, err := readCursorProtoVarint(b, offset)
			if err != nil {
				return nil, err
			}
			offset = next
			if l > uint64(len(b)-offset) {
				return nil, errCursorProtoMalformed
			}
			fields = append(fields, cursorProtoField{number: number, wireType: wireType, data: b[offset : offset+int(l)]})
			offset += int(l)
		case 5:
			if offset+4 > len(b) {
				return nil, errCursorProtoMalformed
			}
			offset += 4
		default:
			return nil, fmt.Errorf("%w: unsupported wire type %d", errCursorProtoMalformed, wireType)
		}
	}
	return fields, nil
}

// cursorProtoTimestamp decodes a google.protobuf.Timestamp message to unix
// seconds. ok is false when the message is malformed or out of range.
func cursorProtoTimestamp(b []byte) (int64, bool) {
	fields, err := readCursorProtoFields(b)
	if err != nil {
		return 0, false
	}
	var seconds int64
	found := false
	for _, f := range fields {
		if f.number == 1 && f.wireType == 0 {
			seconds = int64(f.varint)
			found = true
		}
	}
	if !found || seconds <= 0 || seconds > 1e12 {
		return 0, false
	}
	return seconds, true
}

func decodeCursorSandAccess(body []byte) (*cursorSandAccess, error) {
	fields, err := readCursorProtoFields(body)
	if err != nil {
		return nil, err
	}
	a := &cursorSandAccess{}
	for _, f := range fields {
		if f.number == 1 && f.wireType == 0 {
			a.granted = f.varint == 1
		}
	}
	return a, nil
}

func decodeCursorSandUsage(body []byte) (*cursorSandUsage, error) {
	fields, err := readCursorProtoFields(body)
	if err != nil {
		return nil, err
	}
	u := &cursorSandUsage{}
	for _, f := range fields {
		switch {
		case f.number == 1 && f.wireType == 2:
			if ts, ok := cursorProtoTimestamp(f.data); ok {
				u.currentPeriodStart = ts
			}
		case f.number == 2 && f.wireType == 2:
			if ts, ok := cursorProtoTimestamp(f.data); ok {
				u.nextResetAt = ts
			}
		case f.number == 3 && f.wireType == 1:
			u.usagePercent = math.Float64frombits(f.fixedBits)
			u.hasUsagePercent = true
		case f.number == 4 && f.wireType == 0:
			u.includedLimitZero = f.varint != 0
		}
	}
	return u, nil
}
