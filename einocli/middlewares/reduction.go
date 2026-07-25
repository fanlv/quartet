package middlewares

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	"github.com/fanlv/quartet/einocli/store"
)

func NewReductionMW(ctx context.Context, sessionDir string, sb sdkSandbox.Sandbox) (adk.ChatModelAgentMiddleware, error) {
	rootDir := store.ReductionDir(sessionDir)
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
