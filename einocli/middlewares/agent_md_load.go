package middlewares

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/agentsmd"
	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	"github.com/fanlv/quartet/einocli/logger"
)

// NewAgentDocLoadMW wires the agentsmd middleware to read AGENTS.md /
// AGENTS.local.md from the sandbox's workdir. Using sb.Workdir here keeps
// write and read in lock-step across both backends (Local form, where
// workdir is a host path, and Container form, where workdir is the
// in-container bind-mount path exposed as sb.Workdir).
func NewAgentDocLoadMW(ctx context.Context, sb sdkSandbox.Sandbox, workdir string) (adk.ChatModelAgentMiddleware, error) {
	if workdir == "" {
		return nil, fmt.Errorf("[NewAgentDocLoadMW] workdir is required")
	}

	// AGENTS.md content can include @import directives that resolve to
	// arbitrary paths via the agentsmd library. Pin the backend to the
	// workspace root so a malicious or buggy import cannot read host
	// files outside the user's workspace.
	backend := newSandboxBackend(sb, workdir)

	files := []string{
		filepath.Join(workdir, "AGENTS.md"),
		filepath.Join(workdir, "AGENTS.local.md"),
	}

	mw, err := agentsmd.New(ctx, &agentsmd.Config{
		Backend:       backend,
		AgentsMDFiles: files,
		OnLoadWarning: func(filePath string, err error) {
			// "file doesn't exist" is the common case (users haven't created
			// an AGENTS.md yet) and belongs at Debug. Real I/O / permission
			// problems indicate a broken workdir and should surface as Warn
			// so they don't get buried in the info stream.
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				logger.Debugf(ctx, "[OnLoadWarning] file %s not present, skipping", filePath)
				return
			}
			logger.Warnf(ctx, "[OnLoadWarning] load file %s failed, err: %v", filePath, err)
		},
	})

	if err != nil {
		return nil, fmt.Errorf("[NewAgentDocLoadMW] create agent doc load middleware failed, err: %w", err)
	}

	return mw, nil
}
