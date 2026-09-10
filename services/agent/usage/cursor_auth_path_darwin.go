//go:build darwin

package usage

import (
	"os"
	"path/filepath"
)

// cursorCLIAuthPath returns the cursor-agent CLI's login-token file on macOS
// (mirrors the CLI's own getAuthFilePath logic): ~/.cursor/auth.json.
func cursorCLIAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cursor", "auth.json")
}
