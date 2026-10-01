//go:build !windows

package install

import (
	"fmt"
	"net"
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
	for _, arg := range antigravityACPLaunchArgs(args) {
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

// antigravityACPLaunchArgs keeps the registry arguments and, on a host whose
// loopback has no IPv6 address, adds the server's own bypass. The official
// binary aborts during startup when ::1 is missing.
func antigravityACPLaunchArgs(args []string) []string {
	out := append([]string{}, args...)
	if ipv6LoopbackPresent() {
		return out
	}
	for _, arg := range out {
		if strings.Contains(arg, "enforce_kernel_ipv6_support") {
			return out
		}
	}
	return append(out, "--enforce_kernel_ipv6_support=false")
}

func ipv6LoopbackPresent() bool {
	iface, err := net.InterfaceByName("lo")
	if err != nil {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if ok && ipNet.IP.Equal(net.IPv6loopback) {
			return true
		}
	}
	return false
}
