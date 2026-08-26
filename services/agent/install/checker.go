// Package install checks whether an ACP agent's executables are available to
// the backend process.
package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fanlv/quartet/pkg/executil"
)

// Definition contains the executable parts of one ACP runtime definition.
// ACPProgram is the program only; its ordered arguments do not affect whether
// the runtime is installed.
type Definition struct {
	Bin        string
	ACPProgram string
}

// ExecutableStatus describes how one executable was resolved.
type ExecutableStatus struct {
	Executable   string
	ResolvedPath string
	PathBased    bool
	Installed    bool
	Error        string
}

// Status is the complete installation result for one ACP runtime definition.
type Status struct {
	Installed  bool
	Bin        ExecutableStatus
	ACPProgram ExecutableStatus
	Error      string
}

// Checker performs installation checks against the backend process environment.
type Checker struct{}

func (c Checker) Check(def Definition) Status {
	bin := c.checkExecutable(def.Bin)
	program := bin
	if def.ACPProgram != def.Bin {
		program = c.checkExecutable(def.ACPProgram)
	}

	status := Status{
		Installed:  bin.Installed && program.Installed,
		Bin:        bin,
		ACPProgram: program,
	}
	if status.Installed {
		return status
	}

	switch {
	case def.Bin == def.ACPProgram:
		status.Error = bin.Error
	default:
		var failures []string
		if !bin.Installed {
			failures = append(failures, fmt.Sprintf("bin %q: %s", def.Bin, bin.Error))
		}
		if !program.Installed {
			failures = append(failures, fmt.Sprintf(
				"ACP program %q: %s",
				def.ACPProgram,
				program.Error,
			))
		}
		status.Error = strings.Join(failures, "\n")
	}
	return status
}

func (c Checker) checkExecutable(executable string) ExecutableStatus {
	status := ExecutableStatus{
		Executable: executable,
		PathBased:  containsPathSeparator(executable),
	}
	if executable == "" {
		status.Error = "executable is empty"
		return status
	}

	if status.PathBased {
		info, err := os.Stat(executable)
		if err != nil {
			status.Error = fmt.Sprintf("stat executable path %q failed: %v", executable, err)
			return status
		}
		if !info.Mode().IsRegular() {
			status.Error = fmt.Sprintf(
				"executable path %q is not a regular file: mode=%s",
				executable,
				info.Mode(),
			)
			return status
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			status.Error = fmt.Sprintf(
				"executable path %q is not executable: mode=%s",
				executable,
				info.Mode(),
			)
			return status
		}
		status.ResolvedPath = absolutePath(executable)
		status.Installed = true
		return status
	}

	resolved, err := executil.LookPath(executable)
	if err != nil {
		status.Error = fmt.Sprintf(
			"executable %q not found in $PATH",
			executable,
		)
		return status
	}
	status.ResolvedPath = absolutePath(resolved)
	status.Installed = true
	return status
}

func containsPathSeparator(executable string) bool {
	if strings.ContainsRune(executable, os.PathSeparator) {
		return true
	}
	return runtime.GOOS == "windows" && strings.ContainsRune(executable, '/')
}

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}
