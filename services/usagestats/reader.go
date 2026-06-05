package usagestats

import (
	"fmt"
	"sort"
	"time"
)

// GetDailyTotals returns the totals (totalMs, turnCount) for the given
// workspace on each requested day. Days with no data are omitted.
//
// An empty workspaceID aggregates across ALL workspaces present on disk
// for the requested days. The Job List header uses this branch when the
// "all workspaces" filter is active so the per-day pill still reflects
// reality instead of disappearing.
func (s *service) GetDailyTotals(workspaceID string, days []time.Time) (map[string]DailyTotals, error) {
	if len(days) == 0 {
		return nil, nil
	}

	// Group requested days by month so we load each month at most once.
	byMonth := map[string][]string{}
	for _, d := range days {
		mk := monthKey(d)
		byMonth[mk] = append(byMonth[mk], dayKey(d))
	}

	out := make(map[string]DailyTotals, len(days))
	var firstErr error
	for mk, dks := range byMonth {
		mf, err := s.store.loadMonthSnapshot(s.rootCtx, mk)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("load usage stats month %s: %w", mk, err)
		}
		if workspaceID != "" {
			wsDays, ok := mf.Workspaces[workspaceID]
			if !ok {
				continue
			}
			for _, dk := range dks {
				if day, ok := wsDays[dk]; ok && day != nil {
					out[dk] = DailyTotals{
						TotalMs:   day.TotalMs,
						TurnCount: day.TurnCount,
					}
				}
			}
			continue
		}
		// Cross-workspace aggregation.
		for _, dk := range dks {
			var agg DailyTotals
			seen := false
			for _, wsDays := range mf.Workspaces {
				day, ok := wsDays[dk]
				if !ok || day == nil {
					continue
				}
				agg.TotalMs += day.TotalMs
				agg.TurnCount += day.TurnCount
				seen = true
			}
			if seen {
				out[dk] = agg
			}
		}
	}
	return out, firstErr
}

// GetUsage produces the full aggregate report across the inclusive [from, to]
// range. Either bound may be zero to mean "use the earliest/latest existing
// data" — the implementation discovers months on disk for the All Time path.
func (s *service) GetUsage(from, to time.Time) (UsageReport, error) {
	return s.getUsage(from, to, nil)
}

// GetUsageForWorkspaces is the filtered variant used by the stats page when
// workspace metadata is available. It keeps all report sections consistent by
// filtering before accumulation, so deleted workspaces do not leak into model,
// tool, or trend totals.
func (s *service) GetUsageForWorkspaces(from, to time.Time, workspaceIDs []string) (UsageReport, error) {
	allowed := make(map[string]struct{}, len(workspaceIDs))
	for _, id := range workspaceIDs {
		if id == "" {
			continue
		}
		allowed[id] = struct{}{}
	}
	return s.getUsage(from, to, allowed)
}

func (s *service) getUsage(from, to time.Time, allowedWorkspaces map[string]struct{}) (UsageReport, error) {
	from, to, rangeErr := s.normalizeRange(from, to)

	report := UsageReport{
		From: dayKeyOrEmpty(from),
		To:   dayKeyOrEmpty(to),
	}
	if rangeErr != nil {
		return report, rangeErr
	}
	if from.IsZero() || to.IsZero() {
		return report, nil
	}

	keys := listMonthKeysInRange(from, to)

	// Per-(ws/model/tool) accumulators.
	byWS := map[string]*SectionTotals{}
	byModel := map[string]*SectionTotals{}
	byTool := map[string]*ToolAggregate{}
	dailyMap := map[string]*DailyAggregate{}

	var firstErr error
	for _, mk := range keys {
		mf, err := s.store.loadMonthSnapshot(s.rootCtx, mk)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("load usage stats month %s: %w", mk, err)
		}
		for wsID, wsDays := range mf.Workspaces {
			if allowedWorkspaces != nil {
				if _, ok := allowedWorkspaces[wsID]; !ok {
					continue
				}
			}
			for dk, day := range wsDays {
				if day == nil {
					continue
				}
				if !inRange(dk, from, to) {
					continue
				}
				accumulateWorkspace(byWS, wsID, &day.SectionTotals)
				accumulateModelsFromDay(byModel, day)
				accumulateToolsFromDay(byTool, day)
				accumulateDaily(dailyMap, dk, day)
			}
		}
	}

	report.ByWorkspace = sortedWorkspaceAggregates(byWS)
	report.ByModel = sortedModelAggregates(byModel)
	report.ByTool = sortedToolAggregates(byTool)
	report.Daily = sortedDailyAggregates(dailyMap)
	return report, firstErr
}

// normalizeRange resolves zero bounds. Today's date is the
// upper bound when `to` is zero; the earliest existing-on-disk month is
// the lower bound when `from` is zero. Inverted ranges (from > to) are
// rejected as caller input errors.
func (s *service) normalizeRange(from, to time.Time) (time.Time, time.Time, error) {
	now := s.nowFn()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		// All Time: pick the earliest month that exists either on disk or in
		// the in-process cache. Record() applies snapshots to memory before the
		// debounced flush, so using disk alone can hide freshly recorded data.
		keys, err := s.listKnownMonthKeys()
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if len(keys) == 0 {
			from = now
		} else {
			t, err := time.ParseInLocation("2006-01", keys[0], now.Location())
			if err == nil {
				from = t
			} else {
				from = now
			}
		}
	}

	// Truncate to date precision in the local zone.
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, now.Location())
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, now.Location())
	if from.After(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be on or before to")
	}
	return from, to, nil
}

func (s *service) listKnownMonthKeys() ([]string, error) {
	diskKeys, err := listExistingMonthKeys()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(diskKeys))
	for _, key := range diskKeys {
		seen[key] = struct{}{}
	}
	s.store.mu.Lock()
	for key, mf := range s.store.months {
		if _, dirty := s.store.dirty[key]; dirty || monthFileHasData(mf) {
			seen[key] = struct{}{}
		}
	}
	s.store.mu.Unlock()
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func monthFileHasData(mf *MonthFile) bool {
	if mf == nil {
		return false
	}
	for _, wsDays := range mf.Workspaces {
		for _, day := range wsDays {
			if day != nil && hasSectionValue(&day.SectionTotals) {
				return true
			}
		}
	}
	return false
}

func dayKeyOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return dayKey(t)
}

// inRange returns true when day-key dk is within [from, to] inclusive.
func inRange(dk string, from, to time.Time) bool {
	d, err := time.ParseInLocation("2006-01-02", dk, from.Location())
	if err != nil {
		return false
	}
	if d.Before(from) || d.After(to) {
		return false
	}
	return true
}

func accumulateWorkspace(dst map[string]*SectionTotals, wsID string, src *SectionTotals) {
	t, ok := dst[wsID]
	if !ok {
		t = &SectionTotals{}
		dst[wsID] = t
	}
	addSection(t, src)
}

func accumulateModelsFromDay(dst map[string]*SectionTotals, day *DayBucket) {
	var modelTotal SectionTotals
	for mid, mb := range day.Models {
		if mb == nil {
			continue
		}
		addSection(&modelTotal, &mb.SectionTotals)
		t, ok := dst[mid]
		if !ok {
			t = &SectionTotals{}
			dst[mid] = t
		}
		addSection(t, &mb.SectionTotals)
	}
	residual := residualSection(&day.SectionTotals, &modelTotal)
	if hasSectionValue(&residual) {
		t, ok := dst[""]
		if !ok {
			t = &SectionTotals{}
			dst[""] = t
		}
		addSection(t, &residual)
	}
}

func accumulateToolsFromDay(dst map[string]*ToolAggregate, day *DayBucket) {
	for k, b := range day.Tools {
		if b == nil {
			continue
		}
		bucketKey := canonicalToolBucketKey(k)
		t, ok := dst[bucketKey]
		if !ok {
			t = &ToolAggregate{ToolKey: bucketKey}
			dst[bucketKey] = t
		}
		t.Count += b.Count
		t.TotalMs += b.TotalMs
	}
}

func accumulateDaily(dst map[string]*DailyAggregate, dk string, day *DayBucket) {
	d, ok := dst[dk]
	if !ok {
		d = &DailyAggregate{Date: dk}
		dst[dk] = d
	}
	addSection(&d.SectionTotals, &day.SectionTotals)
	var modelTotal SectionTotals
	if len(day.Models) > 0 {
		if d.Models == nil {
			d.Models = make(map[string]SectionTotals, len(day.Models))
		}
		for mid, mb := range day.Models {
			if mb == nil {
				continue
			}
			addSection(&modelTotal, &mb.SectionTotals)
			cur := d.Models[mid]
			addSection(&cur, &mb.SectionTotals)
			d.Models[mid] = cur
		}
	}
	residual := residualSection(&day.SectionTotals, &modelTotal)
	if hasSectionValue(&residual) {
		if d.Models == nil {
			d.Models = make(map[string]SectionTotals, 1)
		}
		cur := d.Models[""]
		addSection(&cur, &residual)
		d.Models[""] = cur
	}
}

func residualSection(total, known *SectionTotals) SectionTotals {
	if total == nil {
		return SectionTotals{}
	}
	if known == nil {
		known = &SectionTotals{}
	}
	return SectionTotals{
		TotalMs:        nonNegativeInt64(total.TotalMs - known.TotalMs),
		TurnCount:      nonNegativeInt(total.TurnCount - known.TurnCount),
		AssistantCount: nonNegativeInt(total.AssistantCount - known.AssistantCount),
		ThoughtCount:   nonNegativeInt(total.ThoughtCount - known.ThoughtCount),
		ToolCallCount:  nonNegativeInt(total.ToolCallCount - known.ToolCallCount),
		Tokens: TokenTotals{
			Total:     nonNegativeInt(total.Tokens.Total - known.Tokens.Total),
			Assistant: nonNegativeInt(total.Tokens.Assistant - known.Tokens.Assistant),
			Thought:   nonNegativeInt(total.Tokens.Thought - known.Tokens.Thought),
			ToolCall:  nonNegativeInt(total.Tokens.ToolCall - known.Tokens.ToolCall),
		},
	}
}

func nonNegativeInt(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func nonNegativeInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func addSection(dst, src *SectionTotals) {
	dst.TotalMs += src.TotalMs
	dst.TurnCount += src.TurnCount
	dst.AssistantCount += src.AssistantCount
	dst.ThoughtCount += src.ThoughtCount
	dst.ToolCallCount += src.ToolCallCount
	dst.Tokens.Total += src.Tokens.Total
	dst.Tokens.Assistant += src.Tokens.Assistant
	dst.Tokens.Thought += src.Tokens.Thought
	dst.Tokens.ToolCall += src.Tokens.ToolCall
}

func hasSectionValue(s *SectionTotals) bool {
	if s == nil {
		return false
	}
	return s.TotalMs > 0 || s.TurnCount > 0 || s.AssistantCount > 0 || s.ThoughtCount > 0 || s.ToolCallCount > 0 ||
		s.Tokens.Total > 0 || s.Tokens.Assistant > 0 || s.Tokens.Thought > 0 || s.Tokens.ToolCall > 0
}

func sortedWorkspaceAggregates(in map[string]*SectionTotals) []WorkspaceAggregate {
	out := make([]WorkspaceAggregate, 0, len(in))
	for k, v := range in {
		out = append(out, WorkspaceAggregate{WorkspaceID: k, SectionTotals: *v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMs != out[j].TotalMs {
			return out[i].TotalMs > out[j].TotalMs
		}
		return out[i].WorkspaceID < out[j].WorkspaceID
	})
	return out
}

func sortedModelAggregates(in map[string]*SectionTotals) []ModelAggregate {
	out := make([]ModelAggregate, 0, len(in))
	for k, v := range in {
		out = append(out, ModelAggregate{ModelID: k, SectionTotals: *v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMs != out[j].TotalMs {
			return out[i].TotalMs > out[j].TotalMs
		}
		return out[i].ModelID < out[j].ModelID
	})
	return out
}

func sortedToolAggregates(in map[string]*ToolAggregate) []ToolAggregate {
	out := make([]ToolAggregate, 0, len(in))
	for _, v := range in {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMs != out[j].TotalMs {
			return out[i].TotalMs > out[j].TotalMs
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ToolKey < out[j].ToolKey
	})
	return out
}

func sortedDailyAggregates(in map[string]*DailyAggregate) []DailyAggregate {
	out := make([]DailyAggregate, 0, len(in))
	for _, v := range in {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}
