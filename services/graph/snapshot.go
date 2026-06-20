package graph

import "sort"

// Visible variable snapshot merging for joins (§3 可见性 / join 合并).
//
// The only source of read truth at run time is each execution instance's
// visible variable snapshot. A node with one in-edge inherits its upstream's
// post-completion snapshot; a node with multiple in-edges (a join) merges the
// snapshots of its ACTIVATED upstreams once all in-edges are resolved (pruned
// upstreams do not participate).
//
// This file provides the pure merge function; WHEN the scheduler calls it and
// how it serializes the write to disk belong to the scheduler (steps 10/11).

// UpstreamSnapshot is one activated upstream's contribution to a join.
type UpstreamSnapshot struct {
	// NodeID is the upstream node's stable ID. Used to break ties for
	// _last_assistant_msg deterministically (ascending node ID, last wins).
	NodeID string
	// Variables is the upstream instance's visible variable snapshot, including
	// any named outputs and aliases it wrote.
	Variables map[string]string
	// LastAssistantMsg is the upstream instance's raw final output
	// (_last_assistant_msg). Empty string is a valid value.
	LastAssistantMsg string
	// SessionID is the upstream instance's outflow session (§3 会话血缘): the
	// session an inheriting downstream Agent forks from. Empty when the upstream
	// chain has not yet created any session (e.g. before the first Agent node).
	SessionID string
}

const lastAssistantKey = reservedLastAssistant

// MergeVisibleSnapshots merges the visible variable snapshots of all activated
// upstreams into the snapshot a join instance will see.
//
// Rules (§3):
//   - union of all upstream variables; different upstreams writing different
//     variables are unioned. Same-name writes by potentially-parallel nodes are
//     forbidden at save time (§1), so a deterministic tie-break (ascending node
//     ID, last wins) is only a defensive fallback.
//   - _last_assistant_msg is taken from the activated upstream with the
//     greatest node ID (ascending sort, last position), independent of arrival
//     order — guaranteeing the same value across reruns and crash recovery.
//
// Passing a single upstream degenerates to "inherit upstream snapshot".
// Pruned upstreams must be excluded by the caller (not passed in).
func MergeVisibleSnapshots(upstreams []UpstreamSnapshot) map[string]string {
	merged := make(map[string]string)
	if len(upstreams) == 0 {
		return merged
	}

	// Sort by NodeID ascending so both variable tie-breaks and the
	// _last_assistant_msg pick are deterministic and replayable.
	sorted := make([]UpstreamSnapshot, len(upstreams))
	copy(sorted, upstreams)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NodeID < sorted[j].NodeID })

	for _, up := range sorted {
		for k, v := range up.Variables {
			if k == lastAssistantKey {
				// _last_assistant_msg is resolved separately below; never let a
				// snapshot's own copy of it win the union by arrival.
				continue
			}
			merged[k] = v
		}
	}

	// _last_assistant_msg: last position after ascending sort = greatest node ID.
	merged[lastAssistantKey] = sorted[len(sorted)-1].LastAssistantMsg
	return merged
}

// pickInflowSession resolves the session an inheriting downstream Agent forks
// from when it has multiple activated in-edges (a join) — the same deterministic
// rule as _last_assistant_msg: the activated upstream with the greatest node ID
// (ascending sort, last wins), independent of arrival order, so the choice is
// stable across reruns and crash recovery. Upstreams without a session (empty
// SessionID) are skipped so an Agent join inherits from the nearest upstream
// that actually established a session. A multi-in-edge Agent MAY declare
// `inherit` (it forks this greatest-node-ID upstream session); the first-Agent
// save-time rule still guarantees every in-edge path carries an upstream Agent
// session. A single upstream degenerates to "inherit upstream session".
func pickInflowSession(upstreams []UpstreamSnapshot) string {
	chosen := ""
	chosenNode := ""
	for _, up := range upstreams {
		if up.SessionID == "" {
			continue
		}
		if chosen == "" || up.NodeID > chosenNode {
			chosen = up.SessionID
			chosenNode = up.NodeID
		}
	}
	return chosen
}
