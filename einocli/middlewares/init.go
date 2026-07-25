package middlewares

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	"github.com/fanlv/quartet/einocli/store"
)

type InitConfig struct {
	ChatModel       model.BaseChatModel
	Workspace       string
	SessionDir      string
	Sandbox         sdkSandbox.Sandbox
	ChatContextRepo store.ChatContextRepo
	// ToolFailures, when non-nil, is shared with the eino roundAdapter so
	// tool errors swallowed by toolWrap's "return err text, nil" pattern
	// can still be surfaced to live UI and on-disk history as failed
	// tool calls. Nil is legal; failure tagging simply degrades.
	ToolFailures *sync.Map
}

func Init(ctx context.Context, cfg *InitConfig) ([]adk.ChatModelAgentMiddleware, error) {
	summarizationMW, err := NewSummarizationMW(ctx, cfg.ChatModel, cfg.ChatContextRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to create summarization middleware: %w", err)
	}

	planTaskMW, err := NewPlanTaskMW(ctx, cfg.SessionDir, cfg.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("failed to create plan task middleware: %w", err)
	}

	agentDocLoadMW, err := NewAgentDocLoadMW(ctx, cfg.Sandbox, cfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent doc load middleware: %w", err)
	}

	reductionMW, err := NewReductionMW(ctx, cfg.SessionDir, cfg.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("failed to create reduction middleware: %w", err)
	}

	// Middleware order matters:
	// 1. summarization  — must run first to compress history before other middlewares see it
	// 2. planTask       — manages task planning on the (possibly summarized) context
	// 3. agentDocLoad   — injects agent capability docs into the prompt
	// 4. toolWrap       — wraps model/tool calls with logging and error handling
	// 5. reduction      — runs last to trim token budget after all other processing
	return []adk.ChatModelAgentMiddleware{
		summarizationMW,
		planTaskMW,
		agentDocLoadMW,
		NewToolWrapMiddleware(cfg.ToolFailures),
		reductionMW,
	}, nil
}
