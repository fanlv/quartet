package textgen

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/fanlv/quartet/types/consts"
)

func codexExecutable(bin string, env map[string]string) string {
	executable, configured := env[consts.EnvKeyCodexPath]
	if !configured {
		executable = os.Getenv(consts.EnvKeyCodexPath)
	}
	if executable != "" {
		return executable
	}
	return bin
}

func codexArgs(modelID, thoughtLevel string) []string {
	// Older Codex ACP catalogs encode the reasoning effort in the model ID.
	if name, suffix, ok := strings.Cut(modelID, "["); ok && name != "" {
		if effort, ok := strings.CutSuffix(suffix, "]"); ok && effort != "" {
			modelID = name
			if thoughtLevel == "" {
				thoughtLevel = effort
			}
		}
	}

	// JSON mode keeps the command banner and prompt out of CLI diagnostics.
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "--json", "--color", "never"}
	if modelID != "" {
		args = append(args, "--model", modelID)
	}
	if thoughtLevel != "" && thoughtLevel != "default" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(thoughtLevel))
	}
	return append(args, "-")
}

func codexText(output string) (string, error) {
	var text string
	for line := range strings.SplitSeq(output, "\n") {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Type == "turn.failed" {
			return "", errors.New("Codex reported a generation error")
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" {
			text = event.Item.Text
		}
	}
	return cleanOutput(text), nil
}
