//go:build !windows

package executil

import (
	"context"
	"os/exec"
)

func Command(name string, args ...string) *exec.Cmd {
	return CommandContext(context.Background(), name, args...)
}

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if resolved, err := LookPath(name); err == nil {
		name = resolved
	}
	return exec.CommandContext(ctx, name, args...)
}
