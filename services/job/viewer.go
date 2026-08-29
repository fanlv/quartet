package job

import "sync"

// Viewer presence (§ 结束 Hook「无人查看才通知」): a viewer is a live UI event
// stream attached to a Job by a human — the chat page's SSE connection, or the
// graph run page's. Presence answers exactly one question, asked by
// fireEndHook: is somebody watching this Job right now, in which case an "it
// finished" notification is pure noise?
//
// Presence is deliberately NOT derived from the event buffer's reader count:
// the IM gateway subscribes to the same buffer as an internal consumer, and a
// public share page's reader is not the person the notification is for.
// Registration therefore happens only in the authenticated SSE handlers, which
// makes every non-UI subscriber invisible here by construction.
//
// A viewer that is connected but not actually on screen (a hidden browser tab,
// a minimized window) does not count: browsers keep an EventSource alive in
// background tabs, so a forgotten tab would otherwise mute notifications
// forever. Clients report that via SetViewerVisible. iOS needs no reporting —
// it tears the stream down when the view disappears or the app backgrounds.
//
// Presence is per-process in-memory state with no persistence: a restart drops
// every connection anyway.

// ViewerOptions describes a viewer being attached to a Job.
type ViewerOptions struct {
	// ViewerID is the client-generated per-connection id used by
	// SetViewerVisible to address this viewer later. Empty means the client
	// cannot report visibility (iOS, CLI); such a viewer counts as visible for
	// its whole lifetime, which is correct because those clients disconnect
	// instead of going hidden.
	ViewerID string
	// Visible is the viewer's initial on-screen state.
	Visible bool
	// Kind labels the stream ("chat" / "graph-run") for logs only.
	Kind string
}

// ViewerHandle is what an SSE handler holds for the lifetime of its
// connection. Detach is idempotent so a handler can defer it unconditionally.
type ViewerHandle interface {
	Detach()
}

type viewerEntry struct {
	jobID    string
	viewerID string
	kind     string
	visible  bool
}

// viewerRegistry tracks live viewers per Job. It owns its own mutex and never
// calls back into serviceImpl, so it can be read from the hook path without
// participating in the service's lock graph.
type viewerRegistry struct {
	mu sync.Mutex
	// entries are keyed by pointer identity, not by ViewerID: two connections
	// may legitimately carry the same (or an empty) ViewerID, and each must
	// detach independently.
	jobs map[string]map[*viewerEntry]struct{}
}

func newViewerRegistry() *viewerRegistry {
	return &viewerRegistry{jobs: make(map[string]map[*viewerEntry]struct{})}
}

func (r *viewerRegistry) attach(jobID string, opts ViewerOptions) *viewerEntry {
	entry := &viewerEntry{
		jobID:    jobID,
		viewerID: opts.ViewerID,
		kind:     opts.Kind,
		visible:  opts.Visible,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.jobs[jobID]
	if !ok {
		set = make(map[*viewerEntry]struct{})
		r.jobs[jobID] = set
	}
	set[entry] = struct{}{}
	return entry
}

func (r *viewerRegistry) detach(entry *viewerEntry) {
	if entry == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.jobs[entry.jobID]
	if !ok {
		return
	}
	delete(set, entry)
	if len(set) == 0 {
		delete(r.jobs, entry.jobID)
	}
}

// setVisible flips every viewer of jobID carrying viewerID. Returns the number
// of viewers updated so the HTTP layer can tell a real report from a stale one
// (a client whose stream has already been torn down).
func (r *viewerRegistry) setVisible(jobID, viewerID string, visible bool) int {
	if viewerID == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := 0
	for entry := range r.jobs[jobID] {
		if entry.viewerID != viewerID {
			continue
		}
		entry.visible = visible
		updated++
	}
	return updated
}

// watchedBy returns how many viewers of jobID are currently on screen.
func (r *viewerRegistry) watchedBy(jobID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for entry := range r.jobs[jobID] {
		if entry.visible {
			count++
		}
	}
	return count
}

// viewerHandle is the ViewerHandle implementation returned to SSE handlers.
type viewerHandle struct {
	registry *viewerRegistry
	once     sync.Once
	entry    *viewerEntry
}

func (h *viewerHandle) Detach() {
	if h == nil {
		return
	}
	h.once.Do(func() { h.registry.detach(h.entry) })
}

// AttachViewer registers a live UI event stream as a viewer of jobID and
// returns the handle the caller must Detach when the stream ends.
func (s *serviceImpl) AttachViewer(jobID string, opts ViewerOptions) ViewerHandle {
	return &viewerHandle{registry: s.viewers, entry: s.viewers.attach(jobID, opts)}
}

// SetViewerVisible updates a viewer's on-screen state. Returns false when no
// live viewer matches (unknown or already-detached viewerID) so the caller can
// treat the report as a no-op.
func (s *serviceImpl) SetViewerVisible(jobID, viewerID string, visible bool) bool {
	return s.viewers.setVisible(jobID, viewerID, visible) > 0
}

// WatchedBy returns the number of viewers currently watching jobID on screen.
func (s *serviceImpl) WatchedBy(jobID string) int {
	return s.viewers.watchedBy(jobID)
}
