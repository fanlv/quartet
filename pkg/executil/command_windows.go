//go:build windows

package executil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Command(name string, args ...string) *exec.Cmd {
	return CommandContext(context.Background(), name, args...)
}

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if resolved, err := LookPath(name); err == nil {
		name = resolved
	}
	extension := strings.ToLower(filepath.Ext(name))
	if extension == ".cmd" {
		// npm publishes a PowerShell shim next to every .cmd shim. -File keeps
		// each structured argument separate and avoids cmd.exe interpreting a
		// custom Agent argument as shell syntax.
		powerShellShim := strings.TrimSuffix(name, extension) + ".ps1"
		if _, err := os.Stat(powerShellShim); err == nil {
			commandArgs := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", powerShellShim}, args...)
			return exec.CommandContext(ctx, "powershell.exe", commandArgs...)
		}
	}
	if extension == ".cmd" || extension == ".bat" {
		commandArgs := append([]string{"/d", "/s", "/c", name}, args...)
		return exec.CommandContext(ctx, "cmd.exe", commandArgs...)
	}
	return exec.CommandContext(ctx, name, args...)
}
