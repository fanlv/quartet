package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	pkgacp "github.com/fanlv/quartet/pkg/acp"
	svcacp "github.com/fanlv/quartet/services/agent/acp"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/agentstream"
)

const defaultPrompt = "Use the terminal tool to run `printf GROK_TOOL_OK`. Then reply with exactly GROK_ACP_DEMO_OK."

type streamPrinter struct {
	mu sync.Mutex
}

func (p *streamPrinter) print(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Printf(format, args...)
}

func (p *streamPrinter) OnMessageChunk(text string) {
	p.print("%s", text)
}

func (p *streamPrinter) OnThoughtChunk(text string) {
	p.print("[thought] %s", text)
}

func (p *streamPrinter) OnToolCall(id, title string) {
	p.print("\n[tool-call] id=%s title=%s\n", id, title)
}

func (p *streamPrinter) OnToolCallUpdate(id, content string, status agentstream.ToolCallStatus) {
	p.print("[tool-update] id=%s status=%d content=%s\n", id, status, content)
}

func (p *streamPrinter) OnToolCallArgsSnapshot(id, args string) {
	p.print("[tool-args] id=%s args=%s\n", id, args)
}

func (p *streamPrinter) OnTokenUsage(totalTokens int) {
	p.print("[context-usage] total=%d\n", totalTokens)
}

func main() {
	timeout := flag.Duration("timeout", 2*time.Minute, "overall ACP prompt timeout")
	workdir := flag.String("workdir", "", "session working directory (default: current directory)")
	flag.Parse()

	if *workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fail("get current directory", err)
		}
		*workdir = cwd
	}
	promptText := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if promptText == "" {
		promptText = defaultPrompt
	}

	command, ok := grokACPCommand()
	if !ok {
		fail("find Grok ACP command", fmt.Errorf("Grok is not registered in probe.KnownACPAgents"))
	}
	probe.InitAllowedAgentCommands()
	svcacp.InitEnvProvider()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, err := pkgacp.NewProbeConn(ctx, command, *workdir)
	if err != nil {
		fail("connect Grok ACP", err)
	}
	defer conn.Close()

	session, err := conn.NewSession(ctx, *workdir)
	if err != nil {
		fail("create Grok ACP session", err)
	}
	fmt.Printf("[session] id=%s\n", session.SessionID)
	if model := session.ModelConfigSelect(); model != nil {
		fmt.Printf("[model] current=%s options=%d\n", model.CurrentValue, len(model.Options))
	}
	if mode := session.ModeConfigSelect(); mode != nil {
		fmt.Printf("[mode] current=%s options=%d\n", mode.CurrentValue, len(mode.Options))
	}

	sessionID := pkgacp.SessionID(session.SessionID)
	handlerGen := conn.SetStreamHandler(sessionID, &streamPrinter{})
	defer conn.ClearStreamHandlerIfGen(sessionID, handlerGen)

	slot, err := conn.AcquirePromptSlot(ctx, sessionID)
	if err != nil {
		fail("acquire Grok ACP prompt slot", err)
	}
	defer slot.Release()

	result, err := slot.SendPrompt(ctx, promptText)
	if err != nil {
		fail("send Grok ACP prompt", err)
	}
	fmt.Printf("\n[result] stopReason=%s usage=%+v\n", result.StopReason, result.Usage)
}

func grokACPCommand() (string, bool) {
	for _, agent := range probe.KnownACPAgents {
		if agent.Bin == "grok" {
			return agent.Command, true
		}
	}
	return "", false
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "%s failed: %v\n", action, err)
	os.Exit(1)
}
