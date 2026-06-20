package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Loop subgraph driver (step 13, §2 循环子图驱动 + §3 循环变量语义).
//
// The scheduler generalises a single DAG into a tree of "scope instances": the
// main graph is one scope (empty iteration prefix), and every activation of a
// loop container spawns a child scope per round. A scope's `live` count tracks
// its in-flight ASYNC units — business worker instances and active child loop
// containers — but NOT synchronous work (If-Else routing, end resolution,
// pruning) which the scheduler goroutine completes inline. When `live` hits
// zero a scope has quiesced:
//
//   - main scope: handled by the worker-pool termination check (no-op here).
//   - loop scope: one round finished → evaluate the loop's termination rule and
//     either start the next round or finish the loop.
//
// All of this runs in the single scheduler goroutine, preserving the
// single-writer invariant (no locks).

// scopeRun is one execution scope: the main graph or one running loop container.
type scopeRun struct {
	container string                     // loop node ID ("" = main graph)
	prefix    []model.GraphLoopIteration // iteration prefix for instance keys
	parent    *scopeRun                  // parent scope (nil for main)
	live      int                        // in-flight async units (workers + active child loops)

	// Loop-specific (zero for the main scope).
	loopNode          model.GraphNode
	loopKey           model.GraphInstanceKey // the loop container's key in the parent scope
	iterIndex         int                    // current 0-based round index
	roundsRun         int                    // number of rounds actually started (for denominator 回算)
	maxIters          int                    // static upper bound on rounds
	accumSnapshot     map[string]string      // cross-round accumulated snapshot (§3 不轮间隔离)
	inflowSession     string                 // session flowing into the loop (§3 会话血缘); carried into each round and out via finishLoop
	roundEntry        map[string]string      // this round's entry snapshot (fallback round-end value)
	roundEntrySession string                 // this round's entry session (fallback round-end session)
	roundContribs     []UpstreamSnapshot     // this round's activated internal-end contributions
	stopLoopRequested bool                   // a Shell quartet_break/STOP_LOOP ended this container early
}

// startLoop activates a loop container instance: it records the container as
// running, seeds the first round (or finishes immediately for a 0-count fixed
// loop), and accounts the whole loop as one async unit in the parent scope.
func (sc *scheduler) startLoop(ctx context.Context, parent *scopeRun, node model.GraphNode, key model.GraphInstanceKey, visible map[string]string, inflowSession string) {
	keyStr := instanceKeyString(key)
	now := time.Now().UnixMilli()
	sc.instances[keyStr] = model.GraphInstanceState{
		Key: key, NodeID: node.ID, NodeTitle: node.Title, NodeType: node.Type,
		Status: model.GraphInstanceStatusRunning, Version: sc.run.CurrentVersion,
		VisibleVariables: cloneStringMap(visible), StartedAt: now, SessionID: inflowSession,
	}
	updateRunProgress(sc.run, sc.instances)
	if sc.checkRunLimits(ctx) {
		return
	}
	sc.persist(ctx)
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceStarted, key, node.ID, "loop started", nil)
	logger.Infof(ctx, "[graph] loop started: runId=%s nodeId=%s key=%s mode=%s maxIters=%d fixedCount=%d",
		sc.run.ID, node.ID, keyStr, node.Config.LoopMode, effectiveLoopMaxIters(sc.cfg.RunConfig, node), node.Config.FixedCount)

	parent.live++ // the loop container is one async unit in the parent scope.
	loop := &scopeRun{
		container:     node.ID,
		parent:        parent,
		loopNode:      node,
		loopKey:       key,
		maxIters:      effectiveLoopMaxIters(sc.cfg.RunConfig, node),
		accumSnapshot: cloneStringMap(visible),
		inflowSession: inflowSession,
	}
	sc.activeLoops[keyStr] = loop

	// A fixed loop with count 0 runs no rounds: the external snapshot is the
	// entry snapshot and the container is done immediately.
	if node.Config.LoopMode == model.GraphLoopModeFixed && node.Config.FixedCount <= 0 {
		logger.Infof(ctx, "[graph] loop skipped zero rounds: runId=%s nodeId=%s key=%s", sc.run.ID, node.ID, keyStr)
		sc.finishLoop(ctx, loop)
		return
	}
	sc.startIteration(ctx, loop, 0)
}

// startIteration seeds one round of a loop: it appends the iteration to the
// prefix, snapshots the round entry, and decides the subgraph's single entry
// node with the accumulated snapshot.
func (sc *scheduler) startIteration(ctx context.Context, loop *scopeRun, index int) {
	loop.iterIndex = index
	loop.roundsRun = index + 1
	loop.prefix = append(append([]model.GraphLoopIteration{}, loop.parent.prefix...),
		model.GraphLoopIteration{LoopNodeID: loop.container, Index: index})
	loop.roundEntry = cloneStringMap(loop.accumSnapshot)
	loop.roundEntrySession = loop.inflowSession
	loop.roundContribs = nil
	loop.live = 0

	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeLoopIteration, &loop.loopKey, loop.container, "",
		fmt.Sprintf("loop %s iteration %d", loop.container, index), sc.run.Progress, nil)
	logger.Infof(ctx, "[graph] loop iteration started: runId=%s loopId=%s loopKey=%s iteration=%d visibleVars=%d sessionId=%s",
		sc.run.ID, loop.container, instanceKeyString(loop.loopKey), index, len(loop.accumSnapshot), loop.inflowSession)

	entryID, ok := sc.loopEntry[loop.container]
	if !ok {
		sc.failRunSched(ctx, fmt.Errorf("loop container %s has no subgraph entry node", loop.container))
		return
	}
	// The entry is the loop-scoped start node (the container's entry marker). Seed
	// the round by resolving its out-edges with the accumulated snapshot, exactly
	// as seedFresh seeds the main graph's start nodes — this supports a round that
	// fans out into parallel branches, not just a single entry node. The loop's
	// inflow session flows into the round (§3 会话血缘).
	entryKey := scopeKey(loop.prefix, entryID)
	sc.anyActive[instanceKeyString(entryKey)] = true
	contrib := UpstreamSnapshot{
		NodeID:           entryID,
		Variables:        cloneStringMap(loop.accumSnapshot),
		LastAssistantMsg: loop.accumSnapshot[lastAssistantKey],
		SessionID:        loop.inflowSession,
	}
	for _, e := range sc.outEdges[entryID] {
		sc.resolveEdge(ctx, loop, e, true, contrib)
		if sc.failed {
			return
		}
	}
	// If the round contained only synchronous work (e.g. an If-Else entry routing
	// straight to an internal end), no async unit was registered and live is
	// already zero — finish the round now. Workers, if any were enqueued, drive
	// quiescence later via handleResult.
	sc.onScopeQuiesced(ctx, loop)
}

// onScopeQuiesced is invoked whenever a scope's live count may have reached zero.
// For a loop scope at zero, the current round has finished and its termination
// rule is evaluated — unless a graceful stop (pause / step-stop) is in progress,
// in which case the loop is left open at the round boundary for resume to pick
// up (§2 循环内触发停在当前批次边界).
func (sc *scheduler) onScopeQuiesced(ctx context.Context, scope *scopeRun) {
	if scope.container == "" || scope.live > 0 || sc.failed || sc.stopScheduling {
		return
	}
	if sc.pauseRequested || sc.stepStop {
		return
	}
	sc.finishIteration(ctx, scope)
}

// finishIteration closes the current round: it computes the round-end snapshot,
// folds it into the accumulated snapshot, and decides whether to run another
// round or finish the loop.
func (sc *scheduler) finishIteration(ctx context.Context, loop *scopeRun) {
	// Round-end snapshot = join of all activated internal ends; if none were
	// activated (all internal paths pruned), reuse this round's entry snapshot.
	var roundEnd map[string]string
	var roundEndSession string
	if len(loop.roundContribs) > 0 {
		roundEnd = MergeVisibleSnapshots(loop.roundContribs)
		roundEndSession = pickInflowSession(loop.roundContribs)
	} else {
		roundEnd = cloneStringMap(loop.roundEntry)
		roundEndSession = loop.roundEntrySession
	}
	loop.accumSnapshot = roundEnd
	// The round-end session accumulates across rounds (§3 不轮间隔离), so the next
	// round's entry — and the loop's outflow — inherits the latest round's session.
	loop.inflowSession = roundEndSession

	if loop.stopLoopRequested {
		logger.Infof(ctx, "[graph] loop ending after STOP_LOOP: runId=%s loopId=%s loopKey=%s iteration=%d",
			sc.run.ID, loop.container, instanceKeyString(loop.loopKey), loop.iterIndex)
		sc.finishLoop(ctx, loop)
		return
	}

	node := loop.loopNode
	switch node.Config.LoopMode {
	case model.GraphLoopModeFixed:
		if loop.iterIndex+1 >= node.Config.FixedCount {
			logger.Infof(ctx, "[graph] fixed loop completed rounds: runId=%s loopId=%s loopKey=%s rounds=%d",
				sc.run.ID, loop.container, instanceKeyString(loop.loopKey), loop.roundsRun)
			sc.finishLoop(ctx, loop)
			return
		}
		sc.startIteration(ctx, loop, loop.iterIndex+1)
	case model.GraphLoopModeUntil:
		// The until condition sees the round-end snapshot plus the loop iteration
		// vars (QUARTET_LOOP_INDEX is the round that just finished). withLoopVars
		// clones, so roundEnd — which becomes accumSnapshot / the loop's external
		// snapshot — is not polluted with the engine vars.
		result, cerr := EvaluateCondition(node.Config.UntilCondition, CondEvalInput{Variables: withLoopVars(roundEnd, loop), Disabled: sc.disabled})
		if cerr != nil {
			logger.Errorf(ctx, "[graph] loop until condition failed: runId=%s loopId=%s loopKey=%s iteration=%d err=%v",
				sc.run.ID, loop.container, instanceKeyString(loop.loopKey), loop.iterIndex, cerr)
			sc.failLoopNode(ctx, loop, cerr)
			return
		}
		logger.Infof(ctx, "[graph] loop until evaluated: runId=%s loopId=%s loopKey=%s iteration=%d result=%v",
			sc.run.ID, loop.container, instanceKeyString(loop.loopKey), loop.iterIndex, result)
		if result {
			sc.finishLoop(ctx, loop)
			return
		}
		if loop.iterIndex+1 >= loop.maxIters {
			logger.Errorf(ctx, "[graph] loop max iterations reached: runId=%s loopId=%s loopKey=%s maxIters=%d",
				sc.run.ID, node.ID, instanceKeyString(loop.loopKey), loop.maxIters)
			sc.failRunSched(ctx, fmt.Errorf("loop %s reached max iterations (%d) but the until condition was never satisfied: 循环达最大次数但条件未满足", node.ID, loop.maxIters))
			return
		}
		sc.startIteration(ctx, loop, loop.iterIndex+1)
	default:
		sc.failRunSched(ctx, fmt.Errorf("loop %s has unknown loop mode %q", node.ID, node.Config.LoopMode))
	}
}

// finishLoop completes a loop container: it marks the container succeeded with
// its external snapshot, then resolves the container's out-edges in the parent
// scope and accounts the loop unit as done.
func (sc *scheduler) finishLoop(ctx context.Context, loop *scopeRun) {
	parent := loop.parent
	keyStr := instanceKeyString(loop.loopKey)
	delete(sc.activeLoops, keyStr)
	// Denominator 回算 (§4): the static progress bound assumed loopMaxRounds
	// rounds for this container's subgraph (FixedCount for fixed mode, the
	// backstop for until mode). Reclaim the business instances of rounds that
	// never ran (until early-stop, STOP_LOOP, fixed count < bound, 0-count).
	staticRounds := loopMaxRounds(sc.cfg.RunConfig, loop.loopNode)
	if unrun := staticRounds - loop.roundsRun; unrun > 0 {
		sc.denomAdjust(-unrun * sc.loopSubgraphBusinessCount(loop.container))
	}
	state := sc.instances[keyStr]
	now := time.Now().UnixMilli()
	state.Status = model.GraphInstanceStatusSucceeded
	state.VisibleVariables = cloneStringMap(loop.accumSnapshot)
	state.SessionID = loop.inflowSession
	state.FinishedAt = now
	state.DurationMs = now - state.StartedAt
	if state.DurationMs < 0 {
		state.DurationMs = 0
	}
	sc.instances[keyStr] = state
	sc.varsByKey[keyStr] = cloneStringMap(loop.accumSnapshot)
	updateRunProgress(sc.run, sc.instances)
	if sc.checkRunLimits(ctx) {
		return
	}
	sc.persist(ctx)
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceCompleted, loop.loopKey, loop.container, "loop completed", nil)
	logger.Infof(ctx, "[graph] loop completed: runId=%s loopId=%s loopKey=%s rounds=%d durationMs=%d outputVars=%d sessionId=%s",
		sc.run.ID, loop.container, keyStr, loop.roundsRun, state.DurationMs, len(loop.accumSnapshot), loop.inflowSession)

	contrib := UpstreamSnapshot{
		NodeID:           loop.container,
		Variables:        loop.accumSnapshot,
		LastAssistantMsg: loop.accumSnapshot[lastAssistantKey],
		SessionID:        loop.inflowSession,
	}
	if !sc.stopScheduling {
		for _, e := range sc.outEdges[loop.container] {
			sc.resolveEdge(ctx, parent, e, true, contrib)
			if sc.failed {
				return
			}
		}
	}
	parent.live-- // the loop unit in the parent scope is done.
	sc.onScopeQuiesced(ctx, parent)
}

// failLoopNode fails the loop container instance (e.g. an until-condition
// evaluation error) and propagates failure to the whole run.
func (sc *scheduler) failLoopNode(ctx context.Context, loop *scopeRun, err error) {
	keyStr := instanceKeyString(loop.loopKey)
	delete(sc.activeLoops, keyStr)
	state := sc.instances[keyStr]
	now := time.Now().UnixMilli()
	rerr := runtimeError(sc.run.ID, loop.loopKey, loop.loopNode, err)
	state.Status = model.GraphInstanceStatusFailed
	state.Error = rerr
	state.FinishedAt = now
	sc.instances[keyStr] = state
	sc.run.LastError = rerr
	updateRunProgress(sc.run, sc.instances)
	sc.run.Progress.LastError = rerr.Message
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceFailed, loop.loopKey, loop.container, err.Error(), rerr)
	logger.Errorf(ctx, "[graph] loop failed: runId=%s loopId=%s loopKey=%s err=%v",
		sc.run.ID, loop.container, keyStr, err)
	sc.markFailed(ctx, now)
}

// effectiveLoopMaxIters resolves the loop's max-iteration backstop: the node's
// own MaxIterations, else the run config default, else 100. The validator caps
// these at maxLoopMaxIters (1000) and rejects a fixed count above the backstop.
func effectiveLoopMaxIters(rc model.GraphRunConfig, node model.GraphNode) int {
	if node.Config.MaxIterations > 0 {
		return node.Config.MaxIterations
	}
	if rc.DefaultLoopMaxIters > 0 {
		return rc.DefaultLoopMaxIters
	}
	return 100
}
