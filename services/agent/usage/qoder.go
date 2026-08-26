package usage

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	qoderBin = "qoderclicn"
	// qoderQuotaURL is the QoderCN credits quota endpoint (Bearer-authenticated).
	// The openapi host is the resolved region endpoint; the CN default is used
	// here. A VPC override (qoderclicn config set vpc_endpoint) is not honoured
	// yet — the CN public endpoint covers every account this provider targets.
	qoderQuotaURL = "https://openapi.qoder.com.cn/api/v2/quota/usage"
	// qoderConfigDir is the qoderclicn home dir name under the user's home.
	qoderConfigDir = ".qoder-cn"
	// qoderAuthRelPath / qoderMachineIDRelPath locate the encrypted credential
	// blob and the machine id that seeds its key, relative to the config dir.
	qoderAuthRelPath      = ".auth/user"
	qoderMachineIDRelPath = ".auth/machine_id"
	// qoderCacheRelDir holds quartet-managed cache files (the extracted WASM
	// module and the decrypt helper), relative to the config dir.
	qoderCacheRelDir = ".cache/quartet"
)

//go:embed qoder_decrypt.mjs
var qoderDecryptScript string

// qoderQuotaResp mirrors the openapi /api/v2/quota/usage response.
type qoderQuotaResp struct {
	UserType             string  `json:"userType"`
	UsageType            string  `json:"usageType"`
	TotalUsagePercentage float64 `json:"totalUsagePercentage"` // 0–1 fraction
	IsQuotaExceeded      bool    `json:"isQuotaExceeded"`
	ExpiresAt            int64   `json:"expiresAt"` // unix milliseconds
	UserQuota            struct {
		Total      float64 `json:"total"`
		Used       float64 `json:"used"`
		Remaining  float64 `json:"remaining"`
		Percentage float64 `json:"percentage"` // 0–1 fraction
		Unit       string  `json:"unit"`
	} `json:"userQuota"`
}

// qoderUserInfo is the decrypted credential blob (~/.qoder-cn/.auth/user).
type qoderUserInfo struct {
	SecurityOAuthToken string `json:"security_oauth_token"`
	AccessToken        string `json:"access_token"`
}

// QoderUsage reads the QoderCN credits quota from the openapi quota endpoint.
// The Bearer token lives in ~/.qoder-cn/.auth/user, which the CLI encrypts with
// an embedded WASM module (AES-GCM keyed by the machine id). We reuse that WASM
// by shelling out to a cached Node.js helper that decrypts the blob and prints
// the token, then call the quota endpoint directly.
func (s *serviceImpl) QoderUsage(ctx context.Context) (*model.QoderUsage, error) {
	verCh := make(chan string, 1)
	go func() { verCh <- s.binVersion(ctx, qoderBin) }()

	token, err := s.qoderToken(ctx)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 25 * time.Second}
	body, err := fetchQoderQuota(ctx, client, token)
	if err != nil {
		return nil, err
	}

	var u qoderQuotaResp
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("parse qoder quota response failed: %w (body: %s)", err, string(body))
	}

	return &model.QoderUsage{
		Version:       <-verCh,
		PlanType:      u.UserType,
		Unit:          u.UserQuota.Unit,
		Total:         u.UserQuota.Total,
		Used:          u.UserQuota.Used,
		Remaining:     u.UserQuota.Remaining,
		UsedPercent:   u.TotalUsagePercentage * 100,
		ExpiresAt:     u.ExpiresAt / 1000,
		QuotaExceeded: u.IsQuotaExceeded,
	}, nil
}

// qoderToken decrypts the local qoderclicn credential blob and returns a usable
// OAuth access token.
func (s *serviceImpl) qoderToken(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir failed: %w", err)
	}
	cfg := filepath.Join(home, qoderConfigDir)
	authPath := filepath.Join(cfg, qoderAuthRelPath)
	if _, err := os.Stat(authPath); err != nil {
		return "", fmt.Errorf("read %s failed (not logged in? run `qoderclicn` to authenticate): %w", authPath, err)
	}

	scriptPath, wasmPath, err := s.ensureQoderDecryptHelper(ctx, cfg)
	if err != nil {
		return "", err
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "node", scriptPath, authPath, filepath.Join(cfg, qoderMachineIDRelPath), wasmPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("qoder credential decrypt failed: %v (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	var info qoderUserInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return "", fmt.Errorf("parse decrypted qoder credential failed: %w (output: %s)", err, strings.TrimSpace(stdout.String()))
	}
	token := info.SecurityOAuthToken
	if token == "" {
		token = info.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("decrypted qoder credential has no access token")
	}
	return token, nil
}

// ensureQoderDecryptHelper writes the Node.js decrypt helper and, on first use,
// extracts the embedded auth WASM module from the qoderclicn binary. Both files
// are cached under ~/.qoder-cn/.cache/quartet/ so subsequent polls are cheap.
// Returns (scriptPath, wasmPath, error).
func (s *serviceImpl) ensureQoderDecryptHelper(ctx context.Context, cfgDir string) (string, string, error) {
	cacheDir := filepath.Join(cfgDir, qoderCacheRelDir)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create qoder cache dir failed: %w", err)
	}

	scriptPath := filepath.Join(cacheDir, "decrypt.mjs")
	if err := os.WriteFile(scriptPath, []byte(qoderDecryptScript), 0o600); err != nil {
		return "", "", fmt.Errorf("write qoder decrypt helper failed: %w", err)
	}

	wasmPath := filepath.Join(cacheDir, "qoder_auth.wasm.b64")
	if _, err := os.Stat(wasmPath); err != nil {
		if err := extractQoderWasm(ctx, wasmPath); err != nil {
			return "", "", err
		}
	}
	return scriptPath, wasmPath, nil
}

// extractQoderWasm locates the qoderclicn binary and extracts the base64-encoded
// auth WASM module embedded in it (the wasm-bindgen module starts with the
// "AGFzbQ" base64 prefix, i.e. the "\0asm" magic). The result is cached at
// wasmPath.
func extractQoderWasm(ctx context.Context, wasmPath string) error {
	binPath, err := executil.LookPath(qoderBin)
	if err != nil {
		return fmt.Errorf("qoderclicn binary not found in PATH (install it first): %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// The WASM is embedded as a single long base64 string literal. `strings`
	// surfaces it; grep picks the one starting with the WASM magic prefix.
	out, err := exec.CommandContext(cctx, "sh", "-c",
		fmt.Sprintf("strings %q | grep -o '\"AGFzbQ[A-Za-z0-9+/=]*\"' | head -1 | tr -d '\"'", binPath)).Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("extract auth WASM from %s failed (err=%v)", binPath, err)
	}
	if err := os.WriteFile(wasmPath, bytes.TrimSpace(out), 0o600); err != nil {
		return fmt.Errorf("cache qoder auth WASM failed: %w", err)
	}
	logger.Infof(ctx, "[agent.usage] extracted qoder auth WASM (%d bytes) to %s", len(bytes.TrimSpace(out)), wasmPath)
	return nil
}

// fetchQoderQuota GETs the quota endpoint with the given access token.
func fetchQoderQuota(ctx context.Context, client *http.Client, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderQuotaURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request qoder quota failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := readAllLimited(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("qoder quota returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
