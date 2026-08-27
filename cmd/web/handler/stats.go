package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/usagestats"
)

// statsResponse is the JSON shape returned by GET /api/v1/stats/usage.
// Mirrors usagestats.UsageReport but enriches Workspace / Model rows and
// daily model buckets with human-readable labels while keeping model ids as
// stable aggregation keys so the UI can render them directly without merging
// same-display-name models.
type statsResponse struct {
	Range       statsRange                 `json:"range"`
	ByWorkspace []statsWorkspaceRow        `json:"byWorkspace"`
	ByModel     []statsModelRow            `json:"byModel"`
	ByTool      []usagestats.ToolAggregate `json:"byTool"`
	Daily       []statsDailyRow            `json:"daily"`
	// Previous holds the equal-length preceding period's KPI totals so the
	// frontend can render period-over-period deltas on the overview cards.
	// Only populated when the caller passes compare=1 and the range is not
	// All Time (which has no meaningful "previous period").
	Previous *statsKPITotals `json:"previous,omitempty"`
	Note     string          `json:"note"`
	Failed   bool            `json:"failed,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// statsKPITotals is the compact previous-period payload backing the overview
// cards' deltas. It carries only the five headline metrics, never the full
// breakdown sections, to keep the response small.
type statsKPITotals struct {
	TotalMs        int64 `json:"totalMs"`
	TurnCount      int   `json:"turnCount"`
	ToolCallCount  int   `json:"toolCallCount"`
	TokensTotal    int   `json:"tokensTotal"`
	WorkspaceCount int   `json:"workspaceCount"`
}

// statsRange echoes the inclusive date range that was actually used to
// compute the response (after applying defaults / clamping). The frontend
// uses this both to pre-fill the picker on first load and to draw an
// X-axis even when daily[] is empty.
type statsRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type statsWorkspaceRow struct {
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	Deleted       bool   `json:"deleted,omitempty"`
	usagestats.SectionTotals
}

// statsModelRow is the API-level row for a model bucket. ModelID is the
// raw stats key (or "" for the unattributed bucket); ModelName is the
// resolved display name, falling back to ModelID when the registry has
// no match — never empty so the UI doesn't have to second-guess.
type statsModelRow struct {
	ModelID   string `json:"modelId"`
	ModelName string `json:"modelName"`
	usagestats.SectionTotals
}

type statsDailyRow struct {
	Date string `json:"date"`
	usagestats.SectionTotals
	// Models is keyed by the raw model id (or unknownModelID). Display names are
	// provided separately in ModelNames so duplicate labels never collapse series.
	Models     map[string]usagestats.SectionTotals `json:"models,omitempty"`
	ModelNames map[string]string                   `json:"modelNames,omitempty"`
}

// defaultStatsLookbackDays is the window applied when callers omit `from`.
// Matches the spec ("缺 from 默认 30 天前") and the frontend's default
// "30 days" preset so direct API calls produce the same shape the UI
// shows by default.
const defaultStatsLookbackDays = 30

// StatsUsage returns the aggregated usage report for a date range.
//
// Query parameters (all optional):
//   - from: YYYY-MM-DD. Defaults to today minus defaultStatsLookbackDays.
//   - to:   YYYY-MM-DD. Defaults to today.
//
// Both bounds are inclusive. Pass `from=` (empty value) plus `all=1` to
// request the on-disk earliest month (All Time) — the picker uses this
// to short-circuit the 30-day default when the user picks "All".
func (h *Handler) StatsUsage(ctx context.Context, c *app.RequestContext) {
	from, fromErr := parseStatsDate(string(c.Query("from")))
	if fromErr != nil {
		httputil.BadRequest(c, "invalid from: "+fromErr.Error())
		return
	}
	to, toErr := parseStatsDate(string(c.Query("to")))
	if toErr != nil {
		httputil.BadRequest(c, "invalid to: "+toErr.Error())
		return
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		httputil.BadRequest(c, "from must be on or before to")
		return
	}

	allTime := strings.EqualFold(string(c.Query("all")), "1") || strings.EqualFold(string(c.Query("all")), "true")
	now := time.Now()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() && !allTime {
		from = to.AddDate(0, 0, -(defaultStatsLookbackDays - 1))
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		httputil.BadRequest(c, "from must be on or before to")
		return
	}

	if h.usageStats == nil {
		// Should not happen — the service is wired in NewHandler. Return
		// an empty report rather than 500 so the UI stays usable.
		c.JSON(http.StatusOK, statsResponse{
			Range:       statsRange{From: dateOrEmpty(from), To: dateOrEmpty(to)},
			ByWorkspace: []statsWorkspaceRow{},
			ByModel:     []statsModelRow{},
			ByTool:      []usagestats.ToolAggregate{},
			Daily:       []statsDailyRow{},
			Note:        noteText,
		})
		return
	}

	report, err := h.usageStats.GetUsage(from, to)
	resp := statsResponse{
		Range:  statsRange{From: report.From, To: report.To},
		ByTool: ensureToolRows(report.ByTool),
		Daily:  h.enrichDailyRows(ctx, report.Daily),
		Note:   noteText,
	}
	resp.ByWorkspace = h.enrichWorkspaceRows(report.ByWorkspace)
	resp.ByModel = h.enrichModelRows(ctx, report.ByModel)
	if err != nil {
		resp.Failed = true
		resp.Error = err.Error()
	}

	// Period-over-period comparison. Only meaningful for a bounded range:
	// "All Time" has no preceding window, so compare is skipped there.
	compare := strings.EqualFold(string(c.Query("compare")), "1") || strings.EqualFold(string(c.Query("compare")), "true")
	if compare && !resp.Failed && !allTime && !from.IsZero() && !to.IsZero() {
		// Length in days of the current inclusive window, then shift back by
		// that many days to land on the immediately-preceding equal window.
		days := int(to.Sub(from).Hours()/24) + 1
		prevTo := from.AddDate(0, 0, -1)
		prevFrom := prevTo.AddDate(0, 0, -(days - 1))
		if prevReport, prevErr := h.usageStats.GetUsage(prevFrom, prevTo); prevErr == nil {
			resp.Previous = kpiTotals(prevReport)
		} else {
			logger.Warnf(ctx, "[stats] compute previous period failed: %v", prevErr)
		}
	}

	c.JSON(http.StatusOK, resp)
}

// kpiTotals folds a usage report down to the five headline metrics shown on
// the overview cards. WorkspaceCount is the number of workspaces that had any
// activity in the window, including historical workspaces that no longer have
// metadata on this machine.
func kpiTotals(report usagestats.UsageReport) *statsKPITotals {
	out := &statsKPITotals{WorkspaceCount: len(report.ByWorkspace)}
	for _, ws := range report.ByWorkspace {
		out.TotalMs += ws.TotalMs
		out.TurnCount += ws.TurnCount
		out.ToolCallCount += ws.ToolCallCount
		out.TokensTotal += ws.Tokens.Total
	}
	return out
}

// enrichWorkspaceRows binds a name-aggregated historical row back to the
// current machine when possible. A source ID match is preferred; otherwise a
// current workspace with the same persisted name is the same logical workspace.
// Rows that cannot be bound remain visible but are marked deleted so the UI does
// not offer a broken jump target.
func (h *Handler) enrichWorkspaceRows(rows []usagestats.WorkspaceAggregate) []statsWorkspaceRow {
	if len(rows) == 0 {
		return []statsWorkspaceRow{}
	}
	out := make([]statsWorkspaceRow, 0, len(rows))
	for _, row := range rows {
		entry := statsWorkspaceRow{
			WorkspaceID:   row.WorkspaceID,
			WorkspaceName: row.WorkspaceName,
			SectionTotals: row.SectionTotals,
		}
		bound := false
		if h.workspaceService != nil {
			workspaceIDs := row.WorkspaceIDs
			if len(workspaceIDs) == 0 && row.WorkspaceID != "" {
				workspaceIDs = []string{row.WorkspaceID}
			}
			for _, workspaceID := range workspaceIDs {
				if ws, ok := h.workspaceService.Get(workspaceID); ok && ws != nil {
					entry.WorkspaceID = ws.ID
					if entry.WorkspaceName == "" {
						entry.WorkspaceName = ws.Title
					}
					bound = true
					break
				}
			}
			if !bound && entry.WorkspaceName != "" {
				for _, ws := range h.workspaceService.List() {
					if ws != nil && strings.TrimSpace(ws.Title) == entry.WorkspaceName {
						entry.WorkspaceID = ws.ID
						bound = true
						break
					}
				}
			}
		}
		if entry.WorkspaceName == "" {
			entry.WorkspaceName = row.WorkspaceID
		}
		entry.Deleted = h.workspaceService != nil && !bound
		out = append(out, entry)
	}
	return out
}

// enrichModelRows fills in the display name for each model id. Usage recording
// already canonicalizes provider-specific catalog IDs (notably Eino's random
// short IDs) to their actual model names. Other ACP model identifiers are
// already display-ready; the empty bucket (the "couldn't attribute" pile) is
// surfaced with the special id "(unknown model)" so the UI can render it as
// a labelled row rather than a silent gap.
func (h *Handler) enrichModelRows(ctx context.Context, rows []usagestats.ModelAggregate) []statsModelRow {
	if len(rows) == 0 {
		return []statsModelRow{}
	}
	out := make([]statsModelRow, 0, len(rows))
	for _, row := range rows {
		entry := statsModelRow{
			ModelID:       row.ModelID,
			SectionTotals: row.SectionTotals,
		}
		if entry.ModelID == "" {
			entry.ModelID = unknownModelID
		}
		entry.ModelName = h.canonicalUsageModelID(entry.ModelID)
		out = append(out, entry)
	}
	return out
}

func (h *Handler) enrichDailyRows(ctx context.Context, rows []usagestats.DailyAggregate) []statsDailyRow {
	if len(rows) == 0 {
		return []statsDailyRow{}
	}
	out := make([]statsDailyRow, 0, len(rows))
	nameCache := make(map[string]string)
	for _, row := range rows {
		entry := statsDailyRow{
			Date:          row.Date,
			SectionTotals: row.SectionTotals,
		}
		if len(row.Models) > 0 {
			entry.Models = make(map[string]usagestats.SectionTotals, len(row.Models))
			entry.ModelNames = make(map[string]string, len(row.Models))
			for rawID, totals := range row.Models {
				name, ok := nameCache[rawID]
				if !ok {
					name = h.canonicalUsageModelID(rawID)
					if name == "" {
						name = unknownModelID
					}
					nameCache[rawID] = name
				}
				modelID := rawID
				if modelID == "" {
					modelID = unknownModelID
				}
				cur := entry.Models[modelID]
				addStatsSection(&cur, &totals)
				entry.Models[modelID] = cur
				if name == "" {
					name = modelID
				}
				entry.ModelNames[modelID] = name
			}
		}
		out = append(out, entry)
	}
	return out
}

func ensureToolRows(rows []usagestats.ToolAggregate) []usagestats.ToolAggregate {
	if len(rows) == 0 {
		return []usagestats.ToolAggregate{}
	}
	return rows
}

func addStatsSection(dst, src *usagestats.SectionTotals) {
	dst.TotalMs += src.TotalMs
	dst.TurnCount += src.TurnCount
	dst.AssistantCount += src.AssistantCount
	dst.ThoughtCount += src.ThoughtCount
	dst.ToolCallCount += src.ToolCallCount
	dst.Tokens.Total += src.Tokens.Total
	dst.Tokens.Reported += src.Tokens.Reported
	dst.Tokens.Input += src.Tokens.Input
	dst.Tokens.Output += src.Tokens.Output
	dst.Tokens.CachedRead += src.Tokens.CachedRead
	dst.Tokens.CachedWrite += src.Tokens.CachedWrite
	dst.Tokens.Reasoning += src.Tokens.Reasoning
	dst.Tokens.ImageEstimate += src.Tokens.ImageEstimate
	dst.Tokens.Estimated += src.Tokens.Estimated
	dst.Tokens.ReportedTurns += src.Tokens.ReportedTurns
	dst.Tokens.EstimatedTurns += src.Tokens.EstimatedTurns
	dst.Tokens.Assistant += src.Tokens.Assistant
	dst.Tokens.Thought += src.Tokens.Thought
	dst.Tokens.ToolCall += src.Tokens.ToolCall
}

// parseStatsDate accepts an empty string (zero time) or YYYY-MM-DD; any
// other format is an error.
func parseStatsDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

func dateOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// noteText is the i18n key the frontend looks up for the "tokens are local
// estimates, not API billing" banner. Placed in the response so a single
// HTTP call yields everything the UI needs to render.
const noteText = "stats.tokensLocalEstimateNote"

// unknownModelID is the canonical display key for the "couldn't attribute
// model" bucket. Keep this aligned with the stats-page daily residual key.
const unknownModelID = "(unknown model)"
