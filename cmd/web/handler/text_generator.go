package handler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/modelbuilder"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/consts"
)

// generateText runs the configured agent (eino model or external CLI) over the
// supplied messages and returns the resulting text. It powers one-shot
// generation flows like job-title summarisation and IM auto-replies, where the
// caller wants a single string back rather than a streaming session.
func (h *Handler) generateText(ctx context.Context, agentType, modelID string, messages []*schema.Message) (string, error) {
	logger.Debugf(ctx, "[generateText] enter: agentType=%s modelID=%s msgCount=%d", agentType, modelID, len(messages))
	if agentType == consts.AgentTypeEino {
		logger.Debugf(ctx, "[generateText] routing to eino model: modelID=%s", modelID)
		return h.generateTextWithModel(ctx, modelID, messages)
	}

	// Non-eino agent: resolve the headless one-shot binary. agentType is the
	// ACP *serve* command (e.g. "coco acp serve"); it cannot be exec'd with
	// "-p <prompt>" because that boots the ACP JSON-RPC server instead of
	// running a one-shot. KnownACPAgents records the matching plain CLI bin.
	bin, ok := probe.HeadlessBin(agentType)
	if !ok {
		// Unknown / custom command: fall back to its first token as the bin
		// so user-supplied CLIs that support `-p` still work, but log loudly
		// since this is not a recognised ACP agent.
		fields := strings.Fields(agentType)
		if len(fields) == 0 {
			return "", fmt.Errorf("invalid agentType: %q (empty after trim)", agentType)
		}
		bin = fields[0]
		logger.Warnf(ctx, "[generateText] unknown agentType %q, falling back to headless bin=%s -p", agentType, bin)
	} else {
		logger.Debugf(ctx, "[generateText] routing to headless CLI: agentType=%q bin=%s", agentType, bin)
	}
	return generateTextWithCLI(ctx, messages, bin)
}

func (h *Handler) generateTextWithModel(ctx context.Context, modelID string, messages []*schema.Message) (string, error) {
	logger.Debugf(ctx, "[generateTextWithModel] resolving model config: modelID=%s", modelID)
	modelCfg, err := h.resolveModelCfg(ctx, modelID)
	if err != nil {
		return "", fmt.Errorf("resolve model config: %w", err)
	}

	logger.Debugf(ctx, "[generateTextWithModel] building model: modelID=%s", modelID)
	chatModel, err := modelbuilder.BuildModel(ctx, modelCfg)
	if err != nil {
		return "", fmt.Errorf("build model failed: %w", err)
	}

	logger.Debugf(ctx, "[generateTextWithModel] calling Generate: modelID=%s", modelID)
	start := time.Now()
	resp, err := chatModel.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("generate failed: %w", err)
	}
	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("generate failed: empty response")
	}

	logger.Infof(ctx, "[generateTextWithModel] success: modelID=%s elapsed=%s contentLen=%d", modelID, time.Since(start).Round(time.Millisecond), len(resp.Content))
	return resp.Content, nil
}

// generateTextWithCLI executes an external CLI agent in headless print mode
// to generate text. bin is the agent's plain CLI binary (e.g. "coco",
// "claude"); the prompt is passed via "-p". This is intentionally NOT the
// agent's ACP serve command, which speaks JSON-RPC over stdio and would
// exit non-zero if invoked this way.
func generateTextWithCLI(ctx context.Context, messages []*schema.Message, bin string) (string, error) {
	prompt := messagesToPrompt(messages)
	cmd := exec.CommandContext(ctx, bin, "-p", prompt)
	cmd.Env = os.Environ()

	logger.Infof(ctx, "[cliGenerate] generating: bin=%s promptLen=%d", bin, len(prompt))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Pre-check: verify the CLI binary is resolvable before spending time on
	// exec. This gives a clear "not found" error instead of a cryptic exit status.
	if _, lookErr := exec.LookPath(bin); lookErr != nil {
		return "", fmt.Errorf("cli %q not found in PATH: %w (hint: install it or switch the agent to an LLM model)", bin, lookErr)
	}

	logger.Infof(ctx, "[cliGenerate] starting: bin=%s promptLen=%d", bin, len(prompt))
	start := time.Now()
	if err := cmd.Run(); err != nil {
		elapsed := time.Since(start).Round(time.Millisecond)
		stderrOut := strings.TrimSpace(stripAnsiRaw(stderr.String()))
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Include stderr tail so operators can see whether the CLI was
			// stuck during init, model call, or something else entirely.
			hint := stderrOut
			if len(hint) > 512 {
				hint = hint[len(hint)-512:]
			}
			if hint != "" {
				return "", fmt.Errorf("run %q aborted after %s: %w (stderr tail: %s)", bin, elapsed, ctxErr, hint)
			}
			return "", fmt.Errorf("run %q aborted after %s: %w", bin, elapsed, ctxErr)
		}
		errMsg := stderrOut
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("run %q failed after %s: %s", bin, elapsed, errMsg)
	}

	result := strings.TrimSpace(stripAnsiRaw(stdout.String()))
	if result == "" {
		return "", fmt.Errorf("run %q failed after %s: empty response", bin, time.Since(start).Round(time.Millisecond))
	}

	elapsed := time.Since(start).Round(time.Millisecond)
	logger.Infof(ctx, "[cliGenerate] %q succeeded: elapsed=%s result=%s ", bin, elapsed, result)
	return result, nil
}

func messagesToPrompt(messages []*schema.Message) string {
	var parts []string
	for _, msg := range messages {
		if msg.Content != "" {
			parts = append(parts, msg.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// stripAnsiRaw strips ANSI escape sequences from CLI output but preserves the
// surrounding whitespace and newlines, unlike skills.go's stripAnsi which also
// collapses lines for display.
var ansiRawRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b\(B`)

func stripAnsiRaw(s string) string {
	return ansiRawRegex.ReplaceAllString(s, "")
}
