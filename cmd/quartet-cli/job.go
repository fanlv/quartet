package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

const jobUsage = `quartet-cli job — inspect and stop jobs

Usage:
  quartet-cli job <command> [flags]

Commands:
  list       List job summaries (optionally scoped to a workspace)
  get        Print one job's full snapshot as JSON
  stop       Stop a running job

Run "quartet-cli job <command> -h" for command-specific flags.
`

// runJobGroup dispatches the `job` group's subcommands.
func runJobGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, jobUsage)
		return errUsage
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "list", "ls":
		return cmdJobList(rest)
	case "get":
		return cmdJobGet(rest)
	case "stop":
		return cmdJobStop(rest)
	case "-h", "--help", "help":
		fmt.Print(jobUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown job command %q\n\n%s", cmd, jobUsage)
		return errUsage
	}
}

// cmdJobList lists job summaries. Jobs created by workflow runs and scheduled
// tasks show up here, so this is how a script finds a run it started earlier.
func cmdJobList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "only list jobs of this workspace ID")
	limit := fs.Int("limit", 0, "max jobs to return (backend default 50, max 500)")
	asJSON := fs.Bool("json", false, "print the raw summary list as JSON")
	fs.Usage = usageFor("job", fs, "list [--workspace <id>] [--limit N] [--json]",
		"List job summaries, most recently updated first.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}

	resp, err := newClient().listJobs(context.Background(), *workspace, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(resp.Jobs)
	}
	if len(resp.Jobs) == 0 {
		fmt.Fprintln(os.Stderr, "no jobs")
		return nil
	}
	for _, j := range resp.Jobs {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", j.ID, j.Status, j.Mode, formatMillis(&j.CreatedAt), j.Title)
	}
	if resp.HasMore {
		fmt.Fprintf(os.Stderr, "note: more jobs available (next cursor %q); pass --limit to page or --json for the raw page\n", resp.NextCursor)
	}
	return nil
}

// cmdJobGet prints one job's full snapshot (including lastEventSeq, the SSE
// resume cursor) as indented JSON.
func cmdJobGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.Usage = usageFor("job", fs, "get <jobId>", "Print a job's full snapshot as JSON.")
	id, err := parseIDAndFlags(fs, args, "jobId")
	if err != nil {
		return err
	}

	raw, err := newClient().getJob(context.Background(), id)
	if err != nil {
		return err
	}
	return printRawJSON(raw)
}

// cmdJobStop stops a running job (a no-op success when it is not running).
func cmdJobStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.Usage = usageFor("job", fs, "stop <jobId>", "Stop a running job.")
	id, err := parseIDAndFlags(fs, args, "jobId")
	if err != nil {
		return err
	}

	if err := newClient().stopJob(context.Background(), id); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "stopped job %s\n", id)
	return nil
}
