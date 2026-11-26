// Package textgen runs the non-interactive CLI used for title and group replies.
package textgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Generator runs one prompt with the role's model, reasoning and environment.
type Generator struct{}

func (Generator) Generate(ctx context.Context, role model.AgentRoleConfig, bin, prompt string, env map[string]string) (string, error) {
	var args []string
	var stdin string
	var readReply func(string) (string, error)
	switch role.AgentID {
	case "codex":
		bin = codexExecutable(bin, env)
		args = codexArgs(role.ModelID, role.ACPThoughtLevel)
		stdin = prompt
		readReply = codexText
	case "opencode":
		var err error
		args, err = openCodeArgs(ctx, bin, env, role.ModelID, role.ACPThoughtLevel)
		if err != nil {
			return "", err
		}
		stdin = prompt
		readReply = openCodeText
	default:
		args = printArgs(role, bin, prompt)
	}

	logger.Infof(ctx, "[cliGenerate] starting: bin=%s modelId=%q thought=%q promptLen=%d", bin, role.ModelID, role.ACPThoughtLevel, len(prompt))
	start := time.Now()
	output, err := runCLI(ctx, bin, env, stdin, args...)
	elapsed := time.Since(start).Round(time.Millisecond)
	result := output.stdout
	if err == nil && readReply != nil {
		result, err = readReply(output.stdout)
	}
	if err == nil && result == "" {
		err = errors.New("empty response")
	}
	if err != nil {
		return "", fmt.Errorf("run %q failed after %s: %w", bin, elapsed, output.withDiagnostics(err))
	}
	logger.Infof(ctx, "[cliGenerate] %q succeeded: elapsed=%s resultLen=%d", bin, elapsed, len(result))
	return result, nil
}

func printArgs(role model.AgentRoleConfig, bin, prompt string) []string {
	var modelFlag, thoughtFlag string
	name := strings.ToLower(filepath.Base(bin))
	switch {
	case role.AgentID == "eino-cli" || name == "eino-cli":
		modelFlag, thoughtFlag = "--model", "--thought"
	case role.AgentID == "codebuddy" || name == "codebuddy" || name == "cbc":
		modelFlag, thoughtFlag = "--model", "--effort"
	case role.AgentID == "claude" || name == "claude":
		modelFlag, thoughtFlag = "--model", "--effort"
	case role.AgentID == "cursor-agent" || name == "cursor-agent":
		modelFlag = "--model"
	}

	args := []string{"-p"}
	if role.ModelID != "" && modelFlag != "" {
		args = append(args, modelFlag, role.ModelID)
	}
	if role.ACPThoughtLevel != "" && thoughtFlag != "" {
		args = append(args, thoughtFlag, role.ACPThoughtLevel)
	}
	return append(args, prompt)
}

type cliOutput struct {
	stdout string
	stderr string
}

func (o cliOutput) withDiagnostics(err error) error {
	if o.stderr != "" {
		err = fmt.Errorf("%w\nstderr:\n%s", err, o.stderr)
	}
	if o.stdout != "" {
		err = fmt.Errorf("%w\nstdout:\n%s", err, o.stdout)
	}
	return err
}

func runCLI(ctx context.Context, bin string, env map[string]string, stdin string, args ...string) (cliOutput, error) {
	cmd := executil.CommandContext(ctx, bin, args...)
	executil.ConfigureProcessTreeCancellation(cmd)
	cmd.Env = cmd.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		err = errors.Join(err, ctx.Err())
	}
	return cliOutput{stdout: cleanOutput(stdout.String()), stderr: cleanOutput(stderr.String())}, err
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b\(B`)

func cleanOutput(value string) string {
	return strings.TrimSpace(ansiEscape.ReplaceAllString(value, ""))
}
