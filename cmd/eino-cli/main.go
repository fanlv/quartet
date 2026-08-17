// eino-cli is the standalone eino agent binary. Its primary mode is `acp`:
// an ACP (Agent Client Protocol) server speaking JSON-RPC over stdio — stdout
// is the protocol channel and carries NOTHING else; all logs go to stderr.
// The remaining subcommands manage the model catalog and system prompt under
// $EINO_HOME (~/.eino by default).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/fanlv/quartet/einocli/app"
	"github.com/fanlv/quartet/einocli/config"
)

// version is reported in the ACP initialize handshake's agentInfo. Override
// at build time with -ldflags "-X main.version=...".
var version = "0.1.0"

const usageText = `usage: eino-cli <command> [args]

commands:
  acp                                       serve ACP over stdio (blocks)
  -p <prompt> [--model <id>] [--thought <auto|enable|disable>]
                                            headless one-shot print: run one prompt and print the reply text
                                            (--model defaults to the first catalog model; --thought overrides thinking_type)
  models list                               list configured models (API keys masked)
  models add [--json '<json>']              add/update a model; reads JSON from stdin without --json
  models delete [--id <id> | <id>]          delete a model
  systemprompt get                          print the system prompt
  systemprompt set [--json '<json>']        set the system prompt; reads raw text from stdin without --json

model JSON (models add):
  {"id?": "...", "model_class": "ark|openai|claude|deepseek|gemini|ollama|qwen",
   "display_name": "...", "connection": {"api_key": "...", "base_url": "...", "model": "..."},
   "thinking_type": "auto|enable|disable"}
`

func main() {
	os.Exit(run(os.Args))
}

func run(args []string) int {
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}

	var err error
	switch args[1] {
	case "acp":
		err = runACP(args[2:])
	case "-p":
		err = runPrint(args[2:])
	case "models":
		err = runModels(args[2:])
	case "systemprompt":
		err = runSystemPrompt(args[2:])
	case "--version", "-v", "version":
		// quartet's usage probe runs `<bin> --version` (same convention as the
		// other agent CLIs); keep the semver on stdout.
		fmt.Printf("eino-cli %s\n", version)
	default:
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	if err != nil {
		// Full error text to stderr; stdout stays clean for JSON results.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func runACP(args []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return app.ServeACP(context.Background(), version)
}

// runPrint implements `eino-cli -p <prompt>`: a headless one-shot run that
// prints the assistant text to stdout. quartet's title / IM-reply flows exec
// this form (see cmd/web/handler/text_generator.go).
func runPrint(args []string) error {
	fs := flag.NewFlagSet("print", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "catalog model id (defaults to the first configured model)")
	thoughtFlag := fs.String("thought", "", "thinking override: auto|enable|disable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("-p requires exactly one positional <prompt>")
	}
	return app.RunHeadlessPrint(context.Background(), fs.Arg(0), *modelFlag, *thoughtFlag, os.Stdout)
}

func runModels(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("models requires a subcommand: list | add | delete")
	}
	switch args[0] {
	case "list":
		return modelsList()
	case "add":
		return modelsAdd(args[1:])
	case "delete":
		return modelsDelete(args[1:])
	default:
		return fmt.Errorf("unknown models subcommand %q (list | add | delete)", args[0])
	}
}

func modelsList() error {
	models, err := config.ListModels()
	if err != nil {
		return err
	}
	masked := make([]*config.Model, 0, len(models))
	for _, m := range models {
		masked = append(masked, config.Masked(m))
	}
	return printJSON(masked)
}

func modelsAdd(args []string) error {
	fs := flag.NewFlagSet("models add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonFlag := fs.String("json", "", "model JSON (stdin when omitted)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	raw, err := flagValueOrStdin(*jsonFlag)
	if err != nil {
		return err
	}

	var m config.Model
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parse model JSON failed: %w", err)
	}
	stored, err := config.AddModel(&m)
	if err != nil {
		return err
	}
	return printJSON(config.Masked(stored))
}

func modelsDelete(args []string) error {
	fs := flag.NewFlagSet("models delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	idFlag := fs.String("id", "", "model id to delete")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := *idFlag
	if id == "" && fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		return fmt.Errorf("models delete requires --id <id> or a positional id")
	}
	if err := config.DeleteModel(id); err != nil {
		return err
	}
	return printJSON(map[string]string{"deleted": id})
}

func runSystemPrompt(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("systemprompt requires a subcommand: get | set")
	}
	switch args[0] {
	case "get":
		prompt, err := config.GetSystemPrompt()
		if err != nil {
			return err
		}
		return printJSON(map[string]string{"system_prompt": prompt})
	case "set":
		return systemPromptSet(args[1:])
	default:
		return fmt.Errorf("unknown systemprompt subcommand %q (get | set)", args[0])
	}
}

func systemPromptSet(args []string) error {
	fs := flag.NewFlagSet("systemprompt set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonFlag := fs.String("json", "", `JSON payload, e.g. {"system_prompt":"..."} (raw text from stdin when omitted)`)
	if err := fs.Parse(args); err != nil {
		return err
	}

	raw, err := flagValueOrStdin(*jsonFlag)
	if err != nil {
		return err
	}
	prompt := string(raw)
	if *jsonFlag != "" {
		var payload struct {
			SystemPrompt string `json:"system_prompt"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("parse --json failed: %w", err)
		}
		prompt = payload.SystemPrompt
	}

	if err := config.SetSystemPrompt(prompt); err != nil {
		return err
	}
	return printJSON(map[string]string{"system_prompt": prompt})
}

// flagValueOrStdin returns the --json flag value when non-empty, otherwise
// reads the payload from stdin. `models add` and `systemprompt set` both
// accept their payload through exactly one of these two channels.
func flagValueOrStdin(flagVal string) ([]byte, error) {
	if flagVal != "" {
		return []byte(flagVal), nil
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin failed: %w", err)
	}
	return raw, nil
}

// printJSON writes v as a single JSON document to stdout — the ONLY thing
// these subcommands ever put there.
func printJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal result failed: %w", err)
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}
