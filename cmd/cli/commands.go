package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

// agentType is the library tag this CLI manages. create forces it; update and
// delete refuse to touch anything else.
const agentType = model.GraphWorkflowTypeAgent

// cmdCreate creates a new workflow, always tagged type=agent.
func cmdCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	name := fs.String("name", "", "workflow name (required)")
	desc := fs.String("description", "", "workflow description")
	workspace := fs.String("workspace", "", "workspace ID to bind (optional)")
	configFile := fs.String("config-file", "", "path to GraphConfig JSON; reads stdin when omitted or '-'")
	fs.Usage = usageFor(fs, "create --name <name> [--description <d>] [--workspace <id>] [--config-file <path>]",
		"Create a workflow in the agent library. The config JSON may be a bare GraphConfig or a {\"config\": {...}} wrapper.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("--name is required")
	}

	cfg, err := readConfigInput(*configFile)
	if err != nil {
		return err
	}

	ctx := context.Background()
	resp, err := newClient().createWorkflow(ctx, &model.CreateGraphWorkflowRequest{
		Name:        *name,
		Description: *desc,
		Type:        agentType,
		WorkspaceID: *workspace,
		Config:      *cfg,
	})
	if err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("workflow config is invalid:\n%s", formatValidationErrors(resp.Errors))
	}
	fmt.Fprintf(os.Stderr, "created workflow %s (type=%s)\n", resp.Workflow.ID, resp.Workflow.Type)
	return printJSON(resp.Workflow)
}

// cmdList lists workflows, filtered by library type (default agent).
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	typeFilter := fs.String("type", "agent", "library filter: user | agent | all")
	asJSON := fs.Bool("json", false, "print the raw summary list as JSON")
	fs.Usage = usageFor(fs, "list [--type user|agent|all] [--json]",
		"List workflow summaries. Defaults to the agent library.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *typeFilter {
	case "user", "agent", "all":
	default:
		return fmt.Errorf("--type must be one of: user, agent, all (got %q)", *typeFilter)
	}

	ctx := context.Background()
	resp, err := newClient().listWorkflows(ctx)
	if err != nil {
		return err
	}

	filtered := make([]model.GraphWorkflowSummary, 0, len(resp.Workflows))
	for _, wf := range resp.Workflows {
		libType := wf.Type
		if libType == "" {
			libType = model.GraphWorkflowTypeUser
		}
		if *typeFilter == "all" || string(libType) == *typeFilter {
			filtered = append(filtered, wf)
		}
	}

	// Surface any files the backend skipped (unreadable / malformed) so a bad
	// file does not silently vanish from the listing.
	for _, w := range resp.Warnings {
		fmt.Fprintf(os.Stderr, "warning: skipped %s: %s\n", w.File, w.Error)
	}

	if *asJSON {
		return printJSON(filtered)
	}

	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "no workflows")
		return nil
	}
	for _, wf := range filtered {
		libType := wf.Type
		if libType == "" {
			libType = model.GraphWorkflowTypeUser
		}
		fmt.Printf("%s\t%s\t%s\tnodes=%d edges=%d\n", wf.ID, libType, wf.Name, wf.NodeCount, wf.EdgeCount)
	}
	return nil
}

// cmdGet prints one workflow as full JSON. Read-only: any library type allowed.
func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.Usage = usageFor(fs, "get <workflowId>", "Print a workflow as full JSON.")
	id, err := parseIDAndFlags(fs, args)
	if err != nil {
		return err
	}

	resp, err := newClient().getWorkflow(context.Background(), id)
	if err != nil {
		return err
	}
	return printJSON(resp.Workflow)
}

// cmdUpdate updates an agent-library workflow. It first fetches the target to
// (a) enforce the library boundary and (b) read updatedAt for the optimistic
// lock the backend requires.
func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	name := fs.String("name", "", "new name")
	desc := fs.String("description", "", "new description")
	configFile := fs.String("config-file", "", "path to new GraphConfig JSON; reads stdin when '-'")
	fs.Usage = usageFor(fs, "update <workflowId> [--name <n>] [--description <d>] [--config-file <path>]",
		"Update an agent-library workflow. Only the flags you pass are changed. Refuses non-agent workflows.")
	id, err := parseIDAndFlags(fs, args)
	if err != nil {
		return err
	}

	// Record which flags were explicitly set so we only send those fields
	// (UpdateGraphWorkflowRequest uses pointers; nil = leave unchanged).
	set := setFlags(fs)
	if !set["name"] && !set["description"] && !set["config-file"] {
		return fmt.Errorf("nothing to update: pass at least one of --name, --description, --config-file")
	}

	client := newClient()
	ctx := context.Background()
	current, err := client.getWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if err := ensureAgentOwned(current.Workflow); err != nil {
		return err
	}

	req := &model.UpdateGraphWorkflowRequest{UpdatedAt: &current.Workflow.UpdatedAt}
	if set["name"] {
		req.Name = name
	}
	if set["description"] {
		req.Description = desc
	}
	if set["config-file"] {
		cfg, err := readConfigInput(*configFile)
		if err != nil {
			return err
		}
		req.Config = cfg
	}

	resp, err := client.updateWorkflow(ctx, id, req)
	if err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("workflow config is invalid:\n%s", formatValidationErrors(resp.Errors))
	}
	fmt.Fprintf(os.Stderr, "updated workflow %s\n", id)
	return printJSON(resp.Workflow)
}

// cmdDelete deletes an agent-library workflow after the same boundary check.
func cmdDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.Usage = usageFor(fs, "delete <workflowId>", "Delete an agent-library workflow. Refuses non-agent workflows.")
	id, err := parseIDAndFlags(fs, args)
	if err != nil {
		return err
	}

	client := newClient()
	ctx := context.Background()
	current, err := client.getWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if err := ensureAgentOwned(current.Workflow); err != nil {
		return err
	}

	if err := client.deleteWorkflow(ctx, id, &model.DeleteGraphWorkflowRequest{UpdatedAt: &current.Workflow.UpdatedAt}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "deleted workflow %s\n", id)
	return nil
}

// cmdValidate statically validates a config without persisting it.
func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	configFile := fs.String("config-file", "", "path to GraphConfig JSON; reads stdin when omitted or '-'")
	fs.Usage = usageFor(fs, "validate [--config-file <path>]",
		"Statically validate a GraphConfig. Exits non-zero when invalid.")
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}

	cfg, err := readConfigInput(*configFile)
	if err != nil {
		return err
	}

	resp, err := newClient().validateConfig(context.Background(), &model.ValidateGraphWorkflowRequest{Config: *cfg})
	if err != nil {
		return err
	}
	if resp.Valid {
		fmt.Fprintln(os.Stderr, "valid")
		return nil
	}
	return fmt.Errorf("invalid config:\n%s", formatValidationErrors(resp.Errors))
}

// ensureAgentOwned enforces the library boundary: the CLI may only modify or
// delete workflows in the agent library.
func ensureAgentOwned(wf *model.GraphWorkflow) error {
	if wf == nil {
		return fmt.Errorf("workflow not found")
	}
	if wf.Type != agentType {
		libType := wf.Type
		if libType == "" {
			libType = model.GraphWorkflowTypeUser
		}
		return fmt.Errorf("refusing to modify workflow %s: it belongs to the %q library, not %q. The CLI may only change agent-library workflows", wf.ID, libType, agentType)
	}
	return nil
}

// parseIDAndFlags splits a single workflow-ID positional from flag arguments
// and parses the flags into fs. Go's flag package stops parsing at the first
// non-flag token, so a natural `update <id> --name X` would leave `--name X`
// unparsed. This separates the bare ID token from the flags so the ID may
// appear before or after them.
//
// The scan is flag-value-aware: a non-boolean flag written in `--name` form
// (no `=`) consumes the following token as its value, so that value is never
// mistaken for the ID (the bug that a naive "first bare token" scan caused for
// `--name X <id>`). Exactly one bare positional (the ID) is required; any extra
// is surfaced as an error.
func parseIDAndFlags(fs *flag.FlagSet, args []string) (string, error) {
	var id string
	idSet := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a != "-" && strings.HasPrefix(a, "-") {
			// A flag token. Keep it, and if it is a value-taking flag in
			// "--name" form (no '='), also pull its value token so the value
			// is not later read as the ID.
			rest = append(rest, a)
			if !strings.Contains(a, "=") {
				name := strings.TrimLeft(a, "-")
				if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
					rest = append(rest, args[i+1])
					i++
				}
			}
			continue
		}
		if !idSet {
			id = a
			idSet = true
			continue
		}
		// A second bare token: leave it for fs.Parse → fs.Args() to report.
		rest = append(rest, a)
	}
	if err := fs.Parse(rest); err != nil {
		return "", err
	}
	if len(fs.Args()) > 0 {
		return "", fmt.Errorf("unexpected extra arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("exactly one <workflowId> argument is required")
	}
	return id, nil
}

// parseFlagsNoArgs parses flags and rejects any leftover positional arguments,
// so a mistyped `create --name x foo.json` (where foo.json was meant as
// --config-file) fails loudly instead of silently dropping foo.json.
func parseFlagsNoArgs(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected extra arguments: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

// isBoolFlag reports whether a flag is boolean (its value implements the
// optional IsBoolFlag method returning true), i.e. it does NOT take a separate
// value token.
func isBoolFlag(f *flag.Flag) bool {
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return bf.IsBoolFlag()
	}
	return false
}

// setFlags returns the set of flag names that were explicitly provided.
func setFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// usageFor builds a per-command usage function that prints a one-line synopsis,
// a short description, and the flag defaults.
func usageFor(fs *flag.FlagSet, synopsis, desc string) func() {
	return func() {
		fmt.Fprintf(os.Stderr, "Usage: quartet-cli workflow %s\n\n%s\n\nFlags:\n", synopsis, desc)
		fs.PrintDefaults()
	}
}
