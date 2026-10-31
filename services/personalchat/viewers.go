package personalchat

import "github.com/google/uuid"

type taskViewer struct{ taskID, lease string }

// WatchTask counts only the selected task, rather than every background task
// visible somewhere in the shared timeline. Reconnects replace their old lease.
func (s *Service) WatchTask(viewerID, taskID string) func() {
	if viewerID == "" || taskID == "" {
		return func() {}
	}
	s.mu.Lock()
	if s.viewers == nil {
		s.viewers = map[string]taskViewer{}
	}
	lease := uuid.NewString()
	s.viewers[viewerID] = taskViewer{taskID, lease}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if current, ok := s.viewers[viewerID]; ok && current.lease == lease {
			delete(s.viewers, viewerID)
		}
	}
}
func (s *Service) taskWatchedLocked(taskID string) bool {
	for _, v := range s.viewers {
		if v.taskID == taskID {
			return true
		}
	}
	return false
}
