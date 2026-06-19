package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// DAG ready-queue scheduler (§2 执行引擎：就绪队列调度器) with loop-subgraph
// driving (step 13). Covers steps 9/10/11 (branch/join/concurrency) and now 13:
//
//   - 入边计数 + 边三态机：每实例记未满足入边数，入口先就绪；实例完成后把出边
//     标记 active/pruned，下游计数减一、归零即裁决。
//   - 并发执行：就绪业务结点并发跑，单一信号量是整个 run 的全局并发上界（主图
//     与所有〔含嵌套〕循环子图共享）；start/end/循环容器不占名额、不执行；结果
//     回收与落库都在调度 goroutine 单写者完成。
//   - If-Else 剪枝：对可见快照求条件，只激活 yes/no 一条、另一条剪枝；全入边
//     剪枝则自身剪枝并向下游传播（防 join 永久等待）。
//   - 汇总 join：多入边等全部入边解析，≥1 激活则执行、全剪枝则跳过并传播。
//   - 循环子图驱动：固定次数 / 直到条件 do-while / 0 次跳过 / 内部唯一入口 /
//     内部 end 轮末 join / 跨轮累积快照 / 嵌套循环（见 loop.go）。
//
// Concurrency model: a single scheduler goroutine owns ALL mutable run state
// (instances, edges, variables, in-edge counts, scope/loop state, progress) and
// is the only writer to disk. Worker goroutines run only the pure node
// execution and send their result back over a channel — no shared mutable
// state, no locks.
//
// Instance-key model: all run-time maps are keyed by the instance-key STRING
// (instanceKeyString of {NodeID, Iterations}). In the main graph the iteration
// prefix is empty, so a key degenerates to the node ID and behaviour matches the
// pre-loop scheduler exactly.

// readyItem is a decided-active business instance awaiting dispatch, carrying
// the visible variable snapshot computed at decision time and the scope it
// belongs to (main graph or a loop iteration). inflowSession is the session
// flowing in along the instance's in-edges (§3 会话血缘): an `inherit` Agent
// forks from it, other nodes pass it through as their outflow.
type readyItem struct {
	node          model.GraphNode
	outEdges      []model.GraphEdge
	run           *model.GraphRun
	runConfig     model.GraphRunConfig
	disabled      map[string]struct{}
	key           model.GraphInstanceKey
	visible       map[string]string
	scope         *scopeRun
	inflowSession string
}

// nodeResult is a worker's completion report.
type nodeResult struct {
	node          model.GraphNode
	outEdges      []model.GraphEdge
	key           model.GraphInstanceKey
	scope         *scopeRun
	visible       map[string]string // snapshot the node executed against
	inflowSession string            // session that flowed into the node
	outcome       nodeOutcome       // raw output + named outputs + control signals
	err           error
}

// scheduler holds the in-memory run state for one GraphRun execution. Only the
// scheduler goroutine touches these fields.
type scheduler struct {
	svc    *serviceImpl
	run    *model.GraphRun
	runner Runner
	jobs   JobStateSink

	cfg      model.GraphConfig
	disabled map[string]struct{}

	nodesByID map[string]model.GraphNode
	outEdges  map[string][]model.GraphEdge
	inDegree  map[string]int    // intra-scope static in-edge count by node ID
	loopEntry map[string]string // loop container ID -> its single subgraph entry node ID

	inRemaining map[string]int                // unresolved in-edge count by instance-key string
	anyActive   map[string]bool               // any in-edge activated, by instance-key string
	contribs    map[string][]UpstreamSnapshot // active in-edge contributions, by instance-key string

	instances map[string]model.GraphInstanceState
	edges     map[string]model.GraphEdgeState
	varsByKey map[string]map[string]string

	mainScope *scopeRun

	ready          []readyItem
	endReached     bool
	failed         bool
	stopScheduling bool // STOP_WORKFLOW: stop dispatching, terminate with early success

	// Run-control state (steps 15/16). All mutated only by the scheduler
	// goroutine in response to a controlSignal delivered over `control`.
	control        <-chan controlSignal
	pauseRequested bool                   // 暂停/优雅停止: stop dispatching new ready items
	stepStop       bool                   // 步骤后停止: only dispatch the frozen batch
	stopReason     string                 // reason recorded on graceful/hard stop
	batchSeq       int                    // monotonically increasing ready-batch id source
	curBatch       *model.GraphReadyBatch // the open ready batch (step-stop boundary)

	activeLoops map[string]*scopeRun // active loop scopes by loop-instance-key string
}

const (
	defaultGraphInstanceLimit     = 100000
	defaultGraphSnapshotByteLimit = int64(1 << 30)
)

// runGraph is the DAG scheduler entry point, launched as a goroutine by
// StartRun (resume=false) or ResumeRun (resume=true). It drives the run to a
// terminal state and persists every state transition.
func (s *serviceImpl) runGraph(ctx context.Context, runID string, runner Runner, jobs JobStateSink, resume bool) {
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		logger.Errorf(ctx, "[graph] load run failed: runId=%s err=%v", runID, err)
		return
	}

	handle, ctx := s.registerControl(runID, ctx)
	defer s.clearControl(runID, handle)

	cfg := effectiveConfig(run)
	if timeout := jobTimeout(cfg.RunConfig); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	sc := &scheduler{
		svc:         s,
		run:         run,
		runner:      runner,
		jobs:        jobs,
		cfg:         cfg,
		disabled:    disabledNameSet(cfg.DisabledVars),
		nodesByID:   map[string]model.GraphNode{},
		outEdges:    map[string][]model.GraphEdge{},
		inDegree:    map[string]int{},
		loopEntry:   map[string]string{},
		inRemaining: map[string]int{},
		anyActive:   map[string]bool{},
		contribs:    map[string][]UpstreamSnapshot{},
		instances:   map[string]model.GraphInstanceState{},
		edges:       map[string]model.GraphEdgeState{},
		varsByKey:   map[string]map[string]string{},
		control:     handle.controlCh,
		activeLoops: map[string]*scopeRun{},
	}
	sc.mainScope = &scopeRun{container: "", prefix: nil}
	sc.index()
	logger.Infof(ctx, "[graph] scheduler starting: runId=%s jobId=%s resume=%v nodes=%d edges=%d concurrency=%d jobTimeout=%s",
		run.ID, run.JobID, resume, len(sc.cfg.Nodes), len(sc.cfg.Edges),
		concurrencyLimit(sc.cfg.RunConfig.ConcurrencyLimit), jobTimeout(sc.cfg.RunConfig))
	sc.loop(ctx, resume)
}

// index builds adjacency, static in-edge counts and each loop's subgraph entry.
// Only intra-scope edges (matching ParentID) participate, mirroring the
// validator.
func (sc *scheduler) index() {
	sc.nodesByID = map[string]model.GraphNode{}
	sc.outEdges = map[string][]model.GraphEdge{}
	sc.inDegree = map[string]int{}
	sc.loopEntry = map[string]string{}
	for _, n := range sc.cfg.Nodes {
		sc.nodesByID[n.ID] = n
	}
	for _, e := range sc.cfg.Edges {
		src, srcOK := sc.nodesByID[e.SourceNodeID]
		dst, dstOK := sc.nodesByID[e.TargetNodeID]
		if !srcOK || !dstOK || src.ParentID != dst.ParentID {
			continue
		}
		sc.outEdges[e.SourceNodeID] = append(sc.outEdges[e.SourceNodeID], e)
		sc.inDegree[e.TargetNodeID]++
	}
	// Each loop container's subgraph entry is its single child node (business or
	// nested loop, not start/end) with no intra-scope in-edge. The validator
	// guarantees exactly one such entry per container.
	for _, n := range sc.cfg.Nodes {
		if n.ParentID == "" || n.Type == model.GraphNodeTypeStart || n.Type == model.GraphNodeTypeEnd {
			continue
		}
		if sc.inDegree[n.ID] == 0 {
			sc.loopEntry[n.ParentID] = n.ID
		}
	}
}

func (sc *scheduler) applyVersionUpdate(ctx context.Context, sig controlSignal) {
	result := versionUpdateResult{}
	if sig.versionReq == nil {
		result.err = fmt.Errorf("request is required")
		sc.sendVersionUpdateResult(sig, result)
		return
	}
	logger.Infof(ctx, "[graph] applying run version update: runId=%s currentVersion=%d reason=%q", sc.run.ID, sc.run.CurrentVersion, sig.versionReq.Reason)
	updated, err := sc.svc.appendRunVersion(ctx, sc.run, sig.versionReq, sig.versionRunner, sc.instances)
	if err != nil {
		logger.Errorf(ctx, "[graph] run version update failed: runId=%s currentVersion=%d err=%v", sc.run.ID, sc.run.CurrentVersion, err)
		result.err = err
		sc.sendVersionUpdateResult(sig, result)
		return
	}
	sc.run = updated
	sc.cfg = effectiveConfig(sc.run)
	sc.disabled = disabledNameSet(sc.cfg.DisabledVars)
	sc.index()
	sc.remapReadyToLatestVersion(ctx)
	sc.persist(ctx)
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeLog, nil, "", "",
		fmt.Sprintf("graph run version updated: version=%d", sc.run.CurrentVersion), sc.run.Progress, nil)
	logger.Infof(ctx, "[graph] run version updated: runId=%s version=%d ready=%d", sc.run.ID, sc.run.CurrentVersion, len(sc.ready))
	result.run = cloneGraphRun(sc.run)
	sc.sendVersionUpdateResult(sig, result)
}

func (sc *scheduler) sendVersionUpdateResult(sig controlSignal, result versionUpdateResult) {
	if sig.versionResp == nil {
		return
	}
	select {
	case sig.versionResp <- result:
	default:
	}
}

func (sc *scheduler) remapReadyToLatestVersion(ctx context.Context) {
	for i := range sc.ready {
		node, ok := sc.nodesByID[sc.ready[i].node.ID]
		if !ok || node.Type != sc.ready[i].node.Type || node.ParentID != sc.ready[i].node.ParentID {
			continue
		}
		sc.ready[i].node = node
		sc.ready[i].outEdges = cloneGraphEdges(sc.outEdges[node.ID])
		sc.ready[i].run = cloneGraphRun(sc.run)
		sc.ready[i].runConfig = sc.cfg.RunConfig
		sc.ready[i].disabled = cloneStringSet(sc.disabled)
		keyStr := instanceKeyString(sc.ready[i].key)
		st, ok := sc.instances[keyStr]
		if !ok || st.Status != model.GraphInstanceStatusRunning {
			continue
		}
		st.NodeTitle = node.Title
		st.NodeType = node.Type
		st.Version = sc.run.CurrentVersion
		sc.instances[keyStr] = st
	}
}

func (sc *scheduler) loop(ctx context.Context, resume bool) {
	startedAt := time.Now().UnixMilli()
	if resume && sc.run.StartedAt > 0 {
		startedAt = sc.run.StartedAt
	}
	sc.run.Status = model.GraphRunStatusRunning
	sc.run.StartedAt = startedAt
	sc.run.FinishedAt = 0
	sc.run.UpdatedAt = time.Now()
	_ = sc.svc.runRepo.SaveRun(ctx, sc.run)
	if sc.jobs != nil {
		_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, model.JobStatusRunning, startedAt, 0)
	}
	logger.Infof(ctx, "[graph] run started: runId=%s jobId=%s resume=%v startedAt=%d total=%d",
		sc.run.ID, sc.run.JobID, resume, startedAt, sc.run.Progress.TotalCount)

	if resume {
		if err := sc.seedResume(ctx); err != nil {
			sc.failRunSched(ctx, err)
			return
		}
	} else {
		sc.seedFresh(ctx)
	}
	if sc.failed {
		sc.interruptRunning(ctx, "cancelled after graph run failure")
		sc.persist(ctx)
		return
	}

	// Worker pool: a counting semaphore enforces the global concurrency bound;
	// completions stream back over resultCh and are processed by this goroutine
	// alone, keeping all state mutation single-writer.
	limit := concurrencyLimit(sc.cfg.RunConfig.ConcurrencyLimit)
	sem := make(chan struct{}, limit)
	resultCh := make(chan nodeResult, limit)
	inFlight := 0
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	for {
		if ctx.Err() != nil {
			cancelWorkers()
			sc.drain(resultCh, &inFlight)
			sc.finishForContextError(context.Background(), ctx.Err())
			return
		}
		// Dispatch as many dispatchable ready instances as free semaphore slots
		// allow without blocking the loop (so completions can always free slots).
		// canDispatch gates pause/step-stop.
		for len(sc.ready) > 0 && sc.canDispatch(sc.ready[0]) {
			select {
			case sem <- struct{}{}:
				item := sc.ready[0]
				sc.ready = sc.ready[1:]
				inFlight++
				logger.Infof(ctx, "[graph] dispatch node: runId=%s nodeId=%s type=%s key=%s version=%d inFlight=%d ready=%d limit=%d timeout=%s",
					sc.run.ID, item.node.ID, item.node.Type, instanceKeyString(item.key), item.run.CurrentVersion,
					inFlight, len(sc.ready), limit, effectiveNodeTimeout(item.runConfig, item.node))
				go func(it readyItem) {
					nodeCtx, nodeCancel := contextWithNodeTimeout(workerCtx, it.runConfig, it.node)
					outcome, execErr := sc.svc.executeNode(nodeCtx, it.run, it.node, it.key, it.visible, it.disabled, sc.runner, it.inflowSession)
					if workerCtx.Err() == nil && errors.Is(nodeCtx.Err(), context.DeadlineExceeded) {
						execErr = nodeTimeoutErr(it.node, effectiveNodeTimeout(it.runConfig, it.node), execErr)
					}
					nodeCancel()
					resultCh <- nodeResult{node: it.node, outEdges: it.outEdges, key: it.key, scope: it.scope, visible: it.visible, inflowSession: it.inflowSession, outcome: outcome, err: execErr}
				}(item)
			default:
				goto wait
			}
		}
	wait:
		if inFlight == 0 && !sc.hasDispatchable() {
			// In-flight drained and nothing left to dispatch. If a graceful stop
			// (pause / step-stop) is in progress, finish into its terminal state;
			// otherwise the run has reached its natural end.
			if sc.pauseRequested || sc.stepStop {
				sc.finishGraceful(ctx)
				return
			}
			break
		}
		select {
		case res := <-resultCh:
			<-sem
			inFlight--
			logger.Infof(ctx, "[graph] worker result received: runId=%s nodeId=%s key=%s err=%v inFlight=%d ready=%d",
				sc.run.ID, res.node.ID, instanceKeyString(res.key), res.err != nil, inFlight, len(sc.ready))
			sc.handleResult(ctx, res)
			if sc.failed {
				cancelWorkers()
				sc.interruptRunning(ctx, "cancelled after graph run failure")
				sc.drain(resultCh, &inFlight)
				sc.persist(ctx)
				return
			}
		case sig := <-sc.control:
			if sig.kind == ctrlHardStop {
				logger.Warnf(ctx, "[graph] hard stop requested: runId=%s reason=%s inFlight=%d ready=%d", sc.run.ID, sig.reason, inFlight, len(sc.ready))
				cancelWorkers()
				sc.stopReason = orDefault(sig.reason, "hard stopped")
				sc.interruptRunning(ctx, sc.stopReason)
				sc.drain(resultCh, &inFlight)
				sc.finishStopped(ctx)
				return
			}
			if sig.kind == ctrlUpdateVersion {
				sc.applyVersionUpdate(ctx, sig)
				continue
			}
			sc.applyGracefulSignal(ctx, sig)
		case <-ctx.Done():
			cancelWorkers()
			sc.drain(resultCh, &inFlight)
			sc.finishForContextError(context.Background(), ctx.Err())
			return
		}
	}

	if !sc.endReached {
		sc.failRunSched(ctx, fmt.Errorf("无终点到达"))
		return
	}
	finishedAt := time.Now().UnixMilli()
	sc.run.Status = model.GraphRunStatusCompleted
	sc.run.FinishedAt = finishedAt
	sc.run.UpdatedAt = time.Now()
	updateRunProgress(sc.run, sc.instances)
	sc.persist(ctx)
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeProgressUpdated, nil, "", "", "run completed", sc.run.Progress, nil)
	logger.Infof(ctx, "[graph] run completed: runId=%s jobId=%s completed=%d skipped=%d failed=%d total=%d durationMs=%d",
		sc.run.ID, sc.run.JobID, sc.run.Progress.CompletedCount, sc.run.Progress.SkippedCount,
		sc.run.Progress.FailedCount, sc.run.Progress.TotalCount, finishedAt-sc.run.StartedAt)
	if sc.jobs != nil {
		_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, model.JobStatusCompleted, sc.run.StartedAt, finishedAt)
	}
}

// drain waits for any in-flight workers to observe cancellation and report back,
// so no goroutine leaks after a failure short-circuits the loop.
func (sc *scheduler) drain(resultCh <-chan nodeResult, inFlight *int) {
	for *inFlight > 0 {
		<-resultCh
		*inFlight--
	}
}

func (sc *scheduler) interruptRunning(ctx context.Context, reason string) {
	now := time.Now().UnixMilli()
	for key, state := range sc.instances {
		if state.Status != model.GraphInstanceStatusRunning {
			continue
		}
		state.Status = model.GraphInstanceStatusInterrupted
		state.FinishedAt = now
		state.DurationMs = now - state.StartedAt
		if state.DurationMs < 0 {
			state.DurationMs = 0
		}
		state.BlockedReason = reason
		sc.instances[key] = state
		logger.Warnf(ctx, "[graph] interrupt running instance: runId=%s nodeId=%s key=%s reason=%s",
			sc.run.ID, state.NodeID, key, reason)
		sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceFailed, state.Key, state.NodeID, reason, nil)
	}
	updateRunProgress(sc.run, sc.instances)
}

// handleResult processes one worker completion. On success it records the
// instance, applies Shell control signals (STOP_WORKFLOW / STOP_LOOP), merges
// produced outputs into a downstream snapshot and activates the node's out-edges
// (multiple out-edges = parallel fan-out), then advances the owning scope. On
// failure it fails the whole run.
func (sc *scheduler) handleResult(ctx context.Context, res nodeResult) {
	node := res.node
	scope := res.scope
	key := res.key
	keyStr := instanceKeyString(key)
	state := sc.instances[keyStr]
	finishedAt := time.Now().UnixMilli()
	state.FinishedAt = finishedAt
	state.DurationMs = finishedAt - state.StartedAt
	if state.DurationMs < 0 {
		state.DurationMs = 0
	}
	sc.svc.recordUsageSnapshot(sc.run, res.outcome, finishedAt, state.DurationMs)

	if res.err != nil {
		logger.Errorf(ctx, "[graph] node failed: runId=%s nodeId=%s key=%s durationMs=%d err=%v",
			sc.run.ID, node.ID, keyStr, state.DurationMs, res.err)
		sc.failInstance(ctx, &state, key, node, res.err, finishedAt, res.inflowSession, res.outcome)
		return
	}
	// STOP_LOOP outside a loop container is illegal → node failure (§1).
	if res.outcome.stopLoop && scope.container == "" {
		sc.failInstance(ctx, &state, key, node, fmt.Errorf("STOP_LOOP is only supported inside loop containers"), finishedAt, res.inflowSession, res.outcome)
		return
	}

	produced := res.outcome.produced
	if produced == nil {
		produced = map[string]string{}
	}
	visibleAfter := cloneStringMap(res.visible)
	for k, v := range produced {
		visibleAfter[k] = v
	}
	visibleAfter[lastAssistantKey] = res.outcome.output
	if node.Config.LastAssistantAlias != "" {
		visibleAfter[node.Config.LastAssistantAlias] = res.outcome.output
		produced[node.Config.LastAssistantAlias] = res.outcome.output
	}
	// Outflow session (§3 会话血缘): an Agent node carries the session it created
	// or forked; any other node passes the inflow session through unchanged so it
	// keeps flowing to downstream Agents.
	outflowSession := res.inflowSession
	if res.outcome.sessionID != "" {
		outflowSession = res.outcome.sessionID
	}
	state.Status = model.GraphInstanceStatusSucceeded
	state.VisibleVariables = visibleAfter
	state.OutputVariables = cloneStringMap(produced)
	state.SessionID = outflowSession
	// DisplaySessionID is the session the UI lists/opens for this instance. A
	// Shell node records its own display session (ownSessionID); Agent and other
	// nodes show their outflow session. Kept distinct from SessionID so a Shell
	// display session never becomes a §3 lineage parent.
	if res.outcome.ownSessionID != "" {
		state.DisplaySessionID = res.outcome.ownSessionID
	} else {
		state.DisplaySessionID = outflowSession
	}
	sc.instances[keyStr] = state
	sc.varsByKey[keyStr] = cloneStringMap(visibleAfter)
	sc.recordSessionLineage(key, node, res.inflowSession, res.outcome.sessionID, res.outcome.replayCount)
	updateRunProgress(sc.run, sc.instances)
	if sc.checkRunLimits(ctx) {
		return
	}
	sc.persist(ctx)
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceCompleted, key, node.ID, "instance completed", nil)
	logger.Infof(ctx, "[graph] node completed: runId=%s nodeId=%s key=%s type=%s durationMs=%d outputVars=%d sessionId=%s",
		sc.run.ID, node.ID, keyStr, node.Type, state.DurationMs, len(produced), outflowSession)
	for range produced {
		sc.appendInstanceEvent(ctx, model.GraphEventTypeVariableWritten, key, node.ID, "variable written", nil)
	}
	for name := range produced {
		logger.Infof(ctx, "[graph] variable written: runId=%s nodeId=%s key=%s variable=%s", sc.run.ID, node.ID, keyStr, name)
	}

	// STOP_WORKFLOW: the run ends with early success. Stop dispatching, treat the
	// terminus as reached, and do not activate this node's out-edges.
	if res.outcome.stopWorkflow {
		logger.Infof(ctx, "[graph] STOP_WORKFLOW received: runId=%s nodeId=%s key=%s", sc.run.ID, node.ID, keyStr)
		sc.stopScheduling = true
		sc.endReached = true
		sc.ready = nil
		scope.live--
		return
	}

	contrib := UpstreamSnapshot{NodeID: node.ID, Variables: visibleAfter, LastAssistantMsg: res.outcome.output, SessionID: outflowSession}
	if res.outcome.stopLoop {
		// STOP_LOOP inside a loop: end the current container at the round
		// boundary. Prune this node's out-edges (do not continue downstream this
		// round) and flag the scope so finishIteration finishes the loop.
		scope.stopLoopRequested = true
		logger.Infof(ctx, "[graph] STOP_LOOP received: runId=%s nodeId=%s key=%s loop=%s", sc.run.ID, node.ID, keyStr, scope.container)
		for _, e := range sc.outEdges[node.ID] {
			sc.resolveEdge(ctx, scope, e, false, UpstreamSnapshot{})
			if sc.failed {
				return
			}
		}
	} else if !sc.stopScheduling {
		for _, e := range res.outEdges {
			sc.resolveEdge(ctx, scope, e, true, contrib)
			if sc.failed {
				return
			}
		}
	}
	scope.live--
	sc.onScopeQuiesced(ctx, scope)
}

// failInstance records an instance failure with full context and fails the run.
// It also records the failed instance's session (mirroring the success path's
// display-session logic) so a failed Agent/Shell node still surfaces in the UI
// session sidebar. Without this, a failed node's session id is dropped and the
// frontend skips it entirely — the user can't open the session to inspect why
// it failed (e.g. an output-protocol error whose evidence IS the model's raw
// output).
func (sc *scheduler) failInstance(ctx context.Context, state *model.GraphInstanceState, key model.GraphInstanceKey, node model.GraphNode, err error, finishedAt int64, inflowSession string, outcome nodeOutcome) {
	rerr := runtimeError(sc.run.ID, key, node, err)
	state.Status = model.GraphInstanceStatusFailed
	state.Error = rerr
	// Record the session this instance ran against (§3 会话血缘): an Agent node
	// carries its own created/forked session in outcome.sessionID; otherwise the
	// inflow session passes through. Shell nodes record their own transcript
	// session in ownSessionID, which is the display session.
	outflowSession := inflowSession
	if outcome.sessionID != "" {
		outflowSession = outcome.sessionID
	}
	state.SessionID = outflowSession
	if outcome.ownSessionID != "" {
		state.DisplaySessionID = outcome.ownSessionID
	} else {
		state.DisplaySessionID = outflowSession
	}
	sc.instances[instanceKeyString(key)] = *state
	sc.run.LastError = rerr
	updateRunProgress(sc.run, sc.instances)
	sc.run.Progress.LastError = rerr.Message
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceFailed, key, node.ID, err.Error(), rerr)
	sc.markFailed(ctx, finishedAt)
}

// recordSessionLineage records a succeeded instance's session lineage into the
// run's resume state (§3 会话血缘 / §4 resume), keyed by instance-key string.
// Only Agent-class nodes establish a session of their own (outflow != ""); for
// passthrough nodes (Shell/If-Else) nothing is recorded — their outflow is just
// the inherited inflow. The lineage drives crash-recovery / 续跑 so a forked
// session's parent linkage and replay count survive a restart.
func (sc *scheduler) recordSessionLineage(key model.GraphInstanceKey, node model.GraphNode, inflowSession, outflowSession string, replayCount int) {
	if outflowSession == "" {
		return
	}
	if sc.run.Resume == nil {
		sc.run.Resume = &model.GraphResumeState{}
	}
	if sc.run.Resume.SessionLineageByKey == nil {
		sc.run.Resume.SessionLineageByKey = map[string]model.GraphSessionLineage{}
	}
	lineage := model.GraphSessionLineage{
		Strategy:           node.Config.SessionStrategy,
		SessionID:          outflowSession,
		ParentSessionID:    inflowSession,
		ReplayMessageCount: replayCount,
	}
	if lineage.Strategy == "" {
		lineage.Strategy = model.GraphSessionStrategyNew
	}
	sc.run.Resume.SessionLineageByKey[instanceKeyString(key)] = lineage
}

// resolveEdge records an edge's terminal status (active/pruned) for the target
// instance, accumulates an activated upstream's contribution for the target
// join, and decrements the target's unresolved in-edge count — deciding the
// target when it reaches zero. The target shares the source's scope (the
// validator forbids edges crossing container boundaries).
func (sc *scheduler) resolveEdge(ctx context.Context, scope *scopeRun, e model.GraphEdge, active bool, contrib UpstreamSnapshot) {
	targetKey := scopeKey(scope.prefix, e.TargetNodeID)
	targetKeyStr := instanceKeyString(targetKey)
	sourceKey := scopeKey(scope.prefix, e.SourceNodeID)
	status := model.GraphEdgeStatusActive
	reason := ""
	if !active {
		status = model.GraphEdgeStatusPruned
		reason = "upstream pruned"
	} else {
		sc.contribs[targetKeyStr] = append(sc.contribs[targetKeyStr], contrib)
		sc.anyActive[targetKeyStr] = true
	}
	sc.edges[edgeStateKey(e.ID, targetKey)] = model.GraphEdgeState{
		EdgeID:            e.ID,
		SourceInstanceKey: sourceKey,
		TargetInstanceKey: targetKey,
		Status:            status,
		ResolvedAt:        time.Now().UnixMilli(),
		Reason:            reason,
	}
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeEdgeResolved, &targetKey, e.TargetNodeID, e.ID, "edge "+string(status), nil, nil)
	logger.Infof(ctx, "[graph] edge resolved: runId=%s edgeId=%s source=%s target=%s targetKey=%s status=%s remainingBefore=%d activeInputs=%d",
		sc.run.ID, e.ID, e.SourceNodeID, e.TargetNodeID, targetKeyStr, status, sc.inRemaining[targetKeyStr], len(sc.contribs[targetKeyStr]))

	if _, ok := sc.inRemaining[targetKeyStr]; !ok {
		sc.inRemaining[targetKeyStr] = sc.inDegree[e.TargetNodeID]
	}
	sc.inRemaining[targetKeyStr]--
	if sc.inRemaining[targetKeyStr] > 0 {
		logger.Infof(ctx, "[graph] join waiting: runId=%s nodeId=%s key=%s remaining=%d activeInputs=%d",
			sc.run.ID, e.TargetNodeID, targetKeyStr, sc.inRemaining[targetKeyStr], len(sc.contribs[targetKeyStr]))
		return
	}
	logger.Infof(ctx, "[graph] join ready: runId=%s nodeId=%s key=%s activeInputs=%d",
		sc.run.ID, e.TargetNodeID, targetKeyStr, len(sc.contribs[targetKeyStr]))
	sc.decide(ctx, scope, sc.nodesByID[e.TargetNodeID], targetKey, MergeVisibleSnapshots(sc.contribs[targetKeyStr]), pickInflowSession(sc.contribs[targetKeyStr]))
}

// decide is invoked once a node instance's every in-edge is resolved (or it is
// seeded directly as a loop subgraph entry). ≥1 active in-edge activates the
// instance (execute / route / mark-end / drive-loop); all-pruned prunes it and
// propagates the prune downstream. inflowSession is the session flowing into the
// instance (§3 会话血缘), resolved from its activated in-edges.
func (sc *scheduler) decide(ctx context.Context, scope *scopeRun, node model.GraphNode, key model.GraphInstanceKey, visible map[string]string, inflowSession string) {
	// Defensive idempotence (also relied on by resume): an instance already in a
	// reliable terminal state (succeeded/skipped) is never re-decided or re-run.
	if st, ok := sc.instances[instanceKeyString(key)]; ok {
		if st.Status == model.GraphInstanceStatusSucceeded || st.Status == model.GraphInstanceStatusSkipped {
			return
		}
	}
	if !sc.anyActive[instanceKeyString(key)] {
		logger.Infof(ctx, "[graph] node pruned by inputs: runId=%s nodeId=%s key=%s", sc.run.ID, node.ID, instanceKeyString(key))
		sc.pruneNode(ctx, scope, node, key)
		return
	}
	switch node.Type {
	case model.GraphNodeTypeEnd:
		if node.ParentID == "" {
			// Main-graph end: a terminus is reached (other paths keep running).
			sc.endReached = true
		} else {
			// Internal loop end: contribute its visible snapshot to the round-end
			// join (§2). End nodes have no out-edges; this is synchronous. The
			// inflow session is carried so it flows into the next round / out of
			// the loop container (§3 会话血缘).
			scope.roundContribs = append(scope.roundContribs, UpstreamSnapshot{
				NodeID:           node.ID,
				Variables:        visible,
				LastAssistantMsg: visible[lastAssistantKey],
				SessionID:        inflowSession,
			})
		}
	case model.GraphNodeTypeIfElse:
		sc.decideIfElse(ctx, scope, node, key, visible, inflowSession)
	case model.GraphNodeTypeShell, model.GraphNodeTypePrompt, model.GraphNodeTypeEvaluator:
		sc.enqueue(ctx, scope, node, key, visible, inflowSession)
	case model.GraphNodeTypeLoop:
		sc.startLoop(ctx, scope, node, key, visible, inflowSession)
	case model.GraphNodeTypeStart:
		// A start with in-edges is illegal (caught at save time); ignore.
	default:
		sc.failRunSched(ctx, fmt.Errorf("node %s has type %s which is not supported by the scheduler", node.ID, node.Type))
	}
}

// decideIfElse evaluates the routing condition against the instance's visible
// snapshot, activates the yes/no out-edge matching the result and prunes the
// other. The If-Else instance itself produces nothing and is recorded as
// succeeded. It runs synchronously in the scheduler goroutine (no worker slot).
// It passes the inflow session through unchanged (If-Else does not touch
// sessions) so downstream Agents on the chosen branch can still inherit it.
func (sc *scheduler) decideIfElse(ctx context.Context, scope *scopeRun, node model.GraphNode, key model.GraphInstanceKey, visible map[string]string, inflowSession string) {
	keyStr := instanceKeyString(key)
	now := time.Now().UnixMilli()
	in := CondEvalInput{Variables: visible, Disabled: sc.disabled}
	result, cerr := EvaluateCondition(node.Config.Condition, in)
	if cerr != nil {
		rerr := runtimeError(sc.run.ID, key, node, cerr)
		sc.instances[keyStr] = model.GraphInstanceState{
			Key: key, NodeID: node.ID, NodeTitle: node.Title, NodeType: node.Type,
			Status: model.GraphInstanceStatusFailed, Version: sc.run.CurrentVersion,
			VisibleVariables: visible, StartedAt: now, FinishedAt: now, Error: rerr,
		}
		sc.run.LastError = rerr
		updateRunProgress(sc.run, sc.instances)
		sc.run.Progress.LastError = rerr.Message
		sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceFailed, key, node.ID, cerr.Error(), rerr)
		sc.markFailed(ctx, now)
		return
	}

	sc.instances[keyStr] = model.GraphInstanceState{
		Key: key, NodeID: node.ID, NodeTitle: node.Title, NodeType: node.Type,
		Status: model.GraphInstanceStatusSucceeded, Version: sc.run.CurrentVersion,
		VisibleVariables: visible, StartedAt: now, FinishedAt: now,
	}
	sc.varsByKey[keyStr] = cloneStringMap(visible)
	updateRunProgress(sc.run, sc.instances)
	if sc.checkRunLimits(ctx) {
		return
	}
	sc.persist(ctx)
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceCompleted, key, node.ID, fmt.Sprintf("if-else routed to %v", boolPort(result)), nil)
	logger.Infof(ctx, "[graph] if-else routed: runId=%s nodeId=%s key=%s chosen=%s",
		sc.run.ID, node.ID, keyStr, boolPort(result))

	chosen := model.GraphEdgePortYes
	if !result {
		chosen = model.GraphEdgePortNo
	}
	contrib := UpstreamSnapshot{NodeID: node.ID, Variables: visible, LastAssistantMsg: visible[lastAssistantKey], SessionID: inflowSession}
	for _, e := range sc.outEdges[node.ID] {
		active := e.SourcePort == chosen
		sc.resolveEdge(ctx, scope, e, active, contrib)
		if sc.failed {
			return
		}
	}
}

// pruneNode records a pruned business instance as skipped (it was counted in the
// progress denominator) and prunes all of its out-edges, propagating the prune
// so downstream joins do not wait forever. Runs synchronously (no worker slot,
// no scope.live change).
func (sc *scheduler) pruneNode(ctx context.Context, scope *scopeRun, node model.GraphNode, key model.GraphInstanceKey) {
	if isBusiness(node.Type) {
		keyStr := instanceKeyString(key)
		now := time.Now().UnixMilli()
		sc.instances[keyStr] = model.GraphInstanceState{
			Key: key, NodeID: node.ID, NodeTitle: node.Title, NodeType: node.Type,
			Status: model.GraphInstanceStatusSkipped, Version: sc.run.CurrentVersion,
			StartedAt: now, FinishedAt: now, BlockedReason: "all in-edges pruned",
		}
		updateRunProgress(sc.run, sc.instances)
		if sc.checkRunLimits(ctx) {
			return
		}
		sc.persist(ctx)
		sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceSkipped, key, node.ID, "instance pruned", nil)
		logger.Infof(ctx, "[graph] node skipped: runId=%s nodeId=%s key=%s reason=%s",
			sc.run.ID, node.ID, keyStr, "all in-edges pruned")
		// Denominator 回算 (§4): a pruned loop container's subgraph instances were
		// counted in the static bound but will never materialize — reclaim them.
		if node.Type == model.GraphNodeTypeLoop {
			sc.denomAdjust(-loopMaxRounds(sc.cfg.RunConfig, node) * sc.loopSubgraphBusinessCount(node.ID))
		}
	}
	for _, e := range sc.outEdges[node.ID] {
		sc.resolveEdge(ctx, scope, e, false, UpstreamSnapshot{})
		if sc.failed {
			return
		}
	}
}

// enqueue records a business instance as running and pushes it onto the ready
// queue for the worker pool to dispatch, marking the owning scope as having one
// more async unit in flight. inflowSession travels with the ready item so the
// worker can fork it for an `inherit` Agent (§3 会话血缘).
func (sc *scheduler) enqueue(ctx context.Context, scope *scopeRun, node model.GraphNode, key model.GraphInstanceKey, visible map[string]string, inflowSession string) {
	if sc.stopScheduling {
		return
	}
	keyStr := instanceKeyString(key)
	sc.instances[keyStr] = model.GraphInstanceState{
		Key: key, NodeID: node.ID, NodeTitle: node.Title, NodeType: node.Type,
		Status: model.GraphInstanceStatusRunning, Version: sc.run.CurrentVersion,
		VisibleVariables: cloneStringMap(visible), StartedAt: time.Now().UnixMilli(),
	}
	sc.varsByKey[keyStr] = cloneStringMap(visible)
	updateRunProgress(sc.run, sc.instances)
	if sc.checkRunLimits(ctx) {
		return
	}
	sc.persist(ctx)
	sc.appendInstanceEvent(ctx, model.GraphEventTypeInstanceStarted, key, node.ID, "instance started", nil)
	logger.Infof(ctx, "[graph] node ready: runId=%s nodeId=%s key=%s type=%s version=%d visibleVars=%d readyBefore=%d",
		sc.run.ID, node.ID, keyStr, node.Type, sc.run.CurrentVersion, len(visible), len(sc.ready))
	scope.live++
	sc.ready = append(sc.ready, readyItem{
		node:          node,
		outEdges:      cloneGraphEdges(sc.outEdges[node.ID]),
		run:           cloneGraphRun(sc.run),
		runConfig:     sc.cfg.RunConfig,
		disabled:      cloneStringSet(sc.disabled),
		key:           key,
		visible:       visible,
		scope:         scope,
		inflowSession: inflowSession,
	})
}

// markFailed transitions the run to failed; cancellation of in-flight workers is
// handled by the caller. Already-terminal instances are left untouched.
func (sc *scheduler) markFailed(ctx context.Context, finishedAt int64) {
	sc.failed = true
	sc.run.Status = model.GraphRunStatusFailed
	sc.run.FinishedAt = finishedAt
	sc.run.UpdatedAt = time.Now()
	if sc.jobs != nil {
		_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, model.JobStatusFailed, sc.run.StartedAt, finishedAt)
	}
	logger.Errorf(ctx, "[graph] run failed: runId=%s jobId=%s status=%s error=%v", sc.run.ID, sc.run.JobID, sc.run.Status, sc.run.LastError)
}

func (sc *scheduler) finishForContextError(ctx context.Context, err error) {
	finishedAt := time.Now().UnixMilli()
	if errors.Is(err, context.DeadlineExceeded) {
		timeout := jobTimeout(sc.cfg.RunConfig)
		rerr := &model.GraphRuntimeError{
			RunID:     sc.run.ID,
			Message:   fmt.Sprintf("job timed out after %s: %v", timeout, err),
			CanResume: true,
		}
		sc.interruptRunning(ctx, "job timed out")
		sc.run.LastError = rerr
		sc.run.Status = model.GraphRunStatusTimedOut
		sc.run.FinishedAt = finishedAt
		sc.run.UpdatedAt = time.Now()
		if sc.run.Progress != nil {
			sc.run.Progress.LastError = rerr.Message
		}
		sc.persist(ctx)
		sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeError, nil, "", "", rerr.Message, sc.run.Progress, rerr)
		logger.Errorf(ctx, "[graph] job timeout: runId=%s jobId=%s timeout=%s err=%v", sc.run.ID, sc.run.JobID, timeout, err)
		if sc.jobs != nil {
			_ = sc.jobs.SetGraphRunState(ctx, sc.run.JobID, sc.run.ID, model.JobStatusFailed, sc.run.StartedAt, finishedAt)
		}
		return
	}
	sc.failRunSched(ctx, err)
}

// failRunSched fails the run for a scheduler-level reason (no end reached, loop
// max iterations exhausted, unexpected node type) not tied to a single node
// instance.
func (sc *scheduler) failRunSched(ctx context.Context, err error) {
	finishedAt := time.Now().UnixMilli()
	rerr := &model.GraphRuntimeError{RunID: sc.run.ID, Message: err.Error(), CanResume: true}
	sc.run.LastError = rerr
	updateRunProgress(sc.run, sc.instances)
	if sc.run.Progress != nil {
		sc.run.Progress.LastError = err.Error()
	}
	sc.svc.appendEvent(ctx, sc.run.ID, model.GraphEventTypeError, nil, "", "", err.Error(), sc.run.Progress, rerr)
	logger.Errorf(ctx, "[graph] scheduler failure: runId=%s jobId=%s err=%v", sc.run.ID, sc.run.JobID, err)
	sc.markFailed(ctx, finishedAt)
	sc.persist(ctx)
}

func (sc *scheduler) checkRunLimits(ctx context.Context) bool {
	if err := sc.runLimitError(); err != nil {
		sc.failRunSched(ctx, err)
		return true
	}
	return false
}

func (sc *scheduler) runLimitError() error {
	instanceLimit := effectiveInstanceLimit(sc.cfg.RunConfig)
	if len(sc.instances) > instanceLimit {
		return fmt.Errorf("graph run instance limit exceeded: current instances=%d, limit=%d", len(sc.instances), instanceLimit)
	}
	snapshotLimit := effectiveSnapshotByteLimit(sc.cfg.RunConfig)
	snapshotBytes := graphSnapshotBytes(sc.varsByKey)
	if snapshotBytes > snapshotLimit {
		return fmt.Errorf("graph run snapshot byte limit exceeded: current snapshot bytes=%d, limit=%d", snapshotBytes, snapshotLimit)
	}
	return nil
}

func effectiveInstanceLimit(cfg model.GraphRunConfig) int {
	if cfg.InstanceLimit <= 0 {
		return defaultGraphInstanceLimit
	}
	return cfg.InstanceLimit
}

func effectiveSnapshotByteLimit(cfg model.GraphRunConfig) int64 {
	if cfg.SnapshotByteLimit <= 0 {
		return defaultGraphSnapshotByteLimit
	}
	return cfg.SnapshotByteLimit
}

func graphSnapshotBytes(vars map[string]map[string]string) int64 {
	data, err := json.Marshal(vars)
	if err != nil {
		return int64(len(fmt.Sprintf("%v", vars)))
	}
	return int64(len(data))
}

func (sc *scheduler) persist(ctx context.Context) {
	if err := sc.svc.persistRuntimeState(ctx, sc.run, sc.instances, sc.edges, sc.varsByKey); err != nil {
		logger.Errorf(ctx, "[graph] persist run state failed: runId=%s err=%v", sc.run.ID, err)
	}
}

func (sc *scheduler) appendInstanceEvent(ctx context.Context, typ model.GraphEventType, key model.GraphInstanceKey, nodeID, msg string, rerr *model.GraphRuntimeError) {
	var progress *model.GraphProgress
	if typ == model.GraphEventTypeInstanceStarted || typ == model.GraphEventTypeInstanceCompleted ||
		typ == model.GraphEventTypeInstanceFailed || typ == model.GraphEventTypeInstanceSkipped {
		progress = sc.run.Progress
	}
	sc.svc.appendEvent(ctx, sc.run.ID, typ, &key, nodeID, "", msg, progress, rerr)
}

func boolPort(yes bool) model.GraphEdgePort {
	if yes {
		return model.GraphEdgePortYes
	}
	return model.GraphEdgePortNo
}

// scopeKey builds an instance key for a node within a scope whose iteration
// prefix is `prefix` (nil/empty in the main graph → key == node ID).
func scopeKey(prefix []model.GraphLoopIteration, nodeID string) model.GraphInstanceKey {
	if len(prefix) == 0 {
		return model.GraphInstanceKey{NodeID: nodeID}
	}
	return model.GraphInstanceKey{NodeID: nodeID, Iterations: append([]model.GraphLoopIteration{}, prefix...)}
}

// concurrencyLimit resolves the effective global concurrency bound: 0 means the
// default (4); the validator already rejects values outside [0, maxConcurrency].
func concurrencyLimit(configured int) int {
	if configured <= 0 {
		return 4
	}
	if configured > maxConcurrency {
		return maxConcurrency
	}
	return configured
}

func jobTimeout(cfg model.GraphRunConfig) time.Duration {
	if cfg.JobTimeoutSec <= 0 {
		return 0
	}
	return time.Duration(cfg.JobTimeoutSec) * time.Second
}

func effectiveNodeTimeout(cfg model.GraphRunConfig, node model.GraphNode) time.Duration {
	if node.Config.TimeoutSeconds != nil {
		if *node.Config.TimeoutSeconds <= 0 {
			return 0
		}
		return time.Duration(*node.Config.TimeoutSeconds) * time.Second
	}
	if cfg.DefaultNodeTimeoutSec <= 0 {
		return 0
	}
	return time.Duration(cfg.DefaultNodeTimeoutSec) * time.Second
}

func contextWithNodeTimeout(parent context.Context, cfg model.GraphRunConfig, node model.GraphNode) (context.Context, context.CancelFunc) {
	timeout := effectiveNodeTimeout(cfg, node)
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func nodeTimeoutErr(node model.GraphNode, timeout time.Duration, execErr error) error {
	msg := fmt.Sprintf("node %s timed out after %s", node.ID, timeout)
	if execErr == nil {
		return fmt.Errorf("%s: %w", msg, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w; execution error: %v", msg, context.DeadlineExceeded, execErr)
}

// ensureSchedulable rejects graphs the scheduler cannot run. With loop driving
// implemented (step 13) only the "no executable business node" guard remains;
// the config is assumed already validated (well-formed scopes, ports, acyclic
// per scope), so no further structural checks are needed here.
func ensureSchedulable(cfg model.GraphConfig) error {
	for _, n := range cfg.Nodes {
		if isBusiness(n.Type) {
			return nil
		}
	}
	return fmt.Errorf("%w: no executable business nodes", ErrGraphRunUnsupported)
}
