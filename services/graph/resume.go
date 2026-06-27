package graph

import (
	"context"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Resume rebuild (step 15, §4 终态分类 + 可重置状态的传播范围).
//
// resumeBuilder takes the persisted instances/edges/variables of a resumable
// run and produces the post-reset state a fresh scheduler will load:
//
//   - 不可重置终态 (succeeded / skipped): kept verbatim — never re-run.
//   - 可重置终态 (failed / interrupted) and any persisted "running" (crash
//     semantics): deleted so the scheduler re-creates and re-runs them.
//
// Reset propagation is edge-driven and exploits a structural invariant: a
// succeeded/skipped instance's activating upstreams are themselves
// succeeded/skipped (a join only decides once its in-edges resolve, and an
// active in-edge means the upstream completed). So survivors form a closed set
// and the only rule needed is:
//
//	keep an edge iff its SOURCE survives (a start node has no instance and so
//	always "survives"); drop edges whose source was reset — the source re-emits
//	them when it re-runs, so inRemaining is never double-counted.
//
// Loops — STEP-LEVEL resume (§4 循环内精确续跑): an in-flight loop whose scope was
// snapshotted on the failing run (present in run.Resume.LoopState) is NOT reset
// wholesale. Its succeeded/skipped instances — completed rounds AND completed
// sibling steps of the in-flight round — are kept; only the resettable
// (failed/interrupted/running/pending) instances inside it are reset and re-run.
// seedResume rebuilds the loop's in-memory scope (at its persisted CurrentIteration)
// so the re-run steps execute in the correct round with the right QUARTET_LOOP_*
// context, and the round finishes via the normal quiescence path.
//
// FALLBACK — wholesale reset: if a resettable instance lives inside a loop whose
// scope was NOT snapshotted (e.g. a process-crash "recovering" run never runs
// snapshotLoopState), its scope cannot be rebuilt, so that top-level loop is
// reset wholesale (container + everything beneath it) and re-runs from round 0 —
// the original conservative behavior. Completed loops (succeeded container) are
// always kept entirely.
type resumeBuilder struct {
	cfg       model.GraphConfig
	instances map[string]model.GraphInstanceState
	edges     map[string]model.GraphEdgeState
	vars      map[string]map[string]string
	// loopState is the run's persisted in-flight loop snapshot (keyed by loop
	// container instance-key string). A container present here is rebuilt as a
	// live scope on resume rather than reset, enabling step-level continue.
	loopState map[string]model.GraphLoopState
	// archived collects instances removed by the reset that carried a session,
	// so the caller can preserve them on the run for the Chat session sidebar
	// (their conversation transcript still exists on disk). Keyed by instance-key
	// string. See model.GraphRun.ArchivedInstances.
	archived map[string]model.GraphInstanceState
}

func newResumeBuilder(cfg model.GraphConfig, instances map[string]model.GraphInstanceState, edges map[string]model.GraphEdgeState, vars map[string]map[string]string, loopState map[string]model.GraphLoopState) *resumeBuilder {
	return &resumeBuilder{cfg: cfg, instances: instances, edges: edges, vars: vars, loopState: loopState, archived: map[string]model.GraphInstanceState{}}
}

// isResettableStatus reports whether an instance state is a reset start (its
// output is unreliable or it was interrupted). Succeeded/skipped are kept
// verbatim; awaitingInput is also NOT resettable — a parked clarify instance is
// finalized in place by finalizeAwaitingClarify on continue (capture 结论 →
// succeeded → resolve out-edges), not reset and re-run.
func isResettableStatus(st model.GraphInstanceStatus) bool {
	switch st {
	case model.GraphInstanceStatusFailed, model.GraphInstanceStatusInterrupted,
		model.GraphInstanceStatusRunning, model.GraphInstanceStatusPending:
		return true
	default:
		return false
	}
}

// resetResettable computes the reset set, deletes those instances, their
// variable snapshots, and every edge whose source is in the reset set.
func (rb *resumeBuilder) resetResettable() {
	reset := rb.computeResetSet()
	for keyStr := range reset {
		// Preserve a removed instance's session for the Chat sidebar before
		// dropping it: its conversation transcript outlives the reset, and the
		// sidebar derives its list from live instances alone.
		if inst, ok := rb.instances[keyStr]; ok && (inst.SessionID != "" || inst.DisplaySessionID != "") {
			rb.archived[keyStr] = inst
		}
		delete(rb.instances, keyStr)
		delete(rb.vars, keyStr)
	}
	// Drop edges emitted by a reset source (it will re-emit on re-run). Keep
	// edges from survivors (incl. start nodes, which have no instance record).
	for edgeKey, es := range rb.edges {
		srcStr := instanceKeyString(es.SourceInstanceKey)
		if _, isReset := reset[srcStr]; isReset {
			delete(rb.edges, edgeKey)
		}
	}
}

// computeResetSet returns the instance-key strings to reset. Resettable
// instances in the main scope, or inside a loop whose scope was snapshotted
// (step-level resume), reset just themselves. A resettable instance inside a
// loop whose scope was NOT snapshotted forces a wholesale reset of that
// top-level loop (container + everything beneath it) — the conservative
// fallback that re-runs the loop from round 0.
func (rb *resumeBuilder) computeResetSet() map[string]struct{} {
	reset := map[string]struct{}{}
	// Top-level loop node IDs that must be reset wholesale: a resettable instance
	// lives under them but their scope is not rebuildable from loopState.
	wholesale := map[string]struct{}{}
	for keyStr, st := range rb.instances {
		if !isResettableStatus(st.Status) {
			continue
		}
		// A rebuildable in-flight loop container (its scope was snapshotted) is
		// kept, not reset: seedResume revives it to running and continues its
		// current round. Resetting it would lose its StartedAt and let the
		// frontier re-decide re-run the loop from round 0.
		if _, isRebuildableContainer := rb.loopState[keyStr]; isRebuildableContainer {
			continue
		}
		reset[keyStr] = struct{}{}
		if len(st.Key.Iterations) > 0 && !rb.loopRebuildable(st.Key) {
			wholesale[st.Key.Iterations[0].LoopNodeID] = struct{}{}
		}
	}
	if len(wholesale) == 0 {
		return reset
	}
	// Expand each wholesale loop: every instance under it (any round / nesting)
	// plus the container instance itself (which lives in the main scope).
	for keyStr, st := range rb.instances {
		if len(st.Key.Iterations) > 0 {
			if _, ok := wholesale[st.Key.Iterations[0].LoopNodeID]; ok {
				reset[keyStr] = struct{}{}
			}
		}
	}
	for loopNodeID := range wholesale {
		reset[instanceKeyString(model.GraphInstanceKey{NodeID: loopNodeID})] = struct{}{}
	}
	return reset
}

// loopRebuildable reports whether every loop scope enclosing the given instance
// key was snapshotted in run.Resume.LoopState, so seedResume can rebuild those
// scopes and continue them step-by-step. If any enclosing loop scope is missing
// its snapshot the instance is not step-level resumable and its top-level loop
// must be reset wholesale.
func (rb *resumeBuilder) loopRebuildable(key model.GraphInstanceKey) bool {
	for i := range key.Iterations {
		// The scope key for the i-th enclosing loop is the container node with the
		// iteration prefix up to and including itself.
		scopeKeyStr := instanceKeyString(model.GraphInstanceKey{
			NodeID:     key.Iterations[i].LoopNodeID,
			Iterations: key.Iterations[:i],
		})
		if _, ok := rb.loopState[scopeKeyStr]; !ok {
			return false
		}
	}
	return true
}

// scopeForKey resolves the scope an instance key belongs to during resume: the
// main scope for a main-graph key, or the rebuilt loop scope for the innermost
// enclosing loop. Returns nil when that loop scope was not rebuilt (a completed
// round, or an instance under a wholesale-reset loop) — the caller skips such
// targets: completed rounds are already resolved, and wholesale-reset internals
// re-run only via their freshly re-started container.
func (sc *scheduler) scopeForKey(key model.GraphInstanceKey) *scopeRun {
	if len(key.Iterations) == 0 {
		return sc.mainScope
	}
	last := key.Iterations[len(key.Iterations)-1]
	scopeKeyStr := instanceKeyString(model.GraphInstanceKey{
		NodeID:     last.LoopNodeID,
		Iterations: key.Iterations[:len(key.Iterations)-1],
	})
	return sc.activeLoops[scopeKeyStr]
}

// resolveRebuildParent returns the parent scope for a loop being rebuilt from a
// LoopState. Parents are rebuilt before children (depth-sorted), so a nested
// loop's parent is already registered in sc.activeLoops. A top-level loop's
// parent is the main scope. Returns nil if a nested parent was not rebuilt.
func (sc *scheduler) resolveRebuildParent(loopKey model.GraphInstanceKey) *scopeRun {
	p := loopKey.Iterations
	if len(p) == 0 {
		return sc.mainScope
	}
	parentLoopKey := model.GraphInstanceKey{NodeID: p[len(p)-1].LoopNodeID, Iterations: p[:len(p)-1]}
	if s, ok := sc.activeLoops[instanceKeyString(parentLoopKey)]; ok {
		return s
	}
	return nil
}

// rebuildLoopScopes re-creates the in-memory scope tree for every in-flight loop
// snapshotted on the failing run (run.Resume.LoopState), so a resume continues
// each loop from its current round (step-level). It mirrors startLoop's scope
// construction without re-running any round: the kept instances/edges already
// encode the completed work; only the reset steps re-run when seedResume
// re-decides the frontier. Returns the rebuilt scopes in parent-before-child
// order. Containers absent (wholesale-reset) or already terminal are skipped.
func (sc *scheduler) rebuildLoopScopes(ctx context.Context) []*scopeRun {
	if sc.run.Resume == nil || len(sc.run.Resume.LoopState) == 0 {
		return nil
	}
	keys := make([]model.GraphInstanceKey, 0, len(sc.run.Resume.LoopState))
	for _, ls := range sc.run.Resume.LoopState {
		keys = append(keys, ls.InstanceKey)
	}
	sortInstanceKeysByDepth(keys) // parents (shallower) before children
	var built []*scopeRun
	for _, loopKey := range keys {
		keyStr := instanceKeyString(loopKey)
		ls := sc.run.Resume.LoopState[keyStr]
		st, present := sc.instances[keyStr]
		if !present {
			// Container was wholesale-reset (e.g. a child loop lacked its own
			// snapshot): do not rebuild — it re-runs fresh via its container.
			continue
		}
		if st.Status == model.GraphInstanceStatusSucceeded || st.Status == model.GraphInstanceStatusSkipped {
			continue // stale snapshot for an already-completed loop
		}
		node, ok := sc.nodesByID[ls.LoopNodeID]
		if !ok {
			continue
		}
		parent := sc.resolveRebuildParent(loopKey)
		if parent == nil {
			logger.Warnf(ctx, "[graph] resume: loop parent scope missing, skipping rebuild: runId=%s loopKey=%s", sc.run.ID, keyStr)
			continue
		}
		// Consistency guard: the derived prefix must reproduce the persisted key.
		if instanceKeyString(scopeKey(parent.prefix, ls.LoopNodeID)) != keyStr {
			logger.Warnf(ctx, "[graph] resume: loop prefix mismatch, skipping rebuild: runId=%s loopKey=%s", sc.run.ID, keyStr)
			continue
		}
		entry := cloneStringMap(ls.Variables)
		loop := &scopeRun{
			container:         ls.LoopNodeID,
			parent:            parent,
			loopNode:          node,
			loopKey:           loopKey,
			maxIters:          effectiveLoopMaxIters(sc.cfg.RunConfig, node),
			iterIndex:         ls.CurrentIteration,
			roundsRun:         ls.CurrentIteration + 1,
			accumSnapshot:     cloneStringMap(entry),
			inflowSession:     ls.EntrySession,
			roundEntry:        cloneStringMap(entry),
			roundEntrySession: ls.EntrySession,
		}
		loop.prefix = append(append([]model.GraphLoopIteration{}, parent.prefix...),
			model.GraphLoopIteration{LoopNodeID: ls.LoopNodeID, Index: ls.CurrentIteration})
		sc.activeLoops[keyStr] = loop
		parent.live++ // the loop is one async unit in its parent (mirror startLoop)
		// Revive the container instance to running: it was marked interrupted/failed
		// at the stop, but on resume the loop is live again.
		st.Status = model.GraphInstanceStatusRunning
		st.FinishedAt = 0
		st.Error = nil
		st.BlockedReason = ""
		sc.instances[keyStr] = st
		built = append(built, loop)
		logger.Infof(ctx, "[graph] resume: rebuilt loop scope: runId=%s loopKey=%s iteration=%d entryVars=%d",
			sc.run.ID, keyStr, ls.CurrentIteration, len(entry))
	}
	return built
}

// resumeSourceContrib resolves the variable snapshot and session an edge's
// source contributes when rebuilding contribs on resume. A main-graph start node
// contributes the run's initial variables; a loop-entry start node of an
// in-flight round contributes that round's entry snapshot/session (neither has a
// persisted instance). Any other source contributes its persisted varsByKey
// snapshot and instance session.
func (sc *scheduler) resumeSourceContrib(srcKey model.GraphInstanceKey) (map[string]string, string) {
	srcKeyStr := instanceKeyString(srcKey)
	if srcNode, ok := sc.nodesByID[srcKey.NodeID]; ok && srcNode.Type == model.GraphNodeTypeStart {
		if srcNode.ParentID == "" {
			// Main-graph start: never persisted as an instance; its contribution is
			// the run's initial variable snapshot (mirrors seedFresh). Without this a
			// loop fed directly by the start node loses every initial variable, so a
			// condition referencing one would silently see an empty string and route
			// to the wrong branch.
			return cloneStringMap(sc.cfg.Variables), ""
		}
		if vars, sess, ok := sc.loopEntrySnapshot(srcKey, srcNode); ok {
			return vars, sess
		}
	}
	srcVars := sc.varsByKey[srcKeyStr]
	srcSession := ""
	if srcState, ok := sc.instances[srcKeyStr]; ok {
		srcSession = srcState.SessionID
	}
	return srcVars, srcSession
}

// loopEntrySnapshot returns the round-entry snapshot/session for an edge whose
// source is the loop-scoped start node of an in-flight round (the round being
// resumed). It mirrors what startIteration seeds for a fresh round, sourced from
// the persisted LoopState. Returns ok=false for any other start (e.g. a
// completed round's entry, whose downstream is already succeeded and never
// re-read).
func (sc *scheduler) loopEntrySnapshot(srcKey model.GraphInstanceKey, srcNode model.GraphNode) (map[string]string, string, bool) {
	if sc.run.Resume == nil || len(srcKey.Iterations) == 0 {
		return nil, "", false
	}
	if sc.loopEntry[srcNode.ParentID] != srcNode.ID {
		return nil, "", false
	}
	last := srcKey.Iterations[len(srcKey.Iterations)-1]
	if last.LoopNodeID != srcNode.ParentID {
		return nil, "", false
	}
	scopeKeyStr := instanceKeyString(model.GraphInstanceKey{
		NodeID:     srcNode.ParentID,
		Iterations: srcKey.Iterations[:len(srcKey.Iterations)-1],
	})
	ls, ok := sc.run.Resume.LoopState[scopeKeyStr]
	if !ok || ls.CurrentIteration != last.Index {
		return nil, "", false
	}
	return cloneStringMap(ls.Variables), ls.EntrySession, true
}

// seedResume re-derives the scheduler's in-memory state from the post-reset
// persisted maps and re-decides the ready frontier, re-entering normal
// scheduling. Mirrors seedFresh's role but for a continued run.
func (sc *scheduler) seedResume(ctx context.Context) error {
	instances, err := sc.svc.runRepo.GetInstances(ctx, sc.run.ID)
	if err != nil {
		return err
	}
	edges, err := sc.svc.runRepo.GetEdges(ctx, sc.run.ID)
	if err != nil {
		return err
	}
	vars, err := sc.svc.runRepo.GetVariables(ctx, sc.run.ID)
	if err != nil {
		return err
	}
	sc.instances = instances
	sc.edges = edges
	sc.varsByKey = vars

	// Finalize any parked clarify instances BEFORE the edge-driven rebuild
	// (§5 讨论完成/续跑): capture each one's discussion 结论, flip it to succeeded,
	// and resolve its held out-edges as active — so the rebuild below counts those
	// fresh edges and the frontier decide drives the clarify's downstream. A
	// no-op when no instance is awaitingInput (a plain failure resume).
	sc.finalizeAwaitingClarify(ctx)

	// Rebuild the in-memory loop scope tree from the persisted in-flight loop
	// snapshot BEFORE the frontier re-decide, so re-run steps inside a loop
	// resolve to their (rebuilt) scope and execute in the correct round with the
	// right QUARTET_LOOP_* context. Completed/wholesale-reset loops are not here.
	rebuilt := sc.rebuildLoopScopes(ctx)

	// Rebuild inRemaining / anyActive / contribs from the kept (already-resolved)
	// edges. Each kept edge is permanently resolved (its survivor source never
	// re-emits it); reset sources' edges were dropped and will be re-emitted on
	// re-run, so they are not pre-counted here — no double counting.
	for _, es := range sc.edges {
		targetKeyStr := instanceKeyString(es.TargetInstanceKey)
		targetNodeID := es.TargetInstanceKey.NodeID
		if _, ok := sc.inRemaining[targetKeyStr]; !ok {
			sc.inRemaining[targetKeyStr] = sc.inDegree[targetNodeID]
		}
		sc.inRemaining[targetKeyStr]--
		if es.Status == model.GraphEdgeStatusActive {
			sc.anyActive[targetKeyStr] = true
			srcVars, srcSession := sc.resumeSourceContrib(es.SourceInstanceKey)
			sc.contribs[targetKeyStr] = append(sc.contribs[targetKeyStr], UpstreamSnapshot{
				NodeID:           es.SourceInstanceKey.NodeID,
				Variables:        srcVars,
				LastAssistantMsg: srcVars[lastAssistantKey],
				SessionID:        srcSession,
			})
		}
	}

	// endReached if any main-graph end already had a resolved-active in-edge that
	// survived (its path completed before the stop).
	for keyStr, rem := range sc.inRemaining {
		if rem > 0 {
			continue
		}
		node, ok := sc.nodesByID[nodeIDFromKeyString(keyStr, sc.instances)]
		if !ok {
			continue
		}
		if node.Type == model.GraphNodeTypeEnd && node.ParentID == "" && sc.anyActive[keyStr] {
			sc.endReached = true
		}
	}

	// Re-decide the frontier: every target whose in-edges are all resolved and
	// that is not a surviving reliable terminal. decide()'s idempotence guard
	// skips succeeded/skipped instances. A target in the main scope uses the main
	// scope; a target inside an in-flight loop round uses its rebuilt scope; a
	// target in a completed round or a non-rebuilt loop (scopeForKey==nil) is
	// skipped — it is already resolved or only re-runs via its container.
	type frontierItem struct {
		node          model.GraphNode
		key           model.GraphInstanceKey
		scope         *scopeRun
		visible       map[string]string
		inflowSession string
	}
	fullKey := sc.resumeKeyResolver()
	var frontier []frontierItem
	for targetKeyStr, rem := range sc.inRemaining {
		if rem > 0 {
			continue
		}
		key := fullKey(targetKeyStr)
		node, ok := sc.nodesByID[key.NodeID]
		if !ok {
			continue
		}
		// A rebuilt loop container is driven by its own scope, not re-decided as a
		// plain node (that would re-run it via startLoop from round 0). This holds
		// for both top-level and nested containers — both live in sc.activeLoops
		// keyed by their full instance key.
		if _, isRebuiltContainer := sc.activeLoops[targetKeyStr]; isRebuiltContainer {
			continue
		}
		scope := sc.scopeForKey(key)
		if scope == nil {
			continue // completed round / wholesale-reset internal — nothing to do
		}
		frontier = append(frontier, frontierItem{
			node:          node,
			key:           key,
			scope:         scope,
			visible:       MergeVisibleSnapshots(sc.contribs[targetKeyStr]),
			inflowSession: pickInflowSession(sc.contribs[targetKeyStr]),
		})
	}
	for _, fi := range frontier {
		sc.decide(ctx, fi.scope, fi.node, fi.key, fi.visible, fi.inflowSession)
		if sc.failed {
			return nil
		}
	}
	// A rebuilt loop round may consist solely of synchronous work already resolved
	// above, or its body steps were just enqueued; either way, nudge each rebuilt
	// scope so a round that is already quiescent advances (mirrors startIteration's
	// trailing onScopeQuiesced). Live (enqueued) rounds are driven by handleResult.
	for _, loop := range rebuilt {
		sc.onScopeQuiesced(ctx, loop)
		if sc.failed {
			return nil
		}
	}
	return nil
}

// finalizeAwaitingClarify converts every parked clarify instance (status
// awaitingInput) into a succeeded one and resolves its held out-edges, run once
// at the start of seedResume so the continue (§5 讨论完成/续跑) reuses the normal
// edge-driven rebuild rather than a parallel code path. For each such instance
// it:
//
//   - reads the discussion 结论 (the session's last assistant message via the
//     runner's optional SessionLastAssistantReader); a session with no assistant
//     turn yet yields an empty 结论 (best-effort, not a failure);
//   - writes the 结论 into _last_assistant_msg, the optional alias, and every
//     declared output variable — mirroring handleResult's success bookkeeping so
//     downstream {{...}} references resolve identically to a Prompt node;
//   - flips the instance to succeeded and records its visible/output snapshot in
//     varsByKey (the rebuild reads varsByKey for the source's contribution);
//   - writes an ACTIVE edge state for each of the node's out-edges, keyed exactly
//     like resolveEdge (edgeStateKey(edgeID, targetKey)), so the seedResume edge
//     loop counts them down and decides the downstream frontier.
//
// Clarify nodes are restricted to the main scope (validated), so their instance
// keys have no iteration prefix and out-edges resolve in the main scope.
func (sc *scheduler) finalizeAwaitingClarify(ctx context.Context) {
	for keyStr, st := range sc.instances {
		if st.Status != model.GraphInstanceStatusAwaitingInput {
			continue
		}
		node, ok := sc.nodesByID[st.NodeID]
		if !ok {
			// Node removed by a static version edit while parked: drop the hold so
			// the run can still finish. Its downstream is unreachable from here.
			logger.Warnf(ctx, "[graph] continue: clarify node missing from config, skipping: runId=%s key=%s nodeId=%s", sc.run.ID, keyStr, st.NodeID)
			continue
		}

		conclusion := sc.readClarifyConclusion(ctx, node, st)

		visible := cloneStringMap(st.VisibleVariables)
		if visible == nil {
			visible = map[string]string{}
		}
		produced := cloneStringMap(st.OutputVariables)
		if produced == nil {
			produced = map[string]string{}
		}
		// The discussion 结论 is the authoritative output of a clarify node: it
		// populates the reserved last-assistant variable, the optional alias, and
		// every declared output variable (so a downstream node can reference any of
		// them). This overwrites any best-effort draft markers captured at open time.
		visible[lastAssistantKey] = conclusion
		if alias := node.Config.LastAssistantAlias; alias != "" {
			visible[alias] = conclusion
			produced[alias] = conclusion
		}
		for _, name := range node.Config.OutputVariables {
			visible[name] = conclusion
			produced[name] = conclusion
		}

		now := time.Now().UnixMilli()
		st.Status = model.GraphInstanceStatusSucceeded
		st.BlockedReason = ""
		st.VisibleVariables = visible
		st.OutputVariables = produced
		if st.FinishedAt == 0 {
			st.FinishedAt = now
		}
		sc.instances[keyStr] = st
		sc.varsByKey[keyStr] = cloneStringMap(visible)

		// Resolve the held out-edges as active, keyed identically to resolveEdge so
		// the seedResume edge-rebuild loop picks them up. Clarify lives in the main
		// scope (no iteration prefix), so source/target keys are plain node IDs.
		srcKey := model.GraphInstanceKey{NodeID: node.ID}
		for _, e := range sc.outEdges[node.ID] {
			targetKey := model.GraphInstanceKey{NodeID: e.TargetNodeID}
			sc.edges[edgeStateKey(e.ID, targetKey)] = model.GraphEdgeState{
				EdgeID:            e.ID,
				SourceInstanceKey: srcKey,
				TargetInstanceKey: targetKey,
				Status:            model.GraphEdgeStatusActive,
				ResolvedAt:        now,
			}
		}
		sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceCompleted, st.Key, node.ID, "clarify finalized after discussion", nil)
		logger.Infof(ctx, "[graph] clarify finalized: runId=%s nodeId=%s key=%s sessionId=%s conclusionLen=%d outEdges=%d",
			sc.run.ID, node.ID, keyStr, st.SessionID, len(conclusion), len(sc.outEdges[node.ID]))
	}
}

// readClarifyConclusion resolves a clarify instance's discussion 结论 — the last
// assistant message of its session — through the runner's optional
// SessionLastAssistantReader. Best-effort: a missing capability, a read error,
// or a session with no assistant turn all degrade to an empty 结论 (logged), so a
// continue never fails on conclusion capture. The display session is preferred
// (it is the conversation the UI lists for the node), falling back to the
// lineage session.
func (sc *scheduler) readClarifyConclusion(ctx context.Context, node model.GraphNode, st model.GraphInstanceState) string {
	sessionID := firstNonEmpty(st.DisplaySessionID, st.SessionID)
	if sessionID == "" {
		return ""
	}
	reader, ok := sc.runner.(SessionLastAssistantReader)
	if !ok {
		logger.Warnf(ctx, "[graph] continue: runner has no SessionLastAssistantReader, clarify conclusion empty: runId=%s nodeId=%s", sc.run.ID, node.ID)
		return ""
	}
	content, found, err := reader.SessionLastAssistantMessage(ctx, sc.run.JobID, sessionID)
	if err != nil {
		logger.Warnf(ctx, "[graph] continue: read clarify conclusion failed: runId=%s nodeId=%s sessionId=%s err=%v", sc.run.ID, node.ID, sessionID, err)
		return ""
	}
	if !found {
		logger.Infof(ctx, "[graph] continue: clarify session has no assistant message, conclusion empty: runId=%s nodeId=%s sessionId=%s", sc.run.ID, node.ID, sessionID)
		return ""
	}
	return content
}

// resumeKeyResolver returns a function mapping an instance-key string back to its
// full GraphInstanceKey (with iteration prefix). Main-scope keys are the node ID
// itself; loop-scoped keys are recovered from the persisted instances and edges,
// which carry full keys.
func (sc *scheduler) resumeKeyResolver() func(string) model.GraphInstanceKey {
	byStr := map[string]model.GraphInstanceKey{}
	for keyStr, st := range sc.instances {
		byStr[keyStr] = st.Key
	}
	for _, es := range sc.edges {
		if _, ok := byStr[instanceKeyString(es.TargetInstanceKey)]; !ok {
			byStr[instanceKeyString(es.TargetInstanceKey)] = es.TargetInstanceKey
		}
		if _, ok := byStr[instanceKeyString(es.SourceInstanceKey)]; !ok {
			byStr[instanceKeyString(es.SourceInstanceKey)] = es.SourceInstanceKey
		}
	}
	return func(keyStr string) model.GraphInstanceKey {
		if k, ok := byStr[keyStr]; ok {
			return k
		}
		return model.GraphInstanceKey{NodeID: keyStr}
	}
}

// nodeIDFromKeyString resolves a node ID from an instance-key string. For a
// main-scope key the string IS the node ID; otherwise look it up from the
// persisted instance's Key.
func nodeIDFromKeyString(keyStr string, instances map[string]model.GraphInstanceState) string {
	if st, ok := instances[keyStr]; ok {
		return st.NodeID
	}
	return keyStr
}
