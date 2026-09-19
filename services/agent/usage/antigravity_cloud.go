package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	antigravityOAuthTokenURL = "https://oauth2.googleapis.com/token"
	antigravityOAuthFileRel  = "antigravity-cli/antigravity-oauth-token"
	antigravityRefreshLeeway = 60 * time.Second
	antigravityCloudTimeout  = 25 * time.Second
)

var (
	antigravityOAuthClientIDRe = regexp.MustCompile(`[0-9]+-[a-z0-9]+\.apps\.googleusercontent\.com`)
	antigravityOAuthSecretPref = []byte("GOCSPX-")

	antigravityOAuthOnce    sync.Once
	antigravityOAuthIDs     []string
	antigravityOAuthSecrets []string
	antigravityOAuthErr     error
)

var antigravityCloudHosts = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

type antigravityOAuthFile struct {
	Token      antigravityOAuthToken `json:"token"`
	AuthMethod string                `json:"auth_method,omitempty"`
}

type antigravityOAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Expiry       string `json:"expiry,omitempty"`
}

func (t antigravityOAuthToken) expired() bool {
	if t.AccessToken == "" {
		return true
	}
	if t.Expiry == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339Nano, t.Expiry)
	if err != nil {
		exp, err = time.Parse(time.RFC3339, t.Expiry)
	}
	if err != nil {
		return false
	}
	return time.Now().After(exp.Add(-antigravityRefreshLeeway))
}

func antigravityOAuthPath() string {
	root := strings.TrimSpace(os.Getenv("GEMINI_CLI_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".gemini")
	}
	return filepath.Join(root, antigravityOAuthFileRel)
}

// cloudCodeAntigravityUsage reads the local agy OAuth token, refreshes it when
// needed, and calls Cloud Code RetrieveUserQuotaSummary. This is the path that
// works for agy 1.2+ when the loopback language server requires a CSRF token
// that is never exposed on the process command line.
func (s *serviceImpl) cloudCodeAntigravityUsage(ctx context.Context) (*model.AntigravityUsage, error) {
	client := &http.Client{
		Timeout:   antigravityCloudTimeout,
		Transport: s.antigravityCloudTransport(ctx),
	}
	path, creds, err := loadAntigravityOAuth()
	if err != nil {
		return nil, err
	}
	token := creds.Token.AccessToken
	if creds.Token.expired() {
		if creds.Token.RefreshToken == "" {
			return nil, fmt.Errorf("agy Cloud Code token expired and has no refresh token (path %s)", path)
		}
		token, err = refreshAntigravityOAuth(ctx, client, path, creds)
		if err != nil {
			return nil, err
		}
	}

	body, err := fetchAntigravityCloudQuota(ctx, client, token)
	if err != nil && isAntigravityUnauthorized(err) && creds.Token.RefreshToken != "" {
		token, err = refreshAntigravityOAuth(ctx, client, path, creds)
		if err != nil {
			return nil, err
		}
		body, err = fetchAntigravityCloudQuota(ctx, client, token)
	}
	if err != nil {
		return nil, err
	}
	return parseAntigravityQuota(body)
}

func loadAntigravityOAuth() (string, *antigravityOAuthFile, error) {
	path := antigravityOAuthPath()
	if path == "" {
		return "", nil, fmt.Errorf("resolve agy OAuth token path failed")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("read %s failed (run `agy` and sign in): %w", path, err)
	}
	var creds antigravityOAuthFile
	if err := json.Unmarshal(raw, &creds); err != nil {
		return "", nil, fmt.Errorf("parse %s failed: %w", path, err)
	}
	if creds.Token.AccessToken == "" && creds.Token.RefreshToken == "" {
		return "", nil, fmt.Errorf("%s has no access_token or refresh_token (run `agy` and sign in)", path)
	}
	return path, &creds, nil
}

func refreshAntigravityOAuth(ctx context.Context, client *http.Client, path string, creds *antigravityOAuthFile) (string, error) {
	clientIDs, clientSecrets, err := agyOAuthClients()
	if err != nil {
		return "", err
	}
	var lastErr error
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	for _, clientID := range clientIDs {
		for _, clientSecret := range clientSecrets {
			form := url.Values{
				"client_id":     {clientID},
				"client_secret": {clientSecret},
				"refresh_token": {creds.Token.RefreshToken},
				"grant_type":    {"refresh_token"},
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityOAuthTokenURL, strings.NewReader(form.Encode()))
			if err != nil {
				return "", err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			resp, err := client.Do(req)
			if err != nil {
				return "", fmt.Errorf("refresh agy Cloud Code token failed: %w", err)
			}
			body, _ := readAllLimited(resp)
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				lastErr = fmt.Errorf("refresh agy Cloud Code token returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
				continue
			}
			if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
				lastErr = fmt.Errorf("parse agy Cloud Code token refresh failed: %v (body: %s)", err, strings.TrimSpace(string(body)))
				continue
			}
			lastErr = nil
			break
		}
		if lastErr == nil && tok.AccessToken != "" {
			break
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("refresh agy Cloud Code token failed: no embedded OAuth client accepted the refresh token")
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = creds.Token.RefreshToken
	}
	if tok.TokenType == "" {
		tok.TokenType = "Bearer"
	}
	creds.Token.AccessToken = tok.AccessToken
	creds.Token.RefreshToken = tok.RefreshToken
	creds.Token.TokenType = tok.TokenType
	creds.Token.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Format(time.RFC3339Nano)
	if raw, err := json.MarshalIndent(creds, "", "  "); err == nil {
		_ = os.WriteFile(path, raw, 0o600)
	}
	return tok.AccessToken, nil
}

func fetchAntigravityCloudQuota(ctx context.Context, client *http.Client, accessToken string) ([]byte, error) {
	var lastErr error
	for _, host := range antigravityCloudHosts {
		body, err := postAntigravityCloudQuota(ctx, client, host, accessToken)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if isAntigravityUnauthorized(err) {
			return nil, err
		}
		logger.Warnf(ctx, "[agent.usage] antigravity Cloud Code quota on %s failed: %v", host, err)
	}
	if lastErr == nil {
		return nil, fmt.Errorf("agy Cloud Code quota failed")
	}
	return nil, lastErr
}

func postAntigravityCloudQuota(ctx context.Context, client *http.Client, host, accessToken string) ([]byte, error) {
	endpoint := host + "/v1internal:retrieveUserQuotaSummary"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s failed: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, _ := readAllLimited(resp)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("agy Cloud Code quota unauthorized (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func agyOAuthClients() ([]string, []string, error) {
	antigravityOAuthOnce.Do(func() {
		antigravityOAuthIDs, antigravityOAuthSecrets, antigravityOAuthErr = extractAgyOAuthClients()
	})
	return antigravityOAuthIDs, antigravityOAuthSecrets, antigravityOAuthErr
}

// extractAgyOAuthClients reads installed-app OAuth clients from the agy
// binary. Those values are public CLI credentials; we do not check them into
// the repo because GitHub push protection treats them as secrets.
func extractAgyOAuthClients() ([]string, []string, error) {
	bin, err := executil.LookPath(agyBin)
	if err != nil {
		return nil, nil, fmt.Errorf("find agy for Cloud Code OAuth client failed: %w", err)
	}
	f, err := os.Open(bin)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s failed: %w", bin, err)
	}
	defer f.Close()

	const chunk = 1 << 20
	buf := make([]byte, chunk)
	var carry []byte
	var ids, secrets []string
	seenID := map[string]bool{}
	seenSecret := map[string]bool{}
	add := func(dst *[]string, seen map[string]bool, val string) {
		if val == "" || seen[val] {
			return
		}
		seen[val] = true
		*dst = append(*dst, val)
	}
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			window := append(carry, buf[:n]...)
			for _, m := range antigravityOAuthClientIDRe.FindAll(window, -1) {
				add(&ids, seenID, string(m))
			}
			for _, m := range findAgyOAuthSecrets(window) {
				add(&secrets, seenSecret, m)
			}
			if keep := 80; len(window) > keep {
				carry = append(carry[:0], window[len(window)-keep:]...)
			} else {
				carry = append(carry[:0], window...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, fmt.Errorf("read %s failed: %w", bin, readErr)
		}
	}
	if len(ids) == 0 || len(secrets) == 0 {
		return nil, nil, fmt.Errorf("agy binary %s does not embed a Cloud Code OAuth client", bin)
	}
	return ids, secrets, nil
}

func findAgyOAuthSecrets(data []byte) []string {
	var out []string
	for i := 0; i < len(data); {
		idx := bytes.Index(data[i:], antigravityOAuthSecretPref)
		if idx < 0 {
			break
		}
		start := i + idx
		body := start + len(antigravityOAuthSecretPref)
		end := body
		for end < len(data) && end-body < 40 && isAgyOAuthSecretByte(data[end]) {
			if bytes.HasPrefix(data[end:], antigravityOAuthSecretPref) {
				break
			}
			end++
		}
		if n := end - body; n >= 20 && n <= 40 {
			out = append(out, string(data[start:end]))
		}
		i = body
	}
	return out
}

func isAgyOAuthSecretByte(b byte) bool {
	return b == '-' || b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func isAntigravityUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthorized") || strings.Contains(msg, "http 401") || strings.Contains(msg, "http 403")
}

func (s *serviceImpl) antigravityCloudTransport(ctx context.Context) *http.Transport {
	env := map[string]string{}
	if s.settings != nil {
		if acp := s.effectiveACPEnv(agyBin); len(acp) > 0 {
			for k, v := range acp {
				env[k] = v
			}
		}
	}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if env[key] == "" {
			if v := strings.TrimSpace(os.Getenv(key)); v != "" {
				env[key] = v
			}
		}
	}
	if firstNonEmpty(env["https_proxy"], env["HTTPS_PROXY"], env["http_proxy"], env["HTTP_PROXY"]) == "" {
		if proxy := s.agyProcessProxy(ctx); proxy != "" {
			env["HTTPS_PROXY"] = proxy
		}
	}
	tr := proxyTransport(env)
	if tr.Proxy == nil {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return tr
}

// agyProcessProxy reads HTTPS_PROXY/HTTP_PROXY from a running agy process so
// Cloud Code can reuse the same outbound proxy the CLI already uses.
func (s *serviceImpl) agyProcessProxy(ctx context.Context) string {
	procs, err := s.agyProcesses(ctx)
	if err != nil || len(procs) == 0 {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ps", "eww", "-p", fmt.Sprintf("%d", procs[0].pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	for _, part := range strings.Fields(string(out)) {
		for _, prefix := range []string{"HTTPS_PROXY=", "https_proxy=", "HTTP_PROXY=", "http_proxy="} {
			if strings.HasPrefix(part, prefix) {
				if v := strings.TrimSpace(strings.TrimPrefix(part, prefix)); v != "" {
					return v
				}
			}
		}
	}
	return ""
}
