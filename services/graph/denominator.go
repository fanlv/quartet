package graph

import "github.com/fanlv/quartet/types/model"

// Progress denominator runtime recalculation (§4 分母回算).
//
// initialGraphProgress (runtime.go) seeds TotalCount with a static upper bound:
// every business node contributes nodeMaxInstances = product of its ancestor
// loops' max rounds. As the run proves future instances will never materialize
// (a loop runs fewer rounds than its static bound, or a loop container is
// pruned outright), denomAdjust reclaims the difference so that on natural
// completion completed == total.
//
// Created-then-pruned instances are ALSO reclaimed here: a node that
// materializes as skipped (an untaken If-Else branch, an all-in-edges-pruned
// node) will never "complete", so pruneNode drops it from the denominator with
// denomAdjust(-1) rather than leaving it in the numerator. This keeps the
// progress bar at 100% on natural completion (completed == total) and counts
// the skipped instances only in their own SkippedCount.

// adjustDenomTotal adds delta (negative to reclaim) to a progress denominator,
// clamping so it never drops below the count of instances that will still be
// reflected in the denominator once resolved. Skipped instances are excluded
// from the floor because they are themselves reclaimed out of the denominator.
// Shared by the live scheduler (denomAdjust) and the static version-edit path
// (a mid-run FixedCount change adjusts the denominator off the scheduler too).
func adjustDenomTotal(p *model.GraphProgress, delta int) {
	if delta == 0 || p == nil {
		return
	}
	resolved := p.CompletedCount + p.FailedCount + p.InterruptedCount
	p.TotalCount = max(p.TotalCount+delta, resolved)
}

// denomAdjust applies adjustDenomTotal to the live run's progress.
func (sc *scheduler) denomAdjust(delta int) {
	adjustDenomTotal(sc.run.Progress, delta)
}

// loopSubgraphBusinessCount returns how many business instances one round of the
// given loop container's subgraph contributes — counting a node once times the
// product of the max-round bounds of any nested loops between it and this
// container (so a nested loop's body is counted at its own static bound).
// nodesByID maps node ID to node for the config the count is computed against.
func loopSubgraphBusinessCount(nodesByID map[string]model.GraphNode, rc model.GraphRunConfig, containerID string) int {
	total := 0
	for _, n := range nodesByID {
		if !isBusiness(n.Type) {
			continue
		}
		if !isDescendantOf(nodesByID, n, containerID) {
			continue
		}
		total += instancesPerRound(nodesByID, rc, n, containerID)
	}
	return total
}

// isDescendantOf reports whether node n is inside the container (its ParentID
// chain reaches containerID).
func isDescendantOf(nodesByID map[string]model.GraphNode, n model.GraphNode, containerID string) bool {
	for pid := n.ParentID; pid != ""; {
		if pid == containerID {
			return true
		}
		parent, ok := nodesByID[pid]
		if !ok {
			return false
		}
		pid = parent.ParentID
	}
	return false
}

// instancesPerRound returns how many instances of node n exist per single round
// of the given container: the product of max-round bounds of every loop ancestor
// strictly between n and the container (exclusive of the container itself).
func instancesPerRound(nodesByID map[string]model.GraphNode, rc model.GraphRunConfig, n model.GraphNode, containerID string) int {
	prod := 1
	for pid := n.ParentID; pid != "" && pid != containerID; {
		loop, ok := nodesByID[pid]
		if !ok {
			break
		}
		prod *= loopMaxRounds(rc, loop)
		pid = loop.ParentID
	}
	return prod
}

// loopSubgraphBusinessCount is the scheduler-bound thin wrapper over the free
// function, using the scheduler's indexed nodes and run config.
func (sc *scheduler) loopSubgraphBusinessCount(containerID string) int {
	return loopSubgraphBusinessCount(sc.nodesByID, sc.cfg.RunConfig, containerID)
}
