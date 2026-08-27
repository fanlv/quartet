package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/model"
)

const scheduleUsage = `quartet-cli schedule — manage scheduled tasks (cron → graph workflow)

Usage:
  quartet-cli schedule <command> [flags]

Commands:
  create     Create a scheduled task (enabled on this machine by default)
  list       List scheduled tasks
  get        Print one scheduled task as JSON
  update     Update a scheduled task (only the flags you pass change)
  delete     Delete a scheduled task
  toggle     Flip a scheduled task's machine-local enabled state
  run        Trigger a scheduled task immediately (respects max-concurrent)

Run "quartet-cli schedule <command> -h" for command-specific flags.
`

// runScheduleGroup dispatches the `schedule` group's subcommands.
func runScheduleGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, scheduleUsage)
		return errUsage
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "create":
		return cmdScheduleCreate(rest)
	case "list", "ls":
		return cmdScheduleList(rest)
	case "get":
		return cmdScheduleGet(rest)
	case "update":
		return cmdScheduleUpdate(rest)
	case "delete", "rm":
		return cmdScheduleDelete(rest)
	case "toggle":
		return cmdScheduleToggle(rest)
	case "run":
		return cmdScheduleRun(rest)
	case "-h", "--help", "help":
		fmt.Print(scheduleUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown schedule command %q\n\n%s", cmd, scheduleUsage)
		return errUsage
	}
}

// cmdScheduleCreate creates a cron-scheduled graph-workflow run. Unlike
// workflows, scheduled tasks carry no library tag, so there is no
// agent/user boundary to enforce here.
func cmdScheduleCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	name := fs.String("name", "", "task name (required)")
	cron := fs.String("cron", "", `cron expression, 5 fields "M H DoM Mon DoW" in the backend's local time (required)`)
	workflow := fs.String("workflow", "", "graph workflow ID to run (required; see quartet-cli workflow list)")
	workspace := fs.String("workspace", "", "workspace ID to run in (optional; see quartet-cli workspace list)")
	workdir := fs.String("workdir", "", "working directory override (optional)")
	maxConcurrent := fs.Int("max-concurrent", 0, "max concurrent runs; 0 = backend default")
	timeout := fs.Int("timeout", 0, "run timeout in minutes; 0 = backend default")
	disabled := fs.Bool("disabled", false, "create the task disabled on this machine (default: enabled)")
	fs.Usage = usageFor("schedule", fs, "create --name <n> --cron <expr> --workflow <id> [--workspace <id>] [--workdir <dir>] [--max-concurrent N] [--timeout M] [--disabled]",
		"Create a scheduled task that runs a graph workflow on a cron schedule.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("--name is required")
	}
	if strings.TrimSpace(*cron) == "" {
		return fmt.Errorf("--cron is required")
	}
	if strings.TrimSpace(*workflow) == "" {
		return fmt.Errorf("--workflow is required (a scheduled task must reference a graph workflow)")
	}

	req := &model.CreateScheduleRequest{
		Name:            *name,
		CronExpr:        *cron,
		GraphWorkflowID: strings.TrimSpace(*workflow),
		WorkspaceID:     strings.TrimSpace(*workspace),
		Workdir:         strings.TrimSpace(*workdir),
		MaxConcurrent:   *maxConcurrent,
		Timeout:         *timeout,
	}
	if *disabled {
		f := false
		req.Enabled = &f
	}

	resp, err := newClient().createSchedule(context.Background(), req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "created schedule %s\n", resp.Schedule.ID)
	return printJSON(resp.Schedule)
}

// cmdScheduleList lists scheduled tasks, optionally scoped to one workspace.
func cmdScheduleList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "only list tasks of this workspace ID")
	asJSON := fs.Bool("json", false, "print the raw list as JSON")
	fs.Usage = usageFor("schedule", fs, "list [--workspace <id>] [--json]",
		"List scheduled tasks.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}

	resp, err := newClient().listSchedules(context.Background(), strings.TrimSpace(*workspace))
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(resp.Schedules)
	}
	if len(resp.Schedules) == 0 {
		fmt.Fprintln(os.Stderr, "no scheduled tasks")
		return nil
	}
	for _, s := range resp.Schedules {
		fmt.Printf("%s\t%s\t%s\t%s\tnext=%s\n", s.ID, enabledLabel(s.Enabled), s.CronExpr, s.Name, formatMillis(s.NextRunAt))
	}
	return nil
}

// cmdScheduleGet prints one scheduled task as full JSON.
func cmdScheduleGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.Usage = usageFor("schedule", fs, "get <scheduleId>", "Print a scheduled task as full JSON.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}

	info, err := newClient().getSchedule(context.Background(), id)
	if err != nil {
		return err
	}
	return printJSON(info)
}

// cmdScheduleUpdate updates a task. UpdateScheduleRequest is pointer-based, so
// only explicitly-passed flags are sent (nil = leave unchanged).
func cmdScheduleUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	name := fs.String("name", "", "new task name")
	cron := fs.String("cron", "", "new cron expression")
	workflow := fs.String("workflow", "", "new graph workflow ID")
	workspace := fs.String("workspace", "", "new workspace ID")
	workdir := fs.String("workdir", "", "new working directory")
	maxConcurrent := fs.Int("max-concurrent", 0, "new max concurrent runs")
	timeout := fs.Int("timeout", 0, "new run timeout in minutes")
	enable := fs.Bool("enable", false, "enable the task on this machine")
	disable := fs.Bool("disable", false, "disable the task on this machine")
	fs.Usage = usageFor("schedule", fs, "update <scheduleId> [--name <n>] [--cron <expr>] [--workflow <id>] [--workspace <id>] [--workdir <dir>] [--max-concurrent N] [--timeout M] [--enable|--disable]",
		"Update a scheduled task. Only the flags you pass are changed.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}

	set := setFlags(fs)
	if !set["name"] && !set["cron"] && !set["workflow"] && !set["workspace"] && !set["workdir"] &&
		!set["max-concurrent"] && !set["timeout"] && !set["enable"] && !set["disable"] {
		return fmt.Errorf("nothing to update: pass at least one flag (see -h)")
	}
	if *enable && *disable {
		return fmt.Errorf("--enable and --disable are mutually exclusive")
	}

	req := &model.UpdateScheduleRequest{}
	if set["name"] {
		req.Name = name
	}
	if set["cron"] {
		req.CronExpr = cron
	}
	if set["workflow"] {
		req.GraphWorkflowID = workflow
	}
	if set["workspace"] {
		req.WorkspaceID = workspace
	}
	if set["workdir"] {
		req.Workdir = workdir
	}
	if set["max-concurrent"] {
		req.MaxConcurrent = maxConcurrent
	}
	if set["timeout"] {
		req.Timeout = timeout
	}
	if *enable {
		t := true
		req.Enabled = &t
	} else if *disable {
		f := false
		req.Enabled = &f
	}

	info, err := newClient().updateSchedule(context.Background(), id, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "updated schedule %s\n", id)
	return printJSON(info)
}

// cmdScheduleDelete deletes a scheduled task.
func cmdScheduleDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.Usage = usageFor("schedule", fs, "delete <scheduleId>", "Delete a scheduled task.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}

	if err := newClient().deleteSchedule(context.Background(), id); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "deleted schedule %s\n", id)
	return nil
}

// cmdScheduleToggle flips the current machine's enabled state and prints the result.
func cmdScheduleToggle(args []string) error {
	fs := flag.NewFlagSet("toggle", flag.ContinueOnError)
	fs.Usage = usageFor("schedule", fs, "toggle <scheduleId>", "Flip a scheduled task's enabled state on this machine.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}

	info, err := newClient().toggleSchedule(context.Background(), id)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "schedule %s now %s\n", id, enabledLabel(info.Enabled))
	return printJSON(info)
}

// cmdScheduleRun triggers a task immediately via the scheduler (respects
// max-concurrent), prints the created job ID, and leaves a JSON handle on
// stdout for scripting.
func cmdScheduleRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = usageFor("schedule", fs, "run <scheduleId>", "Trigger a scheduled task immediately. Prints the new job ID.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}

	resp, err := newClient().runSchedule(context.Background(), id)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "triggered schedule %s (job %s)\n", id, resp.JobID)
	return printJSON(resp)
}

// enabledLabel renders a task's enabled flag for table output.
func enabledLabel(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

// formatMillis renders an optional UnixMilli timestamp in local time for
// table output ("-" when absent).
func formatMillis(ms *int64) string {
	if ms == nil {
		return "-"
	}
	return time.UnixMilli(*ms).Format("2006-01-02 15:04:05")
}
