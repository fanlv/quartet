package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

var validateHTTPClient = &http.Client{Timeout: 15 * time.Second}

func ValidateCredentials(ctx context.Context, appID, appSecret string) error {
	brand := os.Getenv(envBrand)
	url := resolveOpenDomain(brand) + "/open-apis/auth/v3/tenant_access_token/internal/"
	body, err := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	if err != nil {
		return fmt.Errorf("marshal request failed: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := validateHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("read response failed: %w", err)
	}

	// Check HTTP status before parsing JSON so gateway errors (502/503 HTML
	// pages) surface as a meaningful HTTP error instead of a confusing
	// "parse response failed: invalid character '<'" from the JSON decoder.
	if resp.StatusCode != http.StatusOK {
		preview := string(respBody)
		if len(preview) > 256 {
			preview = preview[:256] + "...(truncated)"
		}
		return fmt.Errorf("lark API returned HTTP %d: %s", resp.StatusCode, preview)
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("parse response failed: %w", err)
	}

	if result.Code != 0 {
		return fmt.Errorf("%s", result.Msg)
	}
	return nil
}
