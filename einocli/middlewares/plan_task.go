package middlewares

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	"github.com/fanlv/quartet/einocli/store"
)

func NewPlanTaskMW(ctx context.Context, sessionDir string, sb sdkSandbox.Sandbox) (adk.ChatModelAgentMiddleware, error) {
	baseDir := store.TasksDir(sessionDir)
	// Pin to the plantask base dir: plantask should never touch files
	// outside its own task store.
	backend := newSandboxBackend(sb, baseDir)

	mw, err := plantask.New(ctx, &plantask.Config{
		Backend: backend,
		BaseDir: baseDir,
	})

	if err != nil {
		return nil, fmt.Errorf("[NewPlanTaskMW] create plan task middleware failed, err: %w", err)
	}

	return mw, nil
}
