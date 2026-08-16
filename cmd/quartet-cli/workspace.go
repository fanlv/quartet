package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

const workspaceUsage = `quartet-cli workspace — workspace helpers (read-only)

Usage:
  quartet-cli workspace <command> [flags]

Commands:
  list       List workspaces (IDs feed workflow create/schedule create --workspace)

Run "quartet-cli workspace <command> -h" for command-specific flags.
`

// runWorkspaceGroup dispatches the `workspace` group's subcommands.
func runWorkspaceGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, workspaceUsage)
		return errUsage
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "list", "ls":
		return cmdWorkspaceList(rest)
	case "-h", "--help", "help":
		fmt.Print(workspaceUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown workspace command %q\n\n%s", cmd, workspaceUsage)
		return errUsage
	}
}

// cmdWorkspaceList lists workspaces. Read-only: workspace create/update/delete
// stays a Web UI concern; the CLI only needs IDs for workflow/schedule flags.
func cmdWorkspaceList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the raw list as JSON")
	fs.Usage = usageFor("workspace", fs, "list [--json]", "List workspaces.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}

	resp, err := newClient().listWorkspaces(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(resp.Workspaces)
	}
	if len(resp.Workspaces) == 0 {
		fmt.Fprintln(os.Stderr, "no workspaces")
		return nil
	}
	for _, ws := range resp.Workspaces {
		fav := ""
		if ws.Favorite {
			fav = "\tfavorite"
		}
		fmt.Printf("%s\t%s\t%s%s\n", ws.ID, ws.Title, strings.TrimSpace(ws.Workdir), fav)
	}
	return nil
}
