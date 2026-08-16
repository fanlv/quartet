// Command quartet-cli is the umbrella CLI for driving a Quartet backend from a
// model or a shell. It is organized into command groups:
//
//	quartet-cli workflow <create|list|get|update|delete|validate> [flags]
//	quartet-cli wechat send [--user <id>]... [--file <path>]
//
//   - workflow manages graph workflows in the "agent" library.
//   - wechat send pushes a proactive WeChat message through the backend
//     (POST /api/v1/wechat/send), used by scheduled-job prompts and scripts.
//
// Library boundary for the workflow group (enforced client-side, see
// workflow.go):
//   - create always tags the workflow as type=agent.
//   - update / delete first GET the target and refuse unless its type is agent,
//     so a model can never modify or remove a user-authored ("user") workflow.
//   - get / list are read-only and work across both libraries.
//
// Connection:
//   - Base URL: $QUARTET_BASE_URL (default http://127.0.0.1:8090)
//   - Auth: $X_AGENT_AUTH; when it holds a comma-separated list, the first
//     token is sent as the X-AGENT-AUTH header.
//
// Per the repo convention, every error is printed in full — nothing is hidden.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// errUsage marks a command-line usage problem (unknown/missing command). The
// dispatcher prints the usage text itself; main only maps the error to the
// conventional exit code 2, so os.Exit never appears below the dispatch layer.
var errUsage = errors.New("usage error")

const usage = `quartet-cli — command-line tools for a Quartet backend

Usage:
  quartet-cli <group> <command> [flags]

Groups:
  workflow   Manage graph workflows in the agent library
  wechat     WeChat helpers (send proactive messages via the backend)

Run "quartet-cli <group> -h" for a group's commands, or
"quartet-cli workflow <command> -h" for command-specific flags.

Environment:
  QUARTET_BASE_URL   Backend base URL (default http://127.0.0.1:8090)
  X_AGENT_AUTH       Auth token(s); comma-separated list → first token sent as the X-AGENT-AUTH header
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	group := os.Args[1]
	args := os.Args[2:]

	var err error
	switch group {
	case "workflow", "wf":
		err = runWorkflowGroup(args)
	case "wechat", "wx":
		err = runWeChatGroup(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown group %q\n\n%s", group, usage)
		os.Exit(2)
	}

	if err != nil {
		// A subcommand's -h/--help prints its usage and returns flag.ErrHelp;
		// that is not a failure, so exit cleanly without an error line.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		// A usage problem (unknown/missing command) was already reported with
		// its usage text; exit with the conventional usage-error code 2.
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

const workflowUsage = `quartet-cli workflow — manage Quartet graph workflows (agent library)

Usage:
  quartet-cli workflow <command> [flags]

Commands:
  create     Create a new workflow (always tagged type=agent)
  list       List workflows (default: agent library only)
  get        Print one workflow as full JSON
  update     Update an agent-library workflow
  delete     Delete an agent-library workflow
  validate   Statically validate a workflow config (no persistence)

Run "quartet-cli workflow <command> -h" for command-specific flags.
`

// runWorkflowGroup dispatches the `workflow` group's subcommands.
func runWorkflowGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, workflowUsage)
		return errUsage
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "create":
		return cmdCreate(rest)
	case "list", "ls":
		return cmdList(rest)
	case "get":
		return cmdGet(rest)
	case "update":
		return cmdUpdate(rest)
	case "delete", "rm":
		return cmdDelete(rest)
	case "validate":
		return cmdValidate(rest)
	case "-h", "--help", "help":
		fmt.Print(workflowUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown workflow command %q\n\n%s", cmd, workflowUsage)
		return errUsage
	}
}
