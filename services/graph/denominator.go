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

// denomAdjust adds delta (negative to reclaim) to the progress denominator,
// clamping so it never drops below the count of instances that will still be
// reflected in the denominator once resolved. Skipped instances are excluded
// from the floor because they are themselves reclaimed out of the denominator.
func (sc *scheduler) denomAdjust(delta int) {
	if delta == 0 || sc.run.Progress == nil {
		return
	}
	resolved := sc.run.Progress.CompletedCount + sc.run.Progress.FailedCount +
		sc.run.Progress.InterruptedCount
	total := sc.run.Progress.TotalCount + delta
	if total < resolved {
		total = resolved
	}
	sc.run.Progress.TotalCount = total
}

// loopSubgraphBusinessCount returns how many business instances one round of the
// given loop container's subgraph contributes — counting a node once times the
// product of the max-round bounds of any nested loops between it and this
// container (so a nested loop's body is counted at its own static bound).
func (sc *scheduler) loopSubgraphBusinessCount(containerID string) int {
	total := 0
	for _, n := range sc.cfg.Nodes {
		if !isBusiness(n.Type) {
			continue
		}
		if !sc.isDescendantOf(n, containerID) {
			continue
		}
		total += sc.instancesPerRound(n, containerID)
	}
	return total
}

// isDescendantOf reports whether node n is inside the container (its ParentID
// chain reaches containerID).
func (sc *scheduler) isDescendantOf(n model.GraphNode, containerID string) bool {
	for pid := n.ParentID; pid != ""; {
		if pid == containerID {
			return true
		}
		parent, ok := sc.nodesByID[pid]
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
func (sc *scheduler) instancesPerRound(n model.GraphNode, containerID string) int {
	prod := 1
	for pid := n.ParentID; pid != "" && pid != containerID; {
		loop, ok := sc.nodesByID[pid]
		if !ok {
			break
		}
		prod *= loopMaxRounds(sc.cfg.RunConfig, loop)
		pid = loop.ParentID
	}
	return prod
}
