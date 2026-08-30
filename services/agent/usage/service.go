package usage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/agent/runtimeenv"
	"github.com/fanlv/quartet/services/config"
	"github.com/fanlv/quartet/types/model"
)

// Service fetches the current subscription / quota info for the Codex and
// Claude ACP agents. Each call fetches live data — the Home page requests a
// refresh on every agent-type switch, so nothing is cached here.
type Service interface {
	CodexUsage(ctx context.Context) (*model.CodexUsage, error)
	ClaudeUsage(ctx context.Context) (*model.ClaudeUsage, error)
	// AntigravityUsage returns the Antigravity (agy) plan quota + agy CLI
	// version, read from the local agy language-server RPC.
	AntigravityUsage(ctx context.Context) (*model.AntigravityUsage, error)
	// KimiUsage returns the Kimi Code plan quota + kimi CLI version, read from
	// the kimi usages endpoint with the local kimi CLI's OAuth credentials.
	KimiUsage(ctx context.Context) (*model.KimiUsage, error)
	// QoderUsage returns the QoderCN credits quota + qoderclicn CLI version,
	// read from the openapi quota endpoint with the local CLI's decrypted token.
	QoderUsage(ctx context.Context) (*model.QoderUsage, error)
	// AgentVersion returns the installed CLI version of a known ACP agent,
	// resolved from its serve command. Used by agents that have no quota view.
	AgentVersion(ctx context.Context, command string) (string, error)
}

type serviceImpl struct {
	settings config.SettingsService

	// agy is a short-lived per-request process, so a usage poll frequently misses
	// its live window. These fields hold the last successful Antigravity quota so
	// AntigravityUsage can fall back to it instead of failing. See antigravity.go.
	agyMu       sync.Mutex
	agyCache    *model.AntigravityUsage
	agyCachedAt time.Time
}

// NewService builds the usage service. It depends on the settings service to
// resolve the environment used by Codex and Claude ACP adapters.
func NewService(settings config.SettingsService) Service {
	return &serviceImpl{settings: settings}
}

// acpCommandByBin resolves an ACP agent's full serve command from the
// known-agent registry (e.g. "codex" -> "codex-acp").
func acpCommandByBin(bin string) string {
	for _, a := range probe.KnownACPAgents {
		if a.Bin == bin {
			return a.Command
		}
	}
	return ""
}

func (s *serviceImpl) effectiveACPEnv(bin string) map[string]string {
	command := acpCommandByBin(bin)
	if command == "" {
		return nil
	}
	return (runtimeenv.Resolver{}).Effective(command, s.settings.GetACPEnvVars(command))
}

func applyCommandEnv(cmd *exec.Cmd, values map[string]string) {
	if len(values) == 0 {
		return
	}
	cmd.Env = os.Environ()
	for key, value := range values {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
}

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// AgentVersion returns the installed CLI version for a known ACP agent,
// resolved from its serve command (e.g. "opencode acp" -> runs
// `opencode --version`). It errors only when command is not a known ACP agent;
// an agent whose binary advertises no parseable version yields "" with no error
// so the UI simply shows nothing for it.
func (s *serviceImpl) AgentVersion(ctx context.Context, command string) (string, error) {
	bin, ok := probe.HeadlessBin(command)
	if !ok {
		return "", fmt.Errorf("unknown ACP agent command %q", command)
	}
	return s.binVersion(ctx, bin), nil
}

// binVersion runs `<bin> --version` and returns the first semver found in
// stdout, prefixed with "v" (e.g. "v1.17.18"), or "" on any failure. `--version`
// is the near-universal convention across the known agent CLIs — traex, opencode,
// codex, claude, grok, qoder, ... all print a semver to stdout for it.
// Reads stdout only so a warning printed to stderr can't false-match a version.
func (s *serviceImpl) binVersion(ctx context.Context, bin string) string {
	if bin == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := executil.CommandContext(cctx, bin, "--version").Output()
	if err != nil {
		logger.Warnf(ctx, "[agent.usage] bin version probe failed: bin=%q err=%v", bin, err)
		return ""
	}
	m := semverRe.Find(out)
	if m == nil {
		return ""
	}
	return "v" + string(m)
}
