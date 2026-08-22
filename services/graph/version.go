package graph

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// Version-aware run config + mid-run version editing (step 6 + steps 15/16 tail,
// §4 运行配置与版本化编辑).
//
// A GraphRun carries an append-only Versions slice; CurrentVersion names the
// latest effective version. The baseline (version 1) is written at StartRun.
// Mid-run edits append a new version with its own content snapshot. When a
// scheduler is live, the edit is delivered to that scheduler over its control
// channel so the same single writer can append the version and refresh the
// effective topology. In-flight and already-queued instances keep the node/edge
// snapshot they were decided with; later not-yet-started instances use the
// latest version.

// effectiveConfig returns the GraphConfig of the run's current effective
// version, falling back to the baseline snapshot if the version is missing
// (older runs, or a corrupted Versions slice).
func effectiveConfig(run *model.GraphRun) model.GraphConfig {
	if run == nil {
		return model.GraphConfig{}
	}
	for i := range run.Versions {
		if run.Versions[i].Version == run.CurrentVersion {
			return run.Versions[i].Config
		}
	}
	return run.BaseSnapshot.Config
}

// EffectiveConfig exposes effectiveConfig to callers outside this package (the
// HTTP handler reads the run's resolved config for Job-title generation). It is
// a pure function of the run object — no service state — so it lives here rather
// than on the Service interface. The fallback to BaseSnapshot keeps it correct
// for legacy runs whose config was only ever stored on the base snapshot.
func EffectiveConfig(run *model.GraphRun) model.GraphConfig {
	return effectiveConfig(run)
}

// UpdateRunVersion validates an edit against the persisted run state and, on
// success, appends a new graph version (with frozen referenced Agent/model
// content) and advances CurrentVersion. In-flight edits are routed through the
// scheduler so its in-memory run state cannot overwrite the newly appended
// version on the next persist.
func (s *serviceImpl) UpdateRunVersion(ctx context.Context, runID string, req *model.UpdateGraphRunVersionRequest, src Runner) (*model.GraphRun, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	lifecycle := s.lifecycle(runID)
	var run *model.GraphRun
	for {
		lifecycle.mu.Lock()
		if lifecycle.deleted {
			lifecycle.mu.Unlock()
			return nil, ErrGraphRunNotFound
		}
		var err error
		run, err = s.runRepo.GetRun(ctx, runID)
		if err != nil {
			lifecycle.mu.Unlock()
			return nil, graphRunLoadError(runID, err)
		}
		if isInFlightStatus(run.Status) {
			handle := lifecycle.handle
			return s.updateRunVersionInFlightLocked(ctx, lifecycle, handle, run, req, src)
		}
		if !isStaticEditableStatus(run.Status) {
			lifecycle.mu.Unlock()
			return nil, fmt.Errorf("%w: status=%s", ErrGraphRunNotEditable, run.Status)
		}
		if handle := lifecycle.handle; handle != nil {
			lifecycle.mu.Unlock()
			select {
			case <-handle.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		lifecycle.mu.Unlock()
		break
	}

	oldCfg := effectiveConfig(run)
	baseVersion := run.CurrentVersion
	prepared, err := s.prepareRunVersion(ctx, run, req, src, nil)
	if err != nil {
		return nil, err
	}

	// Snapshot resolution may call a slow external runtime. Re-enter the
	// lifecycle only for the commit, then re-read and reject if deletion, resume,
	// or any other version update changed the snapshot we prepared from.
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.deleted {
		return nil, ErrGraphRunNotFound
	}
	latest, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if lifecycle.handle != nil || !isStaticEditableStatus(latest.Status) || latest.CurrentVersion != baseVersion || !graphConfigNoOpEqual(effectiveConfig(latest), oldCfg) {
		return nil, fmt.Errorf("%w: run changed while preparing version update", ErrGraphRunNotEditable)
	}
	if err := s.runRepo.SaveRun(ctx, prepared); err != nil {
		return nil, err
	}
	// A mid-run FixedCount change on a stopped run takes effect when the
	// scheduler rebuilds the loop scope on resume (rebuildLoopScopes re-sources
	// the node from the new config), but the persisted progress denominator was
	// seeded with the old count. Correct it here so the bar stays accurate; the
	// live (in-flight) path does the same inside applyVersionUpdate.
	if err := s.adjustStaticLoopDenominator(ctx, prepared, oldCfg); err != nil {
		return nil, err
	}
	return prepared, nil
}

// adjustStaticLoopDenominator corrects the persisted progress denominator after
// a static-path version edit changed a still-running loop container's FixedCount.
// It mirrors the live scheduler's per-loop reclaim: for each active loop recorded
// in Resume.LoopState whose FixedCount differs, it shifts TotalCount by the
// change in not-yet-run rounds × the loop subgraph's per-round business count.
func (s *serviceImpl) adjustStaticLoopDenominator(ctx context.Context, run *model.GraphRun, oldCfg model.GraphConfig) error {
	if run.Progress == nil || run.Resume == nil || len(run.Resume.LoopState) == 0 {
		return nil
	}
	newCfg := effectiveConfig(run)
	oldNodes := nodesByID(oldCfg)
	newNodes := nodesByID(newCfg)
	delta := 0
	for _, ls := range run.Resume.LoopState {
		on, ok := oldNodes[ls.LoopNodeID]
		if !ok {
			continue
		}
		nn, ok := newNodes[ls.LoopNodeID]
		if !ok || on.Config.FixedCount == nn.Config.FixedCount {
			continue
		}
		roundsRun := ls.CurrentIteration + 1
		oldRemaining := max(0, loopMaxRounds(oldCfg.RunConfig, on)-roundsRun)
		newRemaining := max(0, loopMaxRounds(newCfg.RunConfig, nn)-roundsRun)
		delta += (newRemaining - oldRemaining) * loopSubgraphBusinessCount(newNodes, newCfg.RunConfig, ls.LoopNodeID)
	}
	if delta == 0 {
		return nil
	}
	adjustDenomTotal(run.Progress, delta)
	return s.runRepo.SaveRun(ctx, run)
}

func isStaticEditableStatus(st model.GraphRunStatus) bool {
	switch st {
	case model.GraphRunStatusFailed, model.GraphRunStatusStepStopped,
		model.GraphRunStatusStopped,
		model.GraphRunStatusTimedOut, model.GraphRunStatusRecovering,
		model.GraphRunStatusAwaitingInput:
		return true
	default:
		return false
	}
}

// updateRunVersionInFlightLocked enters with lifecycle.mu held. It releases the
// lock immediately after enqueueing so a barrier can prove the request is in the
// scheduler queue without holding the lifecycle lock while awaiting a response.
func (s *serviceImpl) updateRunVersionInFlightLocked(ctx context.Context, lifecycle *runLifecycle, handle *runControl, run *model.GraphRun, req *model.UpdateGraphRunVersionRequest, src Runner) (*model.GraphRun, error) {
	if handle == nil {
		lifecycle.mu.Unlock()
		return nil, fmt.Errorf("%w: status=%s", ErrGraphRunNotEditable, run.Status)
	}
	resp := make(chan versionUpdateResult, 1)
	sig := controlSignal{
		kind:          ctrlUpdateVersion,
		versionReq:    req,
		versionRunner: src,
		versionResp:   resp,
	}
	if err := sendControlToHandle(handle, sig); err != nil {
		lifecycle.mu.Unlock()
		return nil, err
	}
	lifecycle.mu.Unlock()
	select {
	case result := <-resp:
		return result.run, result.err
	case <-handle.done:
		// A destructive stop may retire the scheduler before it reaches this
		// queued update. Do not strand the caller until its request deadline.
		select {
		case result := <-resp:
			return result.run, result.err
		default:
			return nil, ErrGraphRunNotEditable
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *serviceImpl) appendRunVersion(ctx context.Context, run *model.GraphRun, req *model.UpdateGraphRunVersionRequest, src Runner, instances map[string]model.GraphInstanceState) (*model.GraphRun, error) {
	updated, err := s.prepareRunVersion(ctx, run, req, src, instances)
	if err != nil {
		return nil, err
	}
	if err := s.runRepo.SaveRun(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *serviceImpl) prepareRunVersion(ctx context.Context, run *model.GraphRun, req *model.UpdateGraphRunVersionRequest, src Runner, instances map[string]model.GraphInstanceState) (*model.GraphRun, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	newCfg := cloneGraphConfig(req.Config)
	if _, err := s.normalizeWorkflowAgentReferences(ctx, &newCfg); err != nil {
		return nil, err
	}
	updateRevisionNodes := stringSet(req.UpdateAgentRevisionNodeIDs)
	if graphConfigNoOpEqual(effectiveConfig(run), newCfg) && len(updateRevisionNodes) == 0 {
		return run, nil
	}

	// Full static legality of the edited graph, then the incremental check
	// against persisted instance state.
	var verrs []model.GraphValidationError
	verrs = append(verrs, validateConfig(&newCfg)...)
	if instances == nil {
		loaded, err := s.runRepo.GetInstances(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("load instances failed: %w", err)
		}
		instances = loaded
	}
	verrs = append(verrs, validateVersionEdit(effectiveConfig(run), newCfg, instances)...)
	if len(verrs) > 0 {
		return nil, &ValidationError{Errors: verrs}
	}
	if len(updateRevisionNodes) > 0 {
		nextNodes := nodesByID(newCfg)
		for nodeID := range updateRevisionNodes {
			node, exists := nextNodes[nodeID]
			if !exists || !isAgent(node.Type) {
				return nil, fmt.Errorf(
					"update Agent revision failed: node %q does not exist or is not an Agent node",
					nodeID,
				)
			}
			for key, state := range instances {
				if state.NodeID == nodeID {
					return nil, fmt.Errorf(
						"update Agent revision failed: node %q instance %q already selected execution version %d with status %s",
						nodeID,
						key,
						state.Version,
						state.Status,
					)
				}
			}
		}
	}

	previousSnapshots := effectiveAgentSnapshots(run)
	oldNodes := nodesByID(effectiveConfig(run))
	nextNodes := nodesByID(newCfg)
	inheritedAgents := make(map[string]model.GraphAgentSnapshot)
	for nodeID, previous := range previousSnapshots {
		oldNode, oldExists := oldNodes[nodeID]
		newNode, newExists := nextNodes[nodeID]
		if !oldExists || !newExists ||
			oldNode.Config.AgentType != newNode.Config.AgentType ||
			updateRevisionNodes[nodeID] {
			continue
		}
		inheritedAgents[nodeID] = previous
	}
	models, agents, releaseAgentLeases, err := buildSnapshotContent(
		ctx,
		newCfg,
		src,
		inheritedAgents,
	)
	if err != nil {
		return nil, err
	}
	defer releaseAgentLeases()
	now := time.Now()
	newVersion := run.CurrentVersion + 1
	run.Versions = append(run.Versions, model.GraphRunVersion{
		Version:        newVersion,
		Config:         newCfg,
		ModelSnapshots: models,
		AgentSnapshots: agents,
		Reason:         req.Reason,
		CreatedAt:      now.UnixMilli(),
	})
	run.CurrentVersion = newVersion
	run.UpdatedAt = now
	return run, nil
}

func effectiveAgentSnapshots(run *model.GraphRun) map[string]model.GraphAgentSnapshot {
	if run == nil {
		return nil
	}
	for _, version := range run.Versions {
		if version.Version == run.CurrentVersion {
			return version.AgentSnapshots
		}
	}
	return run.BaseSnapshot.AgentSnapshots
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func graphConfigNoOpEqual(a, b model.GraphConfig) bool {
	return reflect.DeepEqual(normalizeGraphConfigForNoOp(a), normalizeGraphConfigForNoOp(b))
}

func normalizeGraphConfigForNoOp(in model.GraphConfig) model.GraphConfig {
	out := cloneGraphConfig(in)
	out.Canvas = model.GraphCanvasState{}
	out.Variables = normalizeGraphVariablesForNoOp(out.Variables)
	if len(out.Variables) == 0 {
		out.Variables = nil
	}
	if len(out.DisabledVars) == 0 {
		out.DisabledVars = nil
	}
	for i := range out.Nodes {
		if len(out.Nodes[i].Config.OutputVariables) == 0 {
			out.Nodes[i].Config.OutputVariables = nil
		}
		if len(out.Nodes[i].Metadata) == 0 {
			out.Nodes[i].Metadata = nil
		}
	}
	for i := range out.Edges {
		if len(out.Edges[i].Metadata) == 0 {
			out.Edges[i].Metadata = nil
		}
	}
	return out
}

func normalizeGraphVariablesForNoOp(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := cloneStringMap(in)
	for _, name := range []string{"Code", "Doc"} {
		if out[name] == "" {
			delete(out, name)
		}
	}
	return out
}

// executionConfigEqual reports whether two nodes carry identical execution
// configuration. Layout/title are display-only and may change freely even for a
// node whose instance already executed; everything that affects what the node
// *does* must stay fixed once that node has a reliable / in-flight instance.
func executionConfigEqual(a, b model.GraphNode) bool {
	if a.Type != b.Type || a.ParentID != b.ParentID {
		return false
	}
	ca, cb := a.Config, b.Config
	if ca.Script != cb.Script ||
		ca.Prompt != cb.Prompt ||
		ca.AgentType != cb.AgentType ||
		ca.ModelID != cb.ModelID ||
		ca.ACPMode != cb.ACPMode ||
		ca.ACPThoughtLevel != cb.ACPThoughtLevel ||
		ca.SessionStrategy != cb.SessionStrategy ||
		ca.LastAssistantAlias != cb.LastAssistantAlias ||
		ca.Condition != cb.Condition ||
		ca.LoopMode != cb.LoopMode ||
		ca.FixedCount != cb.FixedCount ||
		ca.UntilCondition != cb.UntilCondition ||
		ca.MaxIterations != cb.MaxIterations {
		return false
	}
	if !intPtrEqual(ca.TimeoutSeconds, cb.TimeoutSeconds) {
		return false
	}
	return stringSliceEqual(ca.OutputVariables, cb.OutputVariables)
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// loopFixedCountOnlyEdit reports whether the only execution-config difference
// between old and new is the loop FixedCount. It aligns new's FixedCount onto
// old and checks the rest is identical via executionConfigEqual, so any change
// to another field (LoopMode / UntilCondition / MaxIterations / …) makes it
// false and keeps the loop container frozen for that edit.
func loopFixedCountOnlyEdit(old, nn model.GraphNode) bool {
	probe := nn
	probe.Config.FixedCount = old.Config.FixedCount
	return executionConfigEqual(old, probe) && old.Config.FixedCount != nn.Config.FixedCount
}

// nodesByID indexes a config's nodes by ID.
func nodesByID(cfg model.GraphConfig) map[string]model.GraphNode {
	m := make(map[string]model.GraphNode, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		m[n.ID] = n
	}
	return m
}

// nodeInsideLoop reports whether node n lives inside a loop container — its
// ParentID chain reaches a node of type loop. Such nodes re-run each round, so
// editing their execution config mid-run is allowed (the edit takes effect on
// the next round). nodes maps node ID to node for the edited config.
func nodeInsideLoop(nodes map[string]model.GraphNode, n model.GraphNode) bool {
	for pid := n.ParentID; pid != ""; {
		parent, ok := nodes[pid]
		if !ok {
			return false
		}
		if parent.Type == model.GraphNodeTypeLoop {
			return true
		}
		pid = parent.ParentID
	}
	return false
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// validateVersionEdit checks an edited config against the persisted instance
// state (§4 不可编辑范围 + 校验要求). It returns located errors for:
//
//   - removing a node that already has a reliable (succeeded/skipped) or
//     in-flight (running) instance;
//   - changing the execution config of such a node;
//   - removing an edge whose source or target node already has a completed
//     (succeeded/skipped) instance — that would break a path a completed
//     instance depended on or fed.
//
// failed / interrupted / pending instances impose no constraint: they are
// resettable and will re-run against the new version on resume.
func validateVersionEdit(oldCfg model.GraphConfig, newCfg model.GraphConfig, instances map[string]model.GraphInstanceState) []model.GraphValidationError {
	oldNodes := nodesByID(oldCfg)
	newNodes := nodesByID(newCfg)
	newEdges := make(map[string]model.GraphEdge, len(newCfg.Edges))
	for _, e := range newCfg.Edges {
		newEdges[e.ID] = e
	}

	// Classify persisted instances: frozen (immutable config) vs completed
	// (their dependent edges must survive).
	frozenNodes := map[string]model.GraphInstanceStatus{}
	completedNodes := map[string]struct{}{}
	for _, st := range instances {
		switch st.Status {
		case model.GraphInstanceStatusSucceeded, model.GraphInstanceStatusSkipped:
			completedNodes[st.NodeID] = struct{}{}
			frozenNodes[st.NodeID] = st.Status
		case model.GraphInstanceStatusRunning:
			frozenNodes[st.NodeID] = st.Status
		}
	}

	var errs []model.GraphValidationError

	// Frozen nodes may not be deleted or have their execution config changed.
	// Exception (§4 循环内结点可改): a node inside a loop container re-runs each
	// round against the latest version, so editing its execution config is safe —
	// the in-flight round keeps its decided snapshot, the next round picks up the
	// edit. Such nodes are exempt from the config-immutability check (but still may
	// not be deleted, to keep the loop subgraph topology intact). The loop
	// container node itself stays frozen: changing its FixedCount / until rule
	// mid-run has no well-defined round boundary.
	for nodeID, status := range frozenNodes {
		nn, ok := newNodes[nodeID]
		if !ok {
			errs = append(errs, model.GraphValidationError{
				Type:    model.GraphValidationErrorTypeNode,
				NodeID:  nodeID,
				Message: fmt.Sprintf("cannot delete node %q: it has a %s instance in this run", nodeID, status),
			})
			continue
		}
		if nodeInsideLoop(newNodes, nn) {
			continue
		}
		if on, ok := oldNodes[nodeID]; ok && !executionConfigEqual(on, nn) {
			// Exception (§4 循环容器 FixedCount 可改): a loop container's FixedCount is
			// evaluated at each round boundary (finishIteration), so changing it
			// mid-run has a well-defined effect — the next round-end uses the new
			// count. The in-flight round keeps its decided snapshot; the scheduler
			// refreshes the active loop scope and corrects the progress denominator
			// on apply. Other loop-control fields (mode / until / max-iterations)
			// stay frozen — they have no clean mid-run round boundary.
			if nn.Type == model.GraphNodeTypeLoop && loopFixedCountOnlyEdit(on, nn) {
				continue
			}
			errs = append(errs, model.GraphValidationError{
				Type:    model.GraphValidationErrorTypeNode,
				NodeID:  nodeID,
				Message: fmt.Sprintf("cannot change execution config of node %q: it has a %s instance in this run", nodeID, status),
			})
		}
	}

	// Edges incident to a completed node may not be removed: a completed
	// instance either traversed an in-edge (path it depended on) or emits an
	// out-edge downstream depends on.
	for _, oe := range oldCfg.Edges {
		_, srcDone := completedNodes[oe.SourceNodeID]
		_, dstDone := completedNodes[oe.TargetNodeID]
		if !srcDone && !dstDone {
			continue
		}
		if _, ok := newEdges[oe.ID]; ok {
			continue
		}
		errs = append(errs, model.GraphValidationError{
			Type:    model.GraphValidationErrorTypeEdge,
			EdgeID:  oe.ID,
			Message: fmt.Sprintf("cannot remove edge %q (%s→%s): a completed instance depends on it", oe.ID, oe.SourceNodeID, oe.TargetNodeID),
		})
	}

	return errs
}
