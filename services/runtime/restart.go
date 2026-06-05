package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const WebRestartLogPath = "/tmp/quartet-web-restart.log"

// RestartWeb starts `make web` from the repository root in a detached process.
//
// The implementation intentionally uses a short-lived outer shell plus a
// background inner shell. The outer shell exits before make runs, causing the
// inner shell to be re-parented away from the current backend process. This is
// important because `make web` kills the existing backend process tree while
// restarting; if make remained a direct child of the backend, it could kill its
// own restart command before the new services are up.
func RestartWeb(ctx context.Context) error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	script := fmt.Sprintf(`set -eu
cd %s
(
  sleep 1
  printf '\n----- web restart requested at %%s -----\n' "$(date '+%%F %%T')"
  exec make web
) >> %s 2>&1 < /dev/null &
`, shellQuote(repoRoot), shellQuote(WebRestartLogPath))

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", script)
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Run()
}

func findRepoRoot() (string, error) {
	if wd, err := os.Getwd(); err == nil {
		if root, ok := walkUpForMakefile(wd); ok {
			return root, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root, ok := walkUpForMakefile(filepath.Dir(exe)); ok {
			return root, nil
		}
	}
	return "", fmt.Errorf("cannot locate repository root containing Makefile")
}

func walkUpForMakefile(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
