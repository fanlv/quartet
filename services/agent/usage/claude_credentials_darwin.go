//go:build darwin

package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
)

const claudeKeychainService = "Claude Code-credentials"

func readClaudeCodeCredentials(ctx context.Context) (claudeCodeCredentials, string, error) {
	var credentials claudeCodeCredentials
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := executil.CommandContext(
		cctx, "/usr/bin/security", "find-generic-password", "-s", claudeKeychainService, "-w",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return credentials, "macOS Keychain", fmt.Errorf(
				"read Claude Code OAuth credentials from macOS Keychain service %q failed: %w (stderr: %s)",
				claudeKeychainService, err, detail,
			)
		}
		return credentials, "macOS Keychain", fmt.Errorf(
			"read Claude Code OAuth credentials from macOS Keychain service %q failed: %w",
			claudeKeychainService, err,
		)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &credentials); err != nil {
		return credentials, "macOS Keychain", fmt.Errorf(
			"parse Claude Code OAuth credentials from macOS Keychain service %q failed: %w",
			claudeKeychainService, err,
		)
	}
	return credentials, "macOS Keychain", nil
}
