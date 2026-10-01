//go:build !windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fanlv/quartet/pkg/executil"
)

func installAntigravityLauncher(binaryPath string, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return fmt.Errorf("resolve user home for Antigravity ACP launcher failed: %w", err)
	}
	launcher := filepath.Join(home, filepath.FromSlash(executil.AntigravityACPUnixLauncherRel))
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		return fmt.Errorf("create %q failed: %w", filepath.Dir(launcher), err)
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\nexec ")
	script.WriteString(shellQuote(binaryPath))
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		script.WriteByte(' ')
		script.WriteString(shellQuote(arg))
	}
	script.WriteString(" \"$@\"\n")
	if err := writeFileAtomic(launcher, script.String(), 0o755); err != nil {
		return fmt.Errorf("install Antigravity ACP launcher %q failed: %w", launcher, err)
	}
	return nil
}

func ensureServerExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o755); err != nil {
		return err
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
