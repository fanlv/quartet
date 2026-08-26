//go:build windows

package executil

import (
	"context"
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
	if extension == ".cmd" || extension == ".bat" {
		// Windows cannot pass a batch shim directly to CreateProcess. The
		// arguments here come from a structured runtime/catalog definition;
		// passing them after /c keeps them separate at the Go API boundary.
		commandArgs := append([]string{"/d", "/s", "/c", name}, args...)
		return exec.CommandContext(ctx, "cmd.exe", commandArgs...)
	}
	return exec.CommandContext(ctx, name, args...)
}
