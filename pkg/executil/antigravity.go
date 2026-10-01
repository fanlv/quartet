package executil

import (
	"fmt"
	"os"
)

// AntigravityACPRoot is the directory that holds the official ACP server
// payload for the current user. It matches the home-relative uninstall path.
func AntigravityACPRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve user home for Antigravity ACP server failed: %w", err)
	}
	return antigravityACPRoot(home), nil
}

// Official Antigravity ACP server layout. The catalog uninstall paths and
// LookPath must stay on these home-relative locations: the registry archive
// is not placed on PATH by Google's installer.
const (
	AntigravityACPRegistryID = "antigravity-acp"
	AntigravityACPProgram    = "agy_acp_server"

	AntigravityACPUnixRootRel     = ".local/share/quartet/agents/antigravity-acp"
	AntigravityACPUnixLauncherRel = ".local/bin/agy_acp_server"
	AntigravityACPUnixCLIRel      = ".local/bin/agy"

	AntigravityACPWindowsRootRel = "AppData/Local/quartet/agents/antigravity-acp"
	AntigravityACPWindowsCLIRel  = "AppData/Local/agy/bin"

	AntigravityACPVersionFile   = "VERSION"
	AntigravityACPLaunchDirFile = "LAUNCH_DIR"
)
