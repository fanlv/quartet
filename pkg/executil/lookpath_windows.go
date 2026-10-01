//go:build windows

package executil

import (
	"os"
	"path/filepath"
	"strings"
)

func antigravityACPRoot(home string) string {
	return filepath.Join(home, filepath.FromSlash(AntigravityACPWindowsRootRel))
}

// extraFallbackDirs adds the directory that contains the official
// agy_acp_server.exe. The registry zip does not install a PATH entry, and a
// cmd shim would sit between Quartet and the server on stdio.
func extraFallbackDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	rel, err := os.ReadFile(filepath.Join(antigravityACPRoot(home), AntigravityACPLaunchDirFile))
	if err != nil {
		return nil
	}
	relPath := filepath.FromSlash(strings.TrimSpace(string(rel)))
	if relPath == "" || filepath.IsAbs(relPath) || relPath == "." || strings.Contains(relPath, "..") {
		return nil
	}
	return []string{filepath.Join(antigravityACPRoot(home), relPath)}
}
