package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/einocli/config"
	"github.com/fanlv/quartet/einocli/runtime"
)

// RunHeadlessPrint runs a single one-shot prompt against the default model
// (first catalog entry) and writes the assistant's text to stdout. It powers
// quartet's headless CLI path (`eino-cli -p <prompt>`) used by one-shot text
// flows like job-title generation and IM auto-reply: same agent runtime, no
// ACP transport, and no persisted session (history lands in a temp dir that
// is removed on exit).
func RunHeadlessPrint(ctx context.Context, prompt string, stdout io.Writer) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("empty prompt")
	}

	modelID := defaultModelID()
	if modelID == "" {
		return fmt.Errorf("no model configured; run `eino-cli models add` first")
	}
	m, err := config.GetModel(modelID)
	if err != nil {
		return fmt.Errorf("load model %q failed: %w", modelID, err)
	}
	systemPrompt, err := config.GetSystemPrompt()
	if err != nil {
		return fmt.Errorf("load system prompt failed: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd failed: %w", err)
	}
	dir, err := os.MkdirTemp("", "eino-cli-print-*")
	if err != nil {
		return fmt.Errorf("create temp session dir failed: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	rt, err := runtime.New(ctx, cwd, m.ToModelConfig(""),
		runtime.WithSessionID("headless-print"),
		runtime.WithSessionDir(dir),
		runtime.WithSystemPrompt(systemPrompt),
	)
	if err != nil {
		return err
	}
	defer rt.Close()

	collector := &printCollector{}
	if err := rt.Run(ctx, []*schema.Message{schema.UserMessage(prompt)}, collector); err != nil {
		return err
	}
	if collector.err != nil {
		return collector.err
	}
	text := strings.TrimSpace(collector.text.String())
	if text == "" {
		return fmt.Errorf("empty response")
	}
	_, err = fmt.Fprintln(stdout, text)
	return err
}

// printCollector is the agui.EventHandler for headless print mode: it keeps
// only the assistant message text. Thought chunks and tool events are
// dropped — one-shot callers (title, IM reply) want the final answer text,
// not the reasoning or the tool chatter.
type printCollector struct {
	mu   sync.Mutex
	text strings.Builder
	err  error
}

func (c *printCollector) OnMessageStart() error { return nil }

func (c *printCollector) OnMessageDelta(content string) error {
	c.mu.Lock()
	c.text.WriteString(content)
	c.mu.Unlock()
	return nil
}

func (c *printCollector) OnMessageEnd() error { return nil }

func (c *printCollector) LastMessageID() string { return "" }

func (c *printCollector) OnThoughtStart() error { return nil }

func (c *printCollector) OnThoughtDelta(string) error { return nil }

func (c *printCollector) OnThoughtEnd() error { return nil }

func (c *printCollector) OnToolCallStart(string, string) error { return nil }

func (c *printCollector) OnToolCallArgs(string, string, bool) error { return nil }

func (c *printCollector) OnToolCallResult(string, string, bool) error { return nil }

func (c *printCollector) OnToolCallEnd(string, bool) error { return nil }

func (c *printCollector) OnToolCallInterrupted(string, string) error { return nil }

func (c *printCollector) OnToolCallStitched(string, string, bool, int64) error { return nil }

func (c *printCollector) OnTokenUsage(int) error { return nil }

func (c *printCollector) OnError(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}
