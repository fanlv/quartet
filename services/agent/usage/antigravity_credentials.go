package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/consts"
)

const (
	antigravityOAuthFileRel  = "antigravity-cli/antigravity-oauth-token"
	antigravityRefreshLeeway = 60 * time.Second
)

type antigravityOAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Expiry       string `json:"expiry,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

type antigravityOAuthCredentials struct {
	Token  antigravityOAuthToken `json:"token"`
	source string
	raw    map[string]json.RawMessage
	write  func(context.Context, []byte) error
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
		return false
	}
	return time.Now().After(exp.Add(-antigravityRefreshLeeway))
}

func antigravityOAuthPath() string {
	root := strings.TrimSpace(os.Getenv(consts.EnvKeyGeminiCLIHome))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".gemini")
	}
	return filepath.Join(root, antigravityOAuthFileRel)
}

func readAntigravityOAuthFile() (*antigravityOAuthCredentials, error) {
	path := antigravityOAuthPath()
	if path == "" {
		return nil, fmt.Errorf("resolve agy OAuth token path failed")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s failed: %w", path, err)
	}
	return parseAntigravityOAuth(raw, path, func(_ context.Context, data []byte) error {
		return repository.AtomicWriteFile(path, data, 0o600)
	})
}

func parseAntigravityOAuth(raw []byte, source string, write func(context.Context, []byte) error) (*antigravityOAuthCredentials, error) {
	var creds antigravityOAuthCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("parse agy OAuth credentials from %s failed: %w", source, err)
	}
	if creds.Token.AccessToken == "" && creds.Token.RefreshToken == "" {
		return nil, fmt.Errorf("agy OAuth credentials from %s have no access_token or refresh_token", source)
	}
	if err := json.Unmarshal(raw, &creds.raw); err != nil {
		return nil, fmt.Errorf("parse agy OAuth metadata from %s failed: %w", source, err)
	}
	creds.source = source
	creds.write = write
	return &creds, nil
}

// saveToken updates only the OAuth token fields, preserving account metadata
// and any token fields added by the CLI. The source owns its storage format.
func (c *antigravityOAuthCredentials) saveToken(ctx context.Context) error {
	var tokenFields map[string]json.RawMessage
	if err := json.Unmarshal(c.raw["token"], &tokenFields); err != nil {
		return fmt.Errorf("parse stored agy OAuth token failed: %w", err)
	}
	raw, err := json.Marshal(c.Token)
	if err != nil {
		return err
	}
	var updated map[string]json.RawMessage
	if err := json.Unmarshal(raw, &updated); err != nil {
		return err
	}
	for key, value := range updated {
		tokenFields[key] = value
	}
	if c.raw["token"], err = json.Marshal(tokenFields); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(c.raw, "", "  ")
	if err != nil {
		return err
	}
	return c.write(ctx, raw)
}
