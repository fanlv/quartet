//go:build darwin

package usage

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
)

const (
	cursorKeychainAccount             = "cursor-user"
	cursorKeychainAccessTokenService  = "cursor-access-token"
	cursorKeychainRefreshTokenService = "cursor-refresh-token"
	cursorKeychainSource              = "macOS Keychain (cursor-access-token)"
)

// readCursorAuth loads the cursor-agent login token. Current CLI builds on
// macOS store it in the login keychain; older installs still use
// ~/.cursor/auth.json, so a missing keychain item falls back to that file.
func readCursorAuth(ctx context.Context) (cursorAuthFile, string, error) {
	keychainAuth, keychainErr := readCursorKeychainAuth(ctx)
	if keychainErr == nil && keychainAuth.AccessToken != "" {
		return keychainAuth, cursorKeychainSource, nil
	}

	fileAuth, path, fileErr := readCursorAuthFile()
	if fileErr == nil {
		return fileAuth, path, nil
	}
	if keychainErr != nil {
		return cursorAuthFile{}, "cursor-agent credentials", fmt.Errorf(
			"%v; %v", keychainErr, fileErr,
		)
	}
	return cursorAuthFile{}, path, fileErr
}

func readCursorKeychainAuth(ctx context.Context) (cursorAuthFile, error) {
	var auth cursorAuthFile
	token, err := readCursorKeychainSecret(ctx, cursorKeychainAccessTokenService)
	if err != nil {
		return auth, err
	}
	auth.AccessToken = token
	if refresh, refreshErr := readCursorKeychainSecret(ctx, cursorKeychainRefreshTokenService); refreshErr == nil {
		auth.RefreshToken = refresh
	}
	return auth, nil
}

func readCursorKeychainSecret(ctx context.Context, service string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := executil.CommandContext(
		cctx, "/usr/bin/security", "find-generic-password",
		"-s", service, "-a", cursorKeychainAccount, "-w",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return "", fmt.Errorf(
				"read cursor-agent token from macOS Keychain service %q account %q failed: %w (stderr: %s)",
				service, cursorKeychainAccount, err, detail,
			)
		}
		return "", fmt.Errorf(
			"read cursor-agent token from macOS Keychain service %q account %q failed: %w",
			service, cursorKeychainAccount, err,
		)
	}
	return strings.TrimSpace(string(out)), nil
}
