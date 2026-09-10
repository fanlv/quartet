//go:build windows

package usage

import (
	"os"
	"path/filepath"
)

// cursorCLIAuthPath returns the cursor-agent CLI's login-token file on Windows
// (mirrors the CLI's own getAuthFilePath logic, which title-cases the app
// name): %APPDATA%\Cursor\auth.json.
func cursorCLIAuthPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(appData, "Cursor", "auth.json")
}
