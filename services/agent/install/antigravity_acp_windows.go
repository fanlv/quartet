//go:build windows

package install

import (
	"fmt"
	"os"
	"strings"
)

func installAntigravityLauncher(binaryPath string, args []string) error {
	var kept []string
	for _, arg := range args {
		if strings.TrimSpace(arg) != "" {
			kept = append(kept, arg)
		}
	}
	if len(kept) > 0 {
		return fmt.Errorf("official Antigravity ACP server declares arguments %q; Quartet starts agy_acp_server.exe directly", kept)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("stat official Antigravity ACP server %q failed: %w", binaryPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("official Antigravity ACP server %q is not a regular file", binaryPath)
	}
	return nil
}

func ensureServerExecutable(string) error {
	return nil
}
