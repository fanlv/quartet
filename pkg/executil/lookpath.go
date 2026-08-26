// Package executil resolves and starts command-line tools consistently across
// supported desktop platforms.
package executil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LookPath first uses the process PATH, then checks user-scoped directories
// used by the official installers in the built-in Agent catalog. This lets a
// just-installed CLI work without restarting quartet-web merely to inherit an
// updated shell or Windows User PATH.
func LookPath(name string) (string, error) {
	resolved, pathErr := exec.LookPath(name)
	if pathErr == nil {
		return resolved, nil
	}
	if strings.ContainsAny(name, `/\`) {
		return "", pathErr
	}

	for _, dir := range fallbackDirs() {
		resolved, err := exec.LookPath(filepath.Join(dir, name))
		if err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w (also checked the standard user Agent install directories)", pathErr)
}

func fallbackDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".grok", "bin"),
		filepath.Join(home, ".kimi-code", "bin"),
	}
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, "/Applications/Kiro CLI.app/Contents/MacOS")
	}
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		dirs = append(dirs,
			filepath.Join(home, "bin"),
			filepath.Join(localAppData, "agy", "bin"),
			filepath.Join(localAppData, "cursor-agent"),
			filepath.Join(localAppData, "Programs", "TraeX", "bin"),
		)
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			dirs = append(dirs, filepath.Join(programFiles, "Kiro-Cli"))
		}
	}
	return uniqueDirs(dirs)
}

func uniqueDirs(dirs []string) []string {
	seen := make(map[string]bool, len(dirs))
	result := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		key := filepath.Clean(dir)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if dir == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, dir)
	}
	return result
}
