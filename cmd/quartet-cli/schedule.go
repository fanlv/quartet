package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/google/uuid"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/model"
)

const scheduleUsage = `quartet-cli schedule — manage Prompt and Graph timers

Usage:
  quartet-cli schedule <command> [flags]

Commands:
  create     Create a scheduled task (enabled on this machine by default)
  list       List scheduled tasks
  get        Print one scheduled task as JSON
  update     Update a scheduled task (only the flags you pass change)
  delete     Delete a scheduled task
  enable     Authorize and enable a timer on this backend
  disable    Disable a timer on this backend
  run        Trigger a timer immediately
  history    List execution records (including deleted timers)
  sessions   List sessions produced by timer executions

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
	case "enable", "disable", "toggle":
		return cmdScheduleActivation(cmd, rest)
	case "history", "sessions":
		return cmdTimerHistory(cmd, rest)
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

type timerFlags struct {
	name, cron, workflow, prompt, agent, modelID, workspace, workdir, once, zone, endDate *string
	maxRuns, maxConcurrent, timeout                                                       *int
}

func addTimerFlags(fs *flag.FlagSet) timerFlags {
	return timerFlags{
		name:          fs.String("name", "", "timer name"),
		cron:          fs.String("cron", "", "five-field Cron expression"),
		workflow:      fs.String("workflow", "", "saved Graph workflow ID"),
		prompt:        fs.String("prompt", "", "prompt text"),
		agent:         fs.String("agent", "", "Agent ID for a prompt"),
		modelID:       fs.String("model", "", "model ID for a prompt"),
		workspace:     fs.String("workspace", "", "workspace ID (default: backend default)"),
		workdir:       fs.String("workdir", "", "working directory (default: workspace directory)"),
		once:          fs.String("once", "", "one-time date and time (YYYY-MM-DDTHH:MM or RFC3339)"),
		zone:          fs.String("timezone", "", "IANA time zone (default: backend local)"),
		endDate:       fs.String("end-date", "", "inclusive end date (YYYY-MM-DD)"),
		maxRuns:       fs.Int("max-runs", 0, "maximum accepted executions; 0 = unlimited"),
		maxConcurrent: fs.Int("max-concurrent", 0, "maximum concurrent runs; 0 = backend default"),
		timeout:       fs.Int("timeout", 0, "prompt timeout in minutes; 0 = unlimited"),
	}
}

func cmdScheduleCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	o := addTimerFlags(fs)
	disabled := fs.Bool("disabled", false, "save the timer disabled")
	fs.Usage = usageFor("schedule", fs, "create --name <name> (--prompt <text> | --workflow <id>) (--cron <expr> | --once <time>) [flags]", "Create a timer.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}
	if (*o.prompt == "") == (*o.workflow == "") || (*o.cron == "") == (*o.once == "") {
		return fmt.Errorf("specify exactly one execution target and one timing rule")
	}
	kind := model.TimerGraph
	if *o.prompt != "" {
		kind = model.TimerPrompt
	}
	req := &model.CreateScheduleRequest{
		Name: *o.name, CronExpr: *o.cron, GraphWorkflowID: *o.workflow, WorkspaceID: *o.workspace,
		Workdir: *o.workdir, MaxConcurrent: *o.maxConcurrent, Timeout: *o.timeout,
		TimerSpec: model.TimerSpec{Kind: kind, Prompt: *o.prompt, AgentID: *o.agent, ModelID: *o.modelID,
			RunAt: *o.once, TimeZone: *o.zone, EndDate: *o.endDate, MaxRuns: *o.maxRuns},
	}
	ctx, client := context.Background(), newClient()
	if *disabled {
		enabled := false
		req.Enabled = &enabled
	} else {
		preview, err := client.previewSchedule(ctx, "", req)
		if err != nil {
			return err
		}
		req.ExpectedApproval = preview.ApprovalPreview
	}
	resp, err := client.createSchedule(ctx, req)
	if err != nil {
		return err
	}
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
		fmt.Printf("%s\t%s\t%s\t%s\tnext=%s\n", s.ID, s.PlanState, firstTimerValue(s.RunAt, s.CronExpr), s.Name, formatTimerMillis(s.NextRunAt, s.TimeZone))
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

func cmdScheduleUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	o := addTimerFlags(fs)
	enable := fs.Bool("enable", false, "enable the timer on this backend")
	disable := fs.Bool("disable", false, "disable the timer on this backend")
	fs.Usage = usageFor("schedule", fs, "update <scheduleId> [flags]", "Update the supplied timer fields.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}
	set := setFlags(fs)
	if len(set) == 0 || *enable && *disable || set["workflow"] && set["prompt"] || set["once"] && set["cron"] {
		return fmt.Errorf("provide at least one update and choose one value per option")
	}
	req := &model.UpdateScheduleRequest{}
	for name, field := range map[string]struct {
		value *string
		dest  **string
	}{
		"name": {o.name, &req.Name}, "cron": {o.cron, &req.CronExpr}, "workflow": {o.workflow, &req.GraphWorkflowID},
		"prompt": {o.prompt, &req.Prompt}, "agent": {o.agent, &req.AgentID}, "model": {o.modelID, &req.ModelID},
		"workspace": {o.workspace, &req.WorkspaceID}, "workdir": {o.workdir, &req.Workdir}, "once": {o.once, &req.RunAt},
		"timezone": {o.zone, &req.TimeZone}, "end-date": {o.endDate, &req.EndDate},
	} {
		if set[name] {
			*field.dest = field.value
		}
	}
	for name, field := range map[string]struct {
		value *int
		dest  **int
	}{
		"max-runs": {o.maxRuns, &req.MaxRuns}, "max-concurrent": {o.maxConcurrent, &req.MaxConcurrent}, "timeout": {o.timeout, &req.Timeout},
	} {
		if set[name] {
			*field.dest = field.value
		}
	}
	if set["prompt"] && *o.prompt != "" {
		kind := model.TimerPrompt
		req.Kind = &kind
	} else if set["workflow"] && *o.workflow != "" {
		kind := model.TimerGraph
		req.Kind = &kind
	}
	if set["cron"] {
		once := ""
		req.RunAt = &once
	}
	if *enable {
		req.Enabled = enable
	} else if *disable {
		enabled := false
		req.Enabled = &enabled
	}
	ctx, client := context.Background(), newClient()
	if !*disable && (len(set) > 1 || !set["name"]) {
		preview, err := client.previewSchedule(ctx, id, req)
		if err != nil {
			return err
		}
		req.ExpectedApproval = preview.ApprovalPreview
	}
	info, err := client.updateSchedule(ctx, id, req)
	if err != nil {
		return err
	}
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

func cmdScheduleActivation(action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	expected := fs.String("expected-approval", "", "approval preview from schedule get (protects against concurrent changes)")
	fs.Usage = usageFor("schedule", fs, action+" <scheduleId> [--expected-approval <preview>]", "Update the local timer activation.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}
	ctx, client := context.Background(), newClient()
	enabled := action == "enable"
	if action == "toggle" || enabled && *expected == "" {
		info, err := client.getSchedule(ctx, id)
		if err != nil {
			return err
		}
		if action == "toggle" {
			enabled = !info.Enabled
		}
		if *expected == "" {
			*expected = info.ApprovalPreview
		}
	}
	info, err := client.updateSchedule(ctx, id, &model.UpdateScheduleRequest{Enabled: &enabled, ExpectedApproval: *expected})
	if err != nil {
		return err
	}
	return printJSON(info)
}

func cmdScheduleRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	submission := fs.String("request-id", uuid.NewString(), "reuse this ID when retrying the same request")
	fs.Usage = usageFor("schedule", fs, "run <scheduleId> [--request-id <id>]", "Request an immediate execution.")
	id, err := parseIDAndFlags(fs, args, "scheduleId")
	if err != nil {
		return err
	}
	resp, err := newClient().runSchedule(context.Background(), id, *submission)
	if err != nil {
		return fmt.Errorf("request ID %s: %w", *submission, err)
	}
	return printJSON(resp)
}

func cmdTimerHistory(action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	id := fs.String("timer", "", "timer ID")
	workspace := fs.String("workspace", "", "workspace ID (history)")
	state := fs.String("status", "", "execution status (history)")
	execution := fs.String("execution", "", "execution ID (sessions)")
	cursor := fs.String("cursor", "", "cursor returned by the previous page")
	fs.Usage = usageFor("schedule", fs, action+" [flags]", "List timer execution records or sessions.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}
	endpoint := "executions"
	if action == "sessions" {
		endpoint = action
	}
	query := url.Values{"scheduleId": {*id}, "workspaceId": {*workspace}, "status": {*state}, "executionId": {*execution}, "cursor": {*cursor}}
	raw, err := newClient().get(context.Background(), "/api/v1/schedule/"+endpoint, query)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

func firstTimerValue(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func formatTimerMillis(ms *int64, zone string) string {
	if ms == nil {
		return "-"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.Local
	}
	return time.UnixMilli(*ms).In(location).Format("2006-01-02 15:04 MST")
}

func formatMillis(ms *int64) string {
	if ms == nil {
		return "-"
	}
	return time.UnixMilli(*ms).Format("2006-01-02 15:04:05")
}
