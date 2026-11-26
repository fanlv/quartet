package textgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

func openCodeArgs(ctx context.Context, bin string, env map[string]string, modelID, variant string) ([]string, error) {
	helpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := runCLI(helpCtx, bin, env, "", "run", "--help")
	if err != nil {
		return nil, fmt.Errorf("read OpenCode run options: %w", output.withDiagnostics(err))
	}
	// OpenCode 1 prints help to stderr when stdout is redirected.
	options := strings.Fields(output.stdout + "\n" + output.stderr)
	hasVariantFlag := slices.Contains(options, "--variant")
	if variant == "default" {
		variant = ""
	}
	args := []string{"run", "--format", "json"}
	// The private server uses this invocation's environment, including the
	// Agent's provider settings. Archiving keeps the run out of active chats.
	if slices.Contains(options, "--standalone") {
		args = append(args, "--standalone")
	}
	if slices.Contains(options, "--archived") {
		args = append(args, "--archived")
	}
	if slices.Contains(options, "--print-logs") && slices.Contains(options, "--log-level") {
		level := "error"
		if hasVariantFlag {
			level = "ERROR"
		}
		args = append(args, "--print-logs", "--log-level", level)
	}
	if variant != "" {
		switch {
		case hasVariantFlag:
			args = append(args, "--variant", variant)
		case modelID == "":
			return nil, fmt.Errorf("OpenCode requires a model to use reasoning variant %q", variant)
		default: // OpenCode 2 accepts the variant as part of the model reference.
			modelID, _, _ = strings.Cut(modelID, "#")
			modelID += "#" + variant
		}
	}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	return args, nil
}

func openCodeText(output string) (string, error) {
	var messageID, partID string
	var text strings.Builder
	for line := range strings.SplitSeq(output, "\n") {
		var event struct {
			Type string `json:"type"`
			Part struct {
				ID        string `json:"id"`
				MessageID string `json:"messageID"`
				Text      string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		switch event.Type {
		case "error":
			return "", errors.New("OpenCode reported a generation error")
		case "text":
			// A later assistant message may follow tool calls. Keep the
			// latest message and append updates to the same text part.
			if event.Part.MessageID != messageID {
				text.Reset()
				messageID, partID = event.Part.MessageID, ""
			}
			if text.Len() > 0 && (event.Part.ID == "" || event.Part.ID != partID) {
				text.WriteByte('\n')
			}
			text.WriteString(event.Part.Text)
			partID = event.Part.ID
		}
	}
	return cleanOutput(text.String()), nil
}
