// Package command implements the platform-agnostic slash-command layer shared
// by IM (Lark / WeChat) and the Web chat page.
//
// The three-layer model:
//
//  1. Definition (this file): command name, aliases, description. Pure data.
//  2. Execution (this package's Execute): takes (name, args, context), returns
//     a structured Result describing what should happen and what to show the
//     user. Knows nothing about IMJobMapping, URLs, or SSE.
//  3. Platform adapter (outside this package): each platform translates the
//     Result into its own state changes — IM writes to IMJobMapping and sends
//     a text reply; Web updates URL + currentWorkspace / currentJob state and
//     pushes a `command_system_message` SSE event.
package command

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// ActionType describes the side effect a command wants the caller to apply.
// Adapters pattern-match on this and update their own state accordingly.
type ActionType string

const (
	// ActionNone — purely display; no state change.
	ActionNone ActionType = ""
	// ActionSwitchWorkspace — change the current workspace context. IM updates
	// IMJobMapping; Web updates URL + currentWorkspace and then runs its
	// "reuse-or-create Job" logic for the chat page.
	ActionSwitchWorkspace ActionType = "switch_workspace"
	// ActionBindJob — switch to the given Job. IM rebinds the mapping; Web
	// reloads its chat-page state (URL, SSE, messages, etc.).
	ActionBindJob ActionType = "bind_job"
	// ActionNewJob — create a new Job in the current workspace and switch to
	// it. Adapters carry out the create+switch.
	ActionNewJob ActionType = "new_job"
)

// PresentType tells the adapter how to render Result.Message.
type PresentType string

const (
	// PresentInline — render as an in-chat system message (default).
	PresentInline PresentType = "inline"
	// PresentToast — render as a transient UI toast (Web only; IM treats it
	// as inline).
	PresentToast PresentType = "toast"
)

// Platform identifies the calling surface. Used to keep legacy IM output
// text intact while Web gets its cleaner equivalents. Also gates behavior
// differences like /job use cross-workspace:
// Web supports cross-ws binding (URL + currentWorkspace are updated together
// by the adapter); IM keeps the old "same workspace only" semantics since
// an IM chat is pinned to one workspace at a time via IMJobMapping.
type Platform string

const (
	PlatformWeb Platform = "web"
	PlatformIM  Platform = "im"
)

// Action describes the intended state change. Fields are populated only when
// relevant for Type; empty fields mean "no change / not applicable".
type Action struct {
	Type        ActionType `json:"type"`
	WorkspaceID string     `json:"workspaceId,omitempty"`
	JobID       string     `json:"jobId,omitempty"`
}

// Message is what the user sees after the command runs.
type Message struct {
	Text    string      `json:"text"`
	Present PresentType `json:"present,omitempty"`
}

// Result is the structured output of a command.
type Result struct {
	Action  Action  `json:"action"`
	Message Message `json:"message"`
}

// Definition is per-command metadata. Duplicate-free — aliases share the
// same Definition.
type Definition struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
}

// ExecCtx carries everything a command needs to run, independent of platform.
// Fields that don't apply to the caller should be left zero (e.g. Web doesn't
// need IM fields, IM may not set JobID if no mapping).
type ExecCtx struct {
	Ctx              context.Context
	WorkspaceService workspace.Service
	JobService       job.Service

	// CurrentWorkspaceID / CurrentJobID are the current page/chat context at
	// the time the command was invoked. Empty means "not set".
	CurrentWorkspaceID string
	CurrentJobID       string

	// Platform optionally narrows output / behavior (IM keeps legacy hints
	// like "下一条消息将创建新的对话" and refuses cross-workspace /job use).
	// Empty defaults to PlatformWeb.
	Platform Platform
}

func (ec *ExecCtx) isIM() bool {
	return ec != nil && ec.Platform == PlatformIM
}

// Definitions returns the full list of built-in commands, sorted by primary
// name. Used by /help and by the Web-side completion overlay.
//
// The result is a shared, already-sorted slice — callers are read-only.
// IsKnown / ResolveName run on every chat-page message, so caching avoids
// rebuilding and re-sorting on each call.
func Definitions() []Definition {
	return definitionsList
}

var definitionsList = func() []Definition {
	list := []Definition{
		{Name: "/help", Description: "查看可用命令"},
		{Name: "/workspace", Aliases: []string{"/ws"}, Description: "查看/切换工作空间", Usage: "/workspace list | /workspace use <id|序号>"},
		{Name: "/job", Description: "查看/绑定 Job", Usage: "/job list | /job use <id|序号>"},
		{Name: "/new", Description: "在当前工作空间创建新对话"},
		{Name: "/status", Aliases: []string{"/info"}, Description: "查看当前聊天状态"},
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}()

// ResolveName normalizes a raw command name (including aliases) to a canonical
// definition name. Returns empty string if no match — callers can then treat
// the text as a regular message. Matching is case-insensitive.
func ResolveName(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	for _, d := range Definitions() {
		if d.Name == raw {
			return d.Name
		}
		for _, a := range d.Aliases {
			if a == raw {
				return d.Name
			}
		}
	}
	return ""
}

// Parse splits raw text into (command, args). Returns empty command if the
// text doesn't begin with '/'.
func Parse(text string) (cmd, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	head, rest := splitHead(text)
	cmd = strings.ToLower(head)
	args = rest
	return
}

// IsKnown reports whether text is a recognized slash command (matching a
// canonical name or alias). Unknown slash text (e.g. a typo like /hlep, or a
// path like /etc/hosts) returns false so callers treat it as a regular
// message. Mirrors the frontend's isKnownCommand in utils/commands.ts.
func IsKnown(text string) bool {
	cmd, _ := Parse(text)
	return ResolveName(cmd) != ""
}

// IsReadOnly reports whether a known command only reads state and can safely
// run while an Agent turn is active. Navigation and mutation commands are
// rejected by the Web adapter during a running turn rather than delayed.
func IsReadOnly(text string) bool {
	cmd, args := Parse(text)
	switch ResolveName(cmd) {
	case "/help", "/status":
		return true
	case "/workspace", "/job":
		sub, _ := splitSub(args)
		return sub == "" || sub == "list" || sub == "ls"
	default:
		return false
	}
}

// Execute dispatches a command by its canonical name. Returns (nil, false)
// when the name is not recognized — the caller can fall through to its
// regular-message path.
func Execute(cmd, args string, ec *ExecCtx) (*Result, bool) {
	name := ResolveName(cmd)
	if name == "" {
		return nil, false
	}
	switch name {
	case "/help":
		return cmdHelp(ec), true
	case "/workspace":
		return cmdWorkspace(args, ec), true
	case "/job":
		return cmdJob(args, ec), true
	case "/new":
		return cmdNew(ec), true
	case "/status":
		return cmdStatus(ec), true
	}
	return nil, false
}

// ---- Command implementations ----

func cmdHelp(ec *ExecCtx) *Result {
	// IM gets the legacy verbatim text — its users may have scripts /
	// muscle memory around the exact wording. Web gets the metadata-driven
	// format so new commands auto-appear in /help without another code edit.
	if ec.isIM() {
		return text("可用命令:\n" +
			"/workspace list - 查看工作空间列表\n" +
			"/workspace use <id|序号> - 切换当前聊天关联的工作空间\n" +
			"/ws <id|序号> - 快捷切换工作空间 (等价于 /workspace use ...)\n" +
			"/job list - 查看当前工作空间的 Job 列表\n" +
			"/job use <id|序号> - 绑定当前聊天到指定 Job\n" +
			"/job <id|序号> - 快捷绑定 Job (等价于 /job use ...)\n" +
			"/status, /info - 查看当前聊天状态\n" +
			"/new - 创建新的对话 (新 Job)\n" +
			"/help - 查看此帮助")
	}
	var sb strings.Builder
	sb.WriteString("可用命令:\n")
	for _, d := range Definitions() {
		if d.Usage != "" {
			fmt.Fprintf(&sb, "%s - %s\n  用法: %s\n", d.Name, d.Description, d.Usage)
		} else {
			fmt.Fprintf(&sb, "%s - %s\n", d.Name, d.Description)
		}
		for _, a := range d.Aliases {
			fmt.Fprintf(&sb, "  别名: %s\n", a)
		}
	}
	return &Result{Message: Message{Text: sb.String(), Present: PresentInline}}
}

func cmdWorkspace(args string, ec *ExecCtx) *Result {
	sub, rest := splitSub(args)

	// Shorthand: "/ws 2" / "/workspace 2" == "/workspace use 2". Any unknown
	// first token becomes the use argument. Use the untouched original text
	// so case-sensitive IDs survive (splitSub lowercases `sub` to recognize
	// subcommand keywords like "list"/"use", which would otherwise need two
	// variants).
	if rest == "" && sub != "" && sub != "list" && sub != "ls" && sub != "use" {
		rest = strings.TrimSpace(args)
		sub = "use"
	}

	switch sub {
	case "list", "ls", "":
		wsList := ec.WorkspaceService.List()
		if len(wsList) == 0 {
			if ec.isIM() {
				return text("暂无工作空间，请先在 Web 界面创建。")
			}
			return text("暂无工作空间，请先创建")
		}
		var sb strings.Builder
		sb.WriteString("工作空间列表:\n")
		// IM keeps the legacy plain "N. title (workdir)" rows — no current
		// marker, no footer. Web gets the "*" marker + legend so the
		// clickable list bubble can highlight the active workspace.
		isIM := ec.isIM()
		for i, ws := range wsList {
			marker := ""
			if !isIM && ws.ID == ec.CurrentWorkspaceID {
				marker = "*"
			}
			titleStr := ws.Title
			if !isIM {
				titleStr = titleOr(ws.Title, ws.ID)
			}
			fmt.Fprintf(&sb, "%s%d. %s (%s)\n", marker, i+1, titleStr, ws.Workdir)
		}
		if !isIM && ec.CurrentWorkspaceID != "" {
			sb.WriteString("\n* 表示当前工作空间")
		}
		return text(sb.String())

	case "use":
		arg := strings.TrimSpace(rest)
		if arg == "" {
			if ec.isIM() {
				return text("用法: /workspace use <workspace_id|序号>")
			}
			return text("用法: /workspace use <id|序号>")
		}
		wsID := arg
		if idx, err := strconv.Atoi(arg); err == nil {
			wsList := ec.WorkspaceService.List()
			if idx <= 0 || idx > len(wsList) {
				return text(fmt.Sprintf("工作空间序号超出范围: %d", idx))
			}
			wsID = wsList[idx-1].ID
		}
		ws, ok := ec.WorkspaceService.Get(wsID)
		if !ok {
			return text(fmt.Sprintf("工作空间不存在: %s", arg))
		}
		// IM keeps the original hint about the next message creating a new
		// job, since IM's mapping-based switch is truly deferred. Web
		// switches eagerly (reuses or creates a Job in the target ws at
		// command time), so the hint would be misleading.
		var msg string
		if ec.isIM() {
			msg = fmt.Sprintf("已切换到工作空间: %s，下一条消息将创建新的对话。", ws.Title)
		} else {
			msg = fmt.Sprintf("已切换到工作空间: %s", titleOr(ws.Title, ws.ID))
		}
		return &Result{
			Action:  Action{Type: ActionSwitchWorkspace, WorkspaceID: ws.ID},
			Message: Message{Text: msg, Present: PresentToast},
		}
	}
	return text("用法:\n/workspace list\n/workspace use <id|序号>")
}

func cmdJob(args string, ec *ExecCtx) *Result {
	sub, rest := splitSub(args)
	// Shorthand: "/job 1" / "/job <raw-id>". See cmdWorkspace for the
	// rationale on preserving original case via strings.TrimSpace(args).
	if rest == "" && sub != "" && sub != "list" && sub != "ls" && sub != "use" {
		rest = strings.TrimSpace(args)
		sub = "use"
	}

	if ec.CurrentWorkspaceID == "" {
		if ec.isIM() {
			return text("当前未选择工作空间，请先使用 /ws use <id|序号> 选择一个工作空间。")
		}
		return text("当前未选择工作空间，请先 /ws use <id|序号>")
	}
	if _, ok := ec.WorkspaceService.Get(ec.CurrentWorkspaceID); !ok {
		if ec.isIM() {
			return text("当前工作空间无效，请先使用 /ws use <id|序号> 重新选择一个工作空间。")
		}
		return text("当前工作空间无效，请先 /ws use 重新选择")
	}

	switch sub {
	case "list", "ls", "":
		// Web 默认隐藏定时任务生成的 Job（与首页一致）；IM 保持全量。
		excludeScheduled := !ec.isIM()
		sums, _, hasMore, _ := ec.JobService.ListByWorkspacePaged(ec.CurrentWorkspaceID, "", 10, excludeScheduled)
		if len(sums) == 0 {
			if ec.isIM() {
				return text("当前工作空间暂无 Job。发送消息或使用 /new 创建新的对话。")
			}
			return text("当前工作空间暂无 Job，发送消息或 /new 创建")
		}
		var sb strings.Builder
		sb.WriteString("Job 列表:\n")
		for i, s := range sums {
			marker := ""
			if ec.CurrentJobID != "" && ec.CurrentJobID == s.ID {
				marker = "*"
			}
			title := strings.TrimSpace(s.Title)
			if title == "" {
				title = consts.DefaultJobTitle
			}
			updated := ""
			if s.UpdatedAt > 0 {
				updated = time.UnixMilli(s.UpdatedAt).Format("2006-01-02 15:04")
			}
			if updated != "" {
				fmt.Fprintf(&sb, "%s%d. %s (%s) [%s] %s\n", marker, i+1, title, s.ID, s.Status, updated)
			} else {
				fmt.Fprintf(&sb, "%s%d. %s (%s) [%s]\n", marker, i+1, title, s.ID, s.Status)
			}
		}
		if ec.CurrentJobID != "" {
			if ec.isIM() {
				sb.WriteString("\n* 表示当前聊天已绑定的 Job")
			} else {
				sb.WriteString("\n* 表示当前 Job")
			}
		}
		// The "list" branch caps display at 10 for compactness, but "use"
		// accepts indices up to 50. Tell users about the wider range so
		// they know they can reach job #25 without having to paste its ID.
		if hasMore {
			if ec.isIM() {
				sb.WriteString("\n（仅显示最近 10 条。使用 /job use <序号>（1-50）或 /job use <id> 切换到更早的 Job）")
			} else {
				sb.WriteString("\n（仅显示最近 10 条。/job <序号>（1-50）或 /job <id> 可切换到更早的 Job）")
			}
		}
		return text(sb.String())

	case "use":
		arg := strings.TrimSpace(rest)
		if arg == "" {
			if ec.isIM() {
				return text("用法: /job use <job_id|序号>")
			}
			return text("用法: /job use <id|序号>")
		}
		jobID := arg
		// List size used for numeric index resolution. Keep at 50 so power
		// users with long backlogs can still reach older jobs by `/job 42`.
		// The "list" branch above caps display at 10 to keep the bubble
		// compact — that's display ergonomics, not the authoritative range.
		if idx, err := strconv.Atoi(arg); err == nil {
			// Keep the index resolution set consistent with the list output.
			excludeScheduled := !ec.isIM()
			sums, _, _, _ := ec.JobService.ListByWorkspacePaged(ec.CurrentWorkspaceID, "", 50, excludeScheduled)
			if idx <= 0 || idx > len(sums) {
				return text(fmt.Sprintf("Job 序号超出范围: %d", idx))
			}
			jobID = sums[idx-1].ID
		}
		j, ok := ec.JobService.Get(jobID)
		if !ok {
			return text(fmt.Sprintf("Job 不存在: %s", arg))
		}
		// IM binds via IMJobMapping, which holds exactly one workspace per
		// chat — cross-workspace binds would silently desync the mapping.
		// Keep the legacy "same workspace only" check for IM; Web routes
		// through a full workspace+job switch in its adapter and supports
		// cross-ws binding by design.
		if ec.isIM() && j.WorkspaceID != ec.CurrentWorkspaceID {
			return text(fmt.Sprintf("Job 不属于当前工作空间（当前: %s，Job: %s）。请先 /ws use 切换工作空间。", ec.CurrentWorkspaceID, j.WorkspaceID))
		}
		// Allow cross-workspace binding on Web: surface both fields so the
		// adapter can update the workspace context together with the job.
		msg := fmt.Sprintf("已绑定 Job: %s (%s)", titleOr(j.Title, consts.DefaultJobTitle), j.ID)
		if ec.isIM() {
			msg = fmt.Sprintf("已绑定当前聊天到 Job: %s (%s)。", titleOr(j.Title, consts.DefaultJobTitle), j.ID)
		}
		return &Result{
			Action:  Action{Type: ActionBindJob, WorkspaceID: j.WorkspaceID, JobID: j.ID},
			Message: Message{Text: msg, Present: PresentToast},
		}
	}
	return text("用法:\n/job list\n/job use <id|序号>")
}

func cmdNew(ec *ExecCtx) *Result {
	if ec.CurrentWorkspaceID == "" {
		if ec.isIM() {
			return text("当前未选择工作空间，请先使用 /ws use <id|序号> 选择一个工作空间。")
		}
		return text("当前未选择工作空间，请先 /ws use <id|序号>")
	}
	if _, ok := ec.WorkspaceService.Get(ec.CurrentWorkspaceID); !ok {
		if ec.isIM() {
			return text("当前工作空间无效，请先使用 /ws use <id|序号> 重新选择一个工作空间。")
		}
		return text("当前工作空间无效，请先 /ws use 重新选择")
	}
	// IM's legacy text had a period and promise that the next message will
	// kick off the new chat — keep it verbatim so IM-side scripts / muscle
	// memory stays untouched. Web actually creates the Job eagerly in its
	// adapter (see App.tsx new_job handler), so its toast reflects that.
	msg := "已创建新对话"
	if ec.isIM() {
		msg = "已准备好新的对话，发送消息开始。"
	}
	return &Result{
		Action:  Action{Type: ActionNewJob, WorkspaceID: ec.CurrentWorkspaceID},
		Message: Message{Text: msg, Present: PresentToast},
	}
}

func cmdStatus(ec *ExecCtx) *Result {
	var sb strings.Builder
	sb.WriteString("当前聊天状态:\n")
	if ec.CurrentWorkspaceID == "" {
		sb.WriteString("工作空间: 未配置\n")
	} else if ws, ok := ec.WorkspaceService.Get(ec.CurrentWorkspaceID); ok {
		if ec.isIM() {
			fmt.Fprintf(&sb, "工作空间: %s (%s)\n", ws.ID, ws.Title)
		} else {
			fmt.Fprintf(&sb, "工作空间: %s (%s)\n", ws.ID, titleOr(ws.Title, ws.ID))
		}
	} else {
		fmt.Fprintf(&sb, "工作空间: %s (不存在)\n", ec.CurrentWorkspaceID)
	}
	if ec.CurrentJobID == "" {
		if ec.isIM() {
			sb.WriteString("当前 Job: 未绑定，下一条消息会创建新的对话")
		} else {
			sb.WriteString("当前 Job: 未绑定")
		}
		return text(sb.String())
	}
	if j, ok := ec.JobService.Get(ec.CurrentJobID); ok {
		fmt.Fprintf(&sb, "当前 Job: %s\n", j.ID)
		fmt.Fprintf(&sb, "Job 状态: %s", j.Status)
	} else {
		if ec.isIM() {
			fmt.Fprintf(&sb, "当前 Job: %s (不存在或已结束缓存)", ec.CurrentJobID)
		} else {
			fmt.Fprintf(&sb, "当前 Job: %s (不存在)", ec.CurrentJobID)
		}
	}
	return text(sb.String())
}

// ---- helpers ----

func text(s string) *Result {
	return &Result{Message: Message{Text: s, Present: PresentInline}}
}

func splitSub(args string) (sub, rest string) {
	head, tail := splitHead(strings.TrimSpace(args))
	sub = strings.ToLower(head)
	rest = tail
	return
}

// splitHead splits s at the first Unicode whitespace rune, returning the head
// token and the whitespace-trimmed remainder. The web client's command
// detection splits on /\s+/ (see web/src/utils/commands.ts isKnownCommand), so
// the backend must treat tabs, NBSP, etc. as separators too — otherwise e.g.
// "/job\tlist" is a known command on the frontend (user bubble suppressed,
// command result awaited) while the backend forwards it to the Agent as a
// regular message.
func splitHead(s string) (head, tail string) {
	idx := strings.IndexFunc(s, unicode.IsSpace)
	if idx < 0 {
		return s, ""
	}
	return s[:idx], strings.TrimSpace(s[idx:])
}

func titleOr(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

// Ensure we reference model at least once so go mod doesn't complain about
// the import. (Used by adapters indirectly through job.Service types.)
var _ = (*model.Job)(nil)
