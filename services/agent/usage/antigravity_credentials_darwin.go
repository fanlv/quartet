//go:build darwin

package usage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
)

const (
	antigravityKeychainService = "gemini"
	antigravityKeychainAccount = "antigravity"
	antigravityKeychainSource  = "macOS Keychain (service gemini, account antigravity)"
	antigravityKeychainBase64  = "go-keyring-base64:"
	antigravityKeychainHex     = "go-keyring-encoded:"
)

func loadAntigravityOAuth(ctx context.Context) (*antigravityOAuthCredentials, error) {
	creds, keychainErr := readAntigravityKeychainOAuth(ctx)
	if keychainErr == nil {
		return creds, nil
	}
	creds, fileErr := readAntigravityOAuthFile()
	if fileErr == nil {
		return creds, nil
	}
	return nil, fmt.Errorf("load agy OAuth credentials failed: %v; %w (run `agy` and sign in if no saved login is available)", keychainErr, fileErr)
}

func readAntigravityKeychainOAuth(ctx context.Context) (*antigravityOAuthCredentials, error) {
	data, err := antigravityKeychainCommand(ctx, "", "find-generic-password",
		"-s", antigravityKeychainService, "-a", antigravityKeychainAccount, "-w")
	if err != nil {
		return nil, err
	}
	encoded := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(encoded, antigravityKeychainBase64):
		data, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, antigravityKeychainBase64))
	case strings.HasPrefix(encoded, antigravityKeychainHex):
		data, err = hex.DecodeString(strings.TrimPrefix(encoded, antigravityKeychainHex))
	default:
		data = []byte(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode agy OAuth credentials from %s failed: %w", antigravityKeychainSource, err)
	}
	return parseAntigravityOAuth(data, antigravityKeychainSource, writeAntigravityKeychainOAuth)
}

func writeAntigravityKeychainOAuth(ctx context.Context, data []byte) error {
	// Match go-keyring's encoding and send the secret over stdin so it never
	// appears in the security process's command line.
	encoded := antigravityKeychainBase64 + base64.StdEncoding.EncodeToString(data)
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n",
		antigravityKeychainService, antigravityKeychainAccount, encoded)
	if len(command) > 4096 {
		return fmt.Errorf("agy OAuth keychain update exceeds security's 4096-byte command limit (%d bytes)", len(command))
	}
	_, err := antigravityKeychainCommand(ctx, command, "-q", "-i")
	return err
}

func antigravityKeychainCommand(ctx context.Context, input string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := executil.CommandContext(cctx, "/usr/bin/security", args...)
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("access agy OAuth credentials in %s failed: %w (stderr: %s)",
			antigravityKeychainSource, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
