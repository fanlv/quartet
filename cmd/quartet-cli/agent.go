package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

const agentUsage = `quartet-cli agent — ACP agent helpers (read-only)

Usage:
  quartet-cli agent <command> [flags]

Commands:
  list       List installed ACP agents with their available models/modes

Run "quartet-cli agent <command> -h" for command-specific flags.
`

// runAgentGroup dispatches the `agent` group's subcommands.
func runAgentGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, agentUsage)
		return errUsage
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "list", "ls":
		return cmdAgentList(rest)
	case "-h", "--help", "help":
		fmt.Print(agentUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown agent command %q\n\n%s", cmd, agentUsage)
		return errUsage
	}
}

// cmdAgentList lists the installed ACP agents. Orchestrating a workflow needs
// these agent types (node executors) and their model/mode catalogs.
func cmdAgentList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the raw agent list as JSON (includes full model/mode catalogs)")
	fs.Usage = usageFor("agent", fs, "list [--json]", "List installed ACP agents.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}

	resp, err := newClient().listAgents(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(resp.AgentList)
	}
	if len(resp.AgentList) == 0 {
		fmt.Fprintln(os.Stderr, "no agents installed")
		return nil
	}
	for _, a := range resp.AgentList {
		models := 0
		current := "-"
		if a.Models != nil {
			models = len(a.Models.AvailableModels)
			if a.Models.CurrentModelId != "" {
				current = a.Models.CurrentModelId
			}
		}
		fmt.Printf("%s\t%s\tmodels=%d\tcurrent=%s\n", a.Type, a.DisplayName, models, current)
	}
	return nil
}
