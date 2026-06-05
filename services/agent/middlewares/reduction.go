package middlewares

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/fanlv/quartet/pkg/sandbox"
	"github.com/fanlv/quartet/types/path"
)

func NewReductionMW(ctx context.Context, sessionDir string, sb sandbox.Sandbox) (adk.ChatModelAgentMiddleware, error) {
	rootDir := path.SessionReductionDir(sessionDir)
	// Pin to the reduction root: reduction's offload paths are derived
	// from tool-call IDs, so a forged ID must not be able to escape
	// this directory.
	backend := newSandboxBackend(sb, rootDir)

	return reduction.New(ctx, &reduction.Config{
		Backend:          backend,
		RootDir:          rootDir,
		ReadFileToolName: "Read",
	})
}
