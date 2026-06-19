package graph

import (
	"context"

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
// Loops: any active (incomplete) loop touched by a reset is reset WHOLESALE —
// the container instance and every instance under it are dropped and the
// container re-runs from scratch when its (kept) main-scope in-edges re-resolve.
// This trades re-running already-completed sibling iterations (safe under the
// accepted "重复副作用" risk for resume) for avoiding fragile mid-round scope
// reconstruction. Completed loops (succeeded container) are kept entirely.
type resumeBuilder struct {
	cfg       model.GraphConfig
	instances map[string]model.GraphInstanceState
	edges     map[string]model.GraphEdgeState
	vars      map[string]map[string]string
}

func newResumeBuilder(cfg model.GraphConfig, instances map[string]model.GraphInstanceState, edges map[string]model.GraphEdgeState, vars map[string]map[string]string) *resumeBuilder {
	return &resumeBuilder{cfg: cfg, instances: instances, edges: edges, vars: vars}
}

// isResettableStatus reports whether an instance state is a reset start (its
// output is unreliable or it was interrupted).
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

// computeResetSet returns the instance-key strings to reset: directly
// resettable instances, plus — if any reset instance lives inside a loop — the
// entire top-level loop container and everything beneath it.
func (rb *resumeBuilder) computeResetSet() map[string]struct{} {
	reset := map[string]struct{}{}
	loopContainers := map[string]struct{}{}
	for keyStr, st := range rb.instances {
		if isResettableStatus(st.Status) {
			reset[keyStr] = struct{}{}
			if len(st.Key.Iterations) > 0 {
				loopContainers[st.Key.Iterations[0].LoopNodeID] = struct{}{}
			}
		}
	}
	if len(loopContainers) == 0 {
		return reset
	}
	// Expand: every instance inside a touched top-level loop, and the loop
	// container instances themselves (which live in the main scope).
	for keyStr, st := range rb.instances {
		if len(st.Key.Iterations) > 0 {
			if _, ok := loopContainers[st.Key.Iterations[0].LoopNodeID]; ok {
				reset[keyStr] = struct{}{}
			}
		}
	}
	for loopNodeID := range loopContainers {
		reset[instanceKeyString(model.GraphInstanceKey{NodeID: loopNodeID})] = struct{}{}
	}
	return reset
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
			srcKeyStr := instanceKeyString(es.SourceInstanceKey)
			srcVars := sc.varsByKey[srcKeyStr]
			srcSession := ""
			if srcState, ok := sc.instances[srcKeyStr]; ok {
				srcSession = srcState.SessionID
			}
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
	// skips succeeded/skipped instances. All such frontier targets are in the
	// main scope (loop internals were reset wholesale or kept-as-survivor).
	type frontierItem struct {
		node          model.GraphNode
		key           model.GraphInstanceKey
		visible       map[string]string
		inflowSession string
	}
	var frontier []frontierItem
	for targetKeyStr, rem := range sc.inRemaining {
		if rem > 0 {
			continue
		}
		nodeID := nodeIDFromKeyString(targetKeyStr, sc.instances)
		node, ok := sc.nodesByID[nodeID]
		if !ok {
			continue
		}
		key := model.GraphInstanceKey{NodeID: nodeID}
		frontier = append(frontier, frontierItem{
			node:          node,
			key:           key,
			visible:       MergeVisibleSnapshots(sc.contribs[targetKeyStr]),
			inflowSession: pickInflowSession(sc.contribs[targetKeyStr]),
		})
	}
	for _, fi := range frontier {
		sc.decide(ctx, sc.mainScope, fi.node, fi.key, fi.visible, fi.inflowSession)
		if sc.failed {
			return nil
		}
	}
	return nil
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
