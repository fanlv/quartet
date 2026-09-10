//go:build !darwin && !windows

package usage

import (
	"os"
	"path/filepath"
)

// cursorCLIAuthPath returns the cursor-agent CLI's login-token file on Linux
// and other Unix systems (mirrors the CLI's own getAuthFilePath logic):
// $XDG_CONFIG_HOME/cursor/auth.json, defaulting to ~/.config/cursor/auth.json.
func cursorCLIAuthPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cursor", "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "cursor", "auth.json")
}
