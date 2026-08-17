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
	"github.com/fanlv/quartet/types/model"
)

// generateText runs the configured agent's headless CLI over the supplied
// messages and returns the resulting text. It powers one-shot generation flows
// like job-title summarisation and IM auto-replies, where the caller wants a
// single string back rather than a streaming session.
//
// modelID/thoughtLevel are eino-cli specific overrides plumbed from the role
// config: they are forwarded as `--model`/`--thought` only when the resolved
// binary is eino-cli, so other headless CLIs (which don't accept those flags)
// keep their existing invocation.
func (h *Handler) generateText(ctx context.Context, agentID, modelID, thoughtLevel string, messages []*schema.Message) (string, error) {
	logger.Debugf(ctx, "[generateText] enter: agentId=%s msgCount=%d", agentID, len(messages))

	resolved, found, err := h.agentCatalog.Resolve(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("resolve one-shot AgentID %q failed: %w", agentID, err)
	}
	if !found {
		return "", fmt.Errorf("resolve one-shot AgentID %q failed: Agent does not exist", agentID)
	}
	if resolved.Deprecated {
		return "", fmt.Errorf("one-shot AgentID %q is deprecated", resolved.AgentID)
	}
	if resolved.Lifecycle != "active" {
		return "", fmt.Errorf("one-shot AgentID %q lifecycle is %q, want active", resolved.AgentID, resolved.Lifecycle)
	}
	if !resolved.SupportsHeadlessPrint {
		return "", fmt.Errorf("one-shot AgentID %q does not declare bin -p prompt support", resolved.AgentID)
	}
	binding := model.AgentRuntimeBinding{
		AgentID:    resolved.AgentID,
		Revision:   resolved.Revision,
		RuntimeKey: resolved.RuntimeKey,
		Definition: resolved.Definition,
	}
	releaseExecution, ok := h.agentExecutions.acquireExecution(binding.AgentID)
	if !ok {
		return "", fmt.Errorf("one-shot AgentID %q cannot execute: Agent deletion is in progress", binding.AgentID)
	}
	defer releaseExecution()
	if err := h.ensureBindingAvailable(ctx, binding); err != nil {
		return "", err
	}
	// --model/--thought are eino-cli's headless flags; only forward them when the
	// resolved binary is eino-cli. Other CLIs get the plain `bin -p prompt` form.
	if resolved.Bin != einoHeadlessBin {
		modelID, thoughtLevel = "", ""
	}
	logger.Debugf(ctx, "[generateText] routing to headless CLI: agentId=%q bin=%s modelId=%q thought=%q", resolved.AgentID, resolved.Bin, modelID, thoughtLevel)
	return generateTextWithCLI(ctx, messages, resolved.Bin, modelID, thoughtLevel, h.settingsService.GetACPEnvVars(resolved.AgentID))
}

// einoHeadlessBin is the binary name whose `-p` headless mode accepts the
// --model/--thought flags (see cmd/eino-cli/main.go runPrint).
const einoHeadlessBin = "eino-cli"

// generateTextWithCLI executes an external CLI agent in headless print mode
// to generate text. bin is the agent's plain CLI binary (e.g. "gemini",
// "claude"); the prompt is passed via "-p". This is intentionally NOT the
// agent's ACP serve command, which speaks JSON-RPC over stdio and would
// exit non-zero if invoked this way.
//
// modelID/thoughtLevel, when non-empty, are appended as --model/--thought
// BEFORE the positional prompt (Go's flag parser stops at the first
// positional). Callers must only pass them for binaries that accept them.
func generateTextWithCLI(ctx context.Context, messages []*schema.Message, bin, modelID, thoughtLevel string, configuredEnv map[string]string) (string, error) {
	prompt := messagesToPrompt(messages)
	args := []string{"-p"}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	if thoughtLevel != "" {
		args = append(args, "--thought", thoughtLevel)
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	for key, value := range configuredEnv {
		prefix := key + "="
		filtered := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		cmd.Env = filtered
		cmd.Env = append(cmd.Env, key+"="+value)
	}

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
			if stderrOut != "" {
				return "", fmt.Errorf("run %q aborted after %s: %w (stderr: %s)", bin, elapsed, ctxErr, stderrOut)
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
