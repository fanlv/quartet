//go:build !windows

package executil

import "path/filepath"

func antigravityACPRoot(home string) string {
	return filepath.Join(home, filepath.FromSlash(AntigravityACPUnixRootRel))
}

func extraFallbackDirs() []string {
	return nil
}
