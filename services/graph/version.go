package graph

import (
	"context"
	"errors"
	"fmt"
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

// UpdateRunVersion validates an edit against the persisted run state and, on
// success, appends a new graph version (with frozen referenced Agent/model
// content) and advances CurrentVersion. In-flight edits are routed through the
// scheduler so its in-memory run state cannot overwrite the newly appended
// version on the next persist.
func (s *serviceImpl) UpdateRunVersion(ctx context.Context, runID string, req *model.UpdateGraphRunVersionRequest, src Runner) (*model.GraphRun, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, ErrGraphRunNotFound
	}
	if isInFlightStatus(run.Status) {
		return s.updateRunVersionInFlight(ctx, runID, req, src)
	}

	return s.appendRunVersion(ctx, run, req, src, nil)
}

func (s *serviceImpl) updateRunVersionInFlight(ctx context.Context, runID string, req *model.UpdateGraphRunVersionRequest, src Runner) (*model.GraphRun, error) {
	resp := make(chan versionUpdateResult, 1)
	sig := controlSignal{
		kind:          ctrlUpdateVersion,
		versionReq:    req,
		versionRunner: src,
		versionResp:   resp,
	}
	if err := s.sendControl(runID, sig); err != nil {
		if errors.Is(err, ErrGraphRunNotRunning) {
			run, getErr := s.runRepo.GetRun(ctx, runID)
			if getErr != nil {
				return nil, ErrGraphRunNotFound
			}
			return nil, fmt.Errorf("%w: status=%s", ErrGraphRunNotEditable, run.Status)
		}
		return nil, err
	}
	select {
	case result := <-resp:
		return result.run, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *serviceImpl) appendRunVersion(ctx context.Context, run *model.GraphRun, req *model.UpdateGraphRunVersionRequest, src Runner, instances map[string]model.GraphInstanceState) (*model.GraphRun, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	newCfg := cloneGraphConfig(req.Config)

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

	models, agents := buildSnapshotContent(ctx, newCfg, src)
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
	if err := s.runRepo.SaveRun(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
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
	oldNodes := make(map[string]model.GraphNode, len(oldCfg.Nodes))
	for _, n := range oldCfg.Nodes {
		oldNodes[n.ID] = n
	}
	newNodes := make(map[string]model.GraphNode, len(newCfg.Nodes))
	for _, n := range newCfg.Nodes {
		newNodes[n.ID] = n
	}
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
		if on, ok := oldNodes[nodeID]; ok && !executionConfigEqual(on, nn) {
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
