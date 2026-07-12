package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	typepath "github.com/fanlv/quartet/types/path"
)

// containerDriver abstracts the pieces of docker-compose Manager depends
// on. Keeping it small makes it easy to stub in tests and to swap in a
// different runner later (the refactor doc allows for podman / remote
// docker hosts in a follow-up).
type containerDriver interface {
	Up(ctx context.Context, req upRequest) (port int, err error)
	Down(ctx context.Context, projectName string) error
	Port(ctx context.Context, projectName string) (port int, err error)
	List(ctx context.Context) ([]listedContainer, error)
}

type upRequest struct {
	WorkspaceID string
	ProjectName string
	HostWorkdir string
}

type listedContainer struct {
	ProjectName string
	Running     bool
	Port        int
}

// composeDriver is the production containerDriver. It materialises a
// per-workspace docker-compose YAML in $LOCAL_MEMORY/var/quartet/state/sandbox/compose/.
// and shells out to `docker compose`.
type composeDriver struct {
	stateDir string
	docker   string
	// initErr captures state-dir setup failures (missing LOCAL_MEMORY,
	// MkdirAll denied). Surfaced by every method so callers see the real
	// cause instead of a downstream docker/file error.
	initErr error
}

func newComposeDriver() containerDriver {
	d := &composeDriver{docker: "docker"}
	stateDir, err := typepath.SandboxComposeStateDir()
	if err != nil {
		d.initErr = fmt.Errorf("resolve sandbox compose state dir failed: %w", err)
		return d
	}
	d.stateDir = stateDir
	if err := os.MkdirAll(d.stateDir, 0o755); err != nil {
		d.initErr = fmt.Errorf("create compose state dir %s failed: %w", d.stateDir, err)
	}
	return d
}

func (d *composeDriver) Up(ctx context.Context, req upRequest) (int, error) {
	if d.initErr != nil {
		return 0, d.initErr
	}
	dir := filepath.Join(d.stateDir, req.ProjectName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create compose dir %s failed: %w", dir, err)
	}
	composeFile := filepath.Join(dir, "docker-compose.yaml")
	composeYAML, err := renderComposeTemplate(req)
	if err != nil {
		return 0, fmt.Errorf("render compose file for %s failed: %w", req.ProjectName, err)
	}
	// 0o600: the rendered compose file encodes bind-mount paths and may
	// later carry environment secrets; restrict to the quartet user.
	if err := os.WriteFile(composeFile, []byte(composeYAML), 0o600); err != nil {
		return 0, fmt.Errorf("write compose file %s failed: %w", composeFile, err)
	}

	cmd := exec.CommandContext(ctx, d.docker, "compose",
		"-p", req.ProjectName,
		"-f", composeFile,
		"up", "-d",
		"--remove-orphans",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Best-effort cleanup: even if the compose CLI was killed by ctx timeout,
		// the docker daemon may still finish pulling/starting containers.
		// Try to tear the project down with a detached timeout to avoid leaving
		// orphaned containers that the Manager never adopts.
		downCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = d.Down(downCtx, req.ProjectName)
		cancel()
		// Truncate combined output: on failed pulls it can carry multi-MB of
		// layer-progress logs which bloats error logs and flood downstream
		// telemetry. Keep the tail where the actual error message lives.
		return 0, fmt.Errorf("docker compose up failed: %w\n%s", err, truncateComposeOutput(out))
	}

	port, err := d.Port(ctx, req.ProjectName)
	if err != nil {
		// `up -d` already materialised a container; tearing it back down
		// keeps the contract "Up either succeeds with a port or leaves
		// nothing behind", so the Manager doesn't have to guess whether
		// a recovery sweep is needed for this workspace.
		downCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if downErr := d.Down(downCtx, req.ProjectName); downErr != nil {
			return 0, fmt.Errorf("read back published port failed: %w (cleanup also failed: %v)", err, downErr)
		}
		return 0, fmt.Errorf("read back published port failed: %w", err)
	}
	return port, nil
}

func (d *composeDriver) Down(ctx context.Context, projectName string) error {
	if d.initErr != nil {
		return d.initErr
	}
	dir := filepath.Join(d.stateDir, projectName)
	composeFile := filepath.Join(dir, "docker-compose.yaml")
	// compose down is a no-op when the project isn't up; we still want to
	// clean the state dir so a future Up for the same workspace starts
	// from a fresh template render.
	if _, err := os.Stat(composeFile); err == nil {
		cmd := exec.CommandContext(ctx, d.docker, "compose",
			"-p", projectName,
			"-f", composeFile,
			"down", "--volumes", "--remove-orphans",
		)
		out, err := cmd.CombinedOutput()
		if err != nil && !isAlreadyGone(out) {
			return fmt.Errorf("docker compose down failed: %w\n%s", err, truncateComposeOutput(out))
		}
	} else if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat compose file %s failed: %w", composeFile, err)
		}
		if err := d.removeProjectContainers(ctx, projectName); err != nil {
			return err
		}
		if err := d.removeProjectNetworks(ctx, projectName); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(dir)
	return nil
}

func (d *composeDriver) removeProjectContainers(ctx context.Context, projectName string) error {
	listCmd := exec.CommandContext(ctx, d.docker, "ps",
		"-aq",
		"--filter", "label=com.docker.compose.project="+projectName,
	)
	out, err := listCmd.Output()
	if err != nil {
		return formatCommandError("docker ps failed", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	rmArgs := append([]string{"rm", "-f", "-v"}, ids...)
	rmCmd := exec.CommandContext(ctx, d.docker, rmArgs...)
	rmOut, err := rmCmd.CombinedOutput()
	if err != nil && !isAlreadyGone(rmOut) {
		return fmt.Errorf("docker rm failed: %w\n%s", err, rmOut)
	}
	return nil
}

func (d *composeDriver) removeProjectNetworks(ctx context.Context, projectName string) error {
	listCmd := exec.CommandContext(ctx, d.docker, "network", "ls",
		"-q",
		"--filter", "label=com.docker.compose.project="+projectName,
	)
	out, err := listCmd.Output()
	if err != nil {
		return formatCommandError("docker network ls failed", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	rmArgs := append([]string{"network", "rm"}, ids...)
	rmCmd := exec.CommandContext(ctx, d.docker, rmArgs...)
	rmOut, err := rmCmd.CombinedOutput()
	if err != nil && !isAlreadyGone(rmOut) {
		return fmt.Errorf("docker network rm failed: %w\n%s", err, rmOut)
	}
	return nil
}

// Port queries docker for the host port mapped to 8080 of the project's
// sandbox container. Re-reads on every call so a restarted container with
// a new random port is picked up.
func (d *composeDriver) Port(ctx context.Context, projectName string) (int, error) {
	cmd := exec.CommandContext(ctx, d.docker, "compose",
		"-p", projectName,
		"port", sandboxServiceName, "8080",
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, formatCommandError("docker compose port failed", err)
	}
	// Output is of the form "0.0.0.0:49163" or "[::]:49163".
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0, errors.New("docker compose port returned empty output")
	}
	idx := strings.LastIndex(trimmed, ":")
	if idx < 0 {
		return 0, fmt.Errorf("unexpected port output %q", trimmed)
	}
	portStr := strings.TrimSpace(trimmed[idx+1:])
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse port %q failed: %w", portStr, err)
	}
	return port, nil
}

// List enumerates every quartet-owned sandbox project docker knows
// about. Used at boot to reconcile in-memory state with surviving
// containers. Unknown (non-quartet) projects are ignored.
func (d *composeDriver) List(ctx context.Context) ([]listedContainer, error) {
	cmd := exec.CommandContext(ctx, d.docker, "ps",
		"--format", "{{json .}}",
		"--filter", "label=com.docker.compose.project",
		"--no-trunc",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, formatCommandError("docker ps failed", err)
	}
	var result []listedContainer
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			Labels string
			State  string
			Ports  string
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			logger.Warn("[sandbox.compose] unparseable docker ps line (err=%v): %q", err, line)
			continue
		}
		project := extractLabel(item.Labels, "com.docker.compose.project")
		if !strings.HasPrefix(project, "quartet-sb-") {
			continue
		}
		running := strings.EqualFold(item.State, "running")
		port := parseHostPort(item.Ports, 8080)
		result = append(result, listedContainer{
			ProjectName: project,
			Running:     running,
			Port:        port,
		})
	}
	return result, nil
}

func extractLabel(labels, key string) string {
	for _, kv := range strings.Split(labels, ",") {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

// parseHostPort reads a `docker ps` Ports column like
// "0.0.0.0:49163->8080/tcp, :::49163->8080/tcp" and returns the host side
// of the mapping for the requested container port. Returns 0 when not
// found so the caller can decide whether to re-probe via `compose port`.
// Out-of-range values (docker should never emit them, but a malformed
// entry could) are also treated as "not found".
func parseHostPort(ports string, containerPort int) int {
	token := fmt.Sprintf("->%d/", containerPort)
	for _, part := range strings.Split(ports, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, token) {
			continue
		}
		lhs := strings.Split(part, "->")[0]
		lastColon := strings.LastIndex(lhs, ":")
		if lastColon < 0 {
			continue
		}
		p, err := strconv.Atoi(lhs[lastColon+1:])
		if err != nil || p <= 0 || p > 65535 {
			continue
		}
		return p
	}
	return 0
}

func isAlreadyGone(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "no such project") || strings.Contains(s, "not found") || strings.Contains(s, "no such container")
}

func extractStderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.TrimSpace(string(ee.Stderr))
	}
	if err == io.EOF {
		return "eof"
	}
	return err.Error()
}

func formatCommandError(prefix string, err error) error {
	detail := extractStderr(err)
	if detail == "" || detail == err.Error() {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w (%s)", prefix, err, detail)
}

// maxComposeErrorBytes caps how much compose output we embed in error
// messages. Pull failures routinely emit multi-MB of progress bars; without
// this, one failed compose up can dominate a container log file.
const maxComposeErrorBytes = 8 << 10 // 8 KiB

// truncateComposeOutput keeps the tail of docker compose's combined output —
// the actual error lines are almost always at the end, after the progress
// bars for the preceding layers.
func truncateComposeOutput(out []byte) []byte {
	if len(out) <= maxComposeErrorBytes {
		return out
	}
	truncated := out[len(out)-maxComposeErrorBytes:]
	return append([]byte(fmt.Sprintf("...[truncated %d bytes]...\n", len(out)-maxComposeErrorBytes)), truncated...)
}
