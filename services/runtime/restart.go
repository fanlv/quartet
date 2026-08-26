package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	return restartWeb(ctx, repoRoot)
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
