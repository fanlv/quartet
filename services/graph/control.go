package graph

import (
	"context"
	"sort"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Run control state machine (steps 15/16, §2 步骤后停止 + §4 停止).
//
// All functions here run in the single scheduler goroutine. Control intents
// arrive over sc.control (see loop()'s select); these helpers only mutate
// scheduler/run state and persist — never touched by another goroutine.

// seedFresh activates the main-graph start nodes' out-edges with the run's
// initial variable snapshot — the cold-start entry into the DAG.
func (sc *scheduler) seedFresh(ctx context.Context) {
	initial := UpstreamSnapshot{Variables: cloneStringMap(sc.cfg.Variables), Writers: initialVariableWriters(sc.cfg.Variables)}
	for _, n := range sc.cfg.Nodes {
		if n.Type == model.GraphNodeTypeStart && n.ParentID == "" {
			logger.Infof(ctx, "[graph] seed start node: runId=%s nodeId=%s outEdges=%d initialVars=%d",
				sc.run.ID, n.ID, len(sc.outEdges[n.ID]), len(sc.cfg.Variables))
			contrib := initial
			contrib.NodeID = n.ID
			for _, e := range sc.outEdges[n.ID] {
				sc.resolveEdge(ctx, sc.mainScope, e, true, contrib)
				if sc.failed {
					return
				}
			}
		}
	}
}

// canDispatch reports whether a ready item may be dispatched now given the
// current control state. A step-stop only allows the frozen batch's members
// through.
func (sc *scheduler) canDispatch(item readyItem) bool {
	if sc.stepStop {
		if sc.curBatch == nil {
			return false
		}
		_, ok := sc.curBatch.Members[instanceKeyString(item.key)]
		return ok
	}
	return true
}

// hasDispatchable reports whether any ready item can still be dispatched. Under
// a step-stop this becomes false once the frozen batch (or nothing) remains,
// which is the signal for loop() to finalise into stepStopped.
func (sc *scheduler) hasDispatchable() bool {
	for _, item := range sc.ready {
		if sc.canDispatch(item) {
			return true
		}
	}
	return false
}

// applyGracefulSignal records a step-stop request. It does not cancel in-flight
// work; it only changes what may be dispatched and where the run settles when
// the dispatchable frontier drains.
func (sc *scheduler) applyGracefulSignal(ctx context.Context, sig controlSignal) {
	switch sig.kind {
	case ctrlStepStop:
		if sc.stepStop {
			return
		}
		sc.stepStop = true
		sc.stopReason = orDefault(sig.reason, "step stopped")
		sc.run.Status = model.GraphRunStatusStepStopping
		sc.freezeCurrentBatch(ctx)
		memberCount := 0
		if sc.curBatch != nil {
			memberCount = len(sc.curBatch.Members)
		}
		logger.Infof(ctx, "[graph] step-stop requested: runId=%s reason=%s batchId=%s members=%d ready=%d",
			sc.run.ID, sc.stopReason, sc.curBatch.ID, memberCount, len(sc.ready))
	default:
		return
	}
	sc.run.UpdatedAt = time.Now()
	sc.persist(ctx)
}

// cancelGracefulSignal reverses a not-yet-settled step-stop request, returning
// the run to running. Because a step-stop never cancels in-flight work (it only
// gates dispatch), clearing the flag releases the held dispatch frontier: the
// next loop pass dispatches the previously-blocked ready items again. The frozen
// batch is discarded so a later step-stop re-freezes cleanly. No-op if no
// step-stop is pending.
func (sc *scheduler) cancelGracefulSignal(ctx context.Context, sig controlSignal) {
	if !sc.stepStop {
		return
	}
	wasStepStop := sc.stepStop
	sc.stepStop = false
	sc.stopReason = ""
	sc.curBatch = nil
	if sc.run.Resume != nil {
		sc.run.Resume.FrozenBatch = nil
	}
	sc.run.Status = model.GraphRunStatusRunning
	sc.run.UpdatedAt = time.Now()
	sc.persist(ctx)
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeProgressUpdated, nil, "", "",
		orDefault(sig.reason, "stop cancelled"), nil)
	logger.Infof(ctx, "[graph] graceful stop cancelled: runId=%s wasStepStop=%v ready=%d",
		sc.run.ID, wasStepStop, len(sc.ready))
}

// flight or queued (the "current ready batch", §2) into Resume.FrozenBatch.
// Only these members may keep dispatching; downstream instances decided later
// are held until resume.
func (sc *scheduler) freezeCurrentBatch(ctx context.Context) {
	sc.batchSeq++
	batch := &model.GraphReadyBatch{
		ID:        instanceKeyBatchID(sc.run.ID, sc.batchSeq),
		Version:   sc.run.CurrentVersion,
		Members:   map[string]model.GraphReadyBatchMember{},
		CreatedAt: time.Now().UnixMilli(),
	}
	// Members = every running business worker instance (dispatched-in-flight and
	// enqueued-not-yet-dispatched), captured at the instant the signal lands.
	for keyStr, st := range sc.instances {
		if st.Status == model.GraphInstanceStatusRunning && isBusiness(st.NodeType) {
			batch.Members[keyStr] = model.GraphReadyBatchMember{Key: st.Key, Status: st.Status}
		}
	}
	sc.curBatch = batch
	if sc.run.Resume == nil {
		sc.run.Resume = &model.GraphResumeState{}
	}
	sc.run.Resume.FrozenBatch = batch
	logger.Infof(ctx, "[graph] ready batch frozen: runId=%s batchId=%s members=%d version=%d",
		sc.run.ID, batch.ID, len(batch.Members), batch.Version)
}

// finishStopped finalises a hard stop: in-flight instances were already marked
// interrupted by the caller; record status=stopped and persist resume state so
// the run can be continued later.
func (sc *scheduler) finishStopped(ctx context.Context) {
	sc.rollbackUndispatched(ctx)
	finishedAt := time.Now().UnixMilli()
	sc.run.Status = model.GraphRunStatusStopped
	sc.run.FinishedAt = finishedAt
	sc.run.UpdatedAt = time.Now()
	sc.snapshotLoopState()
	updateRunProgress(sc.run, sc.instances)
	if sc.run.Progress != nil {
		sc.run.Progress.LastError = ""
	}
	sc.persist(ctx)
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeProgressUpdated, nil, "", "", "run stopped: "+sc.stopReason, nil)
	logger.Infof(ctx, "[graph] run stopped: runId=%s reason=%s completed=%d skipped=%d failed=%d total=%d",
		sc.run.ID, sc.stopReason, sc.run.Progress.CompletedCount, sc.run.Progress.SkippedCount,
		sc.run.Progress.FailedCount, sc.run.Progress.TotalCount)
	if sc.jobs != nil {
		_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, model.JobStatusStopped, model.GraphRunStatusStopped, sc.run.StartedAt, finishedAt, "")
	}
}

// finishGraceful finalises a step-stop once the dispatchable frontier has
// drained and no workers are in flight. Un-dispatched ready instances are rolled
// back so resume re-derives them cleanly.
func (sc *scheduler) finishGraceful(ctx context.Context) {
	sc.rollbackUndispatched(ctx)
	finishedAt := time.Now().UnixMilli()
	jobStatus := model.JobStatusStopped
	label := "run step-stopped"
	sc.run.Status = model.GraphRunStatusStepStopped
	sc.run.FinishedAt = finishedAt
	sc.run.UpdatedAt = time.Now()
	sc.snapshotLoopState()
	updateRunProgress(sc.run, sc.instances)
	sc.persist(ctx)
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeProgressUpdated, nil, "", "", label, nil)
	logger.Infof(ctx, "[graph] %s: runId=%s reason=%s completed=%d skipped=%d failed=%d total=%d",
		label, sc.run.ID, sc.stopReason, sc.run.Progress.CompletedCount, sc.run.Progress.SkippedCount,
		sc.run.Progress.FailedCount, sc.run.Progress.TotalCount)
	if sc.jobs != nil {
		_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, jobStatus, model.GraphRunStatusStepStopped, sc.run.StartedAt, finishedAt, "")
	}
}

// finishAwaiting finalises the run into the awaitingInput terminal (§ 交互澄清结点):
// one or more clarify nodes ran their turn (draft plan + open session) and the
// dispatchable frontier has drained with their out-edges held unresolved. The
// run settles like a step-stop — scheduler exits, state stays resumable —
// but into GraphRunStatusAwaitingInput, and the bound Job is set to a
// non-running status (JobStatusStopped, same as stop) so the Chat
// append-message path accepts the user's multi-turn discussion in the clarify
// session(s). The held edges and awaitingInput instances are finalized later by
// ContinueRun (read 结论 → succeeded → resolve out-edges → resume frontier).
//
// Un-dispatched ready items are rolled back exactly like finishGraceful so a
// continue re-derives them cleanly; awaitingInput instances are NOT rolled back
// (they are not in sc.ready — they completed their turn and are held in the
// instances map), and the continue path keys off their persisted status.
func (sc *scheduler) finishAwaiting(ctx context.Context) {
	sc.rollbackUndispatched(ctx)
	finishedAt := time.Now().UnixMilli()
	sc.run.Status = model.GraphRunStatusAwaitingInput
	sc.run.FinishedAt = finishedAt
	sc.run.UpdatedAt = time.Now()
	sc.snapshotLoopState()
	updateRunProgress(sc.run, sc.instances)
	if sc.run.Progress != nil {
		sc.run.Progress.LastError = ""
	}
	sc.persist(ctx)
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeProgressUpdated, nil, "", "", "run awaiting user input", nil)
	logger.Infof(ctx, "[graph] run awaiting input: runId=%s completed=%d skipped=%d failed=%d total=%d",
		sc.run.ID, sc.run.Progress.CompletedCount, sc.run.Progress.SkippedCount,
		sc.run.Progress.FailedCount, sc.run.Progress.TotalCount)
	if sc.jobs != nil {
		// JobStatusStopped is a non-running, non-terminal-for-chat status: the Chat
		// append path rejects only JobStatusRunning, so the user can discuss in the
		// clarify session while the run is parked. Continue re-launches via resume.
		graphSessionID := ""
		instanceKeys := make([]string, 0, len(sc.instances))
		for key := range sc.instances {
			instanceKeys = append(instanceKeys, key)
		}
		sort.Strings(instanceKeys)
		for _, key := range instanceKeys {
			instance := sc.instances[key]
			if instance.Status != model.GraphInstanceStatusAwaitingInput {
				continue
			}
			graphSessionID = firstNonEmpty(instance.DisplaySessionID, instance.SessionID)
			if graphSessionID != "" {
				break
			}
		}
		_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, model.JobStatusStopped, model.GraphRunStatusAwaitingInput, sc.run.StartedAt, finishedAt, graphSessionID)
	}
}

// rollbackUndispatched clears the instance state of ready items that were marked
// running by enqueue but never dispatched (held back by the dispatch gate). On
// resume, seedResume re-creates them from their resolved in-edges. Their
// already-resolved in-edges and recorded variable snapshots are left intact.
func (sc *scheduler) rollbackUndispatched(_ context.Context) {
	for _, item := range sc.ready {
		keyStr := instanceKeyString(item.key)
		st, ok := sc.instances[keyStr]
		if ok && st.Status == model.GraphInstanceStatusRunning {
			delete(sc.instances, keyStr)
			delete(sc.varsByKey, keyStr)
		}
	}
	sc.ready = nil
}

// snapshotLoopState walks the live scope tree and records one GraphLoopState per
// active loop container so a fresh scheduler can rebuild the scope tree on
// resume (§4 Resume/续跑). It is called on every resumable terminal transition —
// step-stop AND failure/timeout — so a resume continues each
// in-flight loop from its current round (step-level resume) rather than
// re-running it from round 0. Completed loops are derivable from their succeeded
// container instance, so only in-flight loops (still in sc.activeLoops) are
// persisted; a loop removed from activeLoops before failure (e.g. failLoopNode
// on an until-condition error) is intentionally absent and degrades to a
// wholesale re-run on resume (its condition is suspect).
func (sc *scheduler) snapshotLoopState() {
	if sc.run.Resume == nil {
		sc.run.Resume = &model.GraphResumeState{}
	}
	states := map[string]model.GraphLoopState{}
	for keyStr, scope := range sc.activeLoops {
		states[keyStr] = model.GraphLoopState{
			LoopNodeID:       scope.container,
			InstanceKey:      scope.loopKey,
			CurrentIteration: scope.iterIndex,
			Completed:        false,
			Variables:        cloneStringMap(scope.roundEntry),
			VariableWriters:  cloneStringMap(scope.roundEntryWriters),
			EntrySession:     scope.roundEntrySession,
		}
	}
	sc.run.Resume.LoopState = states
}

// orDefault returns s if non-empty, else def.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// instanceKeyBatchID builds a deterministic batch id from the run id and a
// monotonically increasing sequence (no time/random — replay-safe).
func instanceKeyBatchID(runID string, seq int) string {
	return runID + "#batch-" + itoa(seq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
