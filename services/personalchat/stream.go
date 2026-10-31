package personalchat

import (
	"sync"
	"time"

	"github.com/fanlv/quartet/types/model"
)

type streamHandler struct {
	mu       sync.Mutex
	s        *Service
	run      model.PersonalChatRun
	message  model.PersonalChatMessage
	last     time.Time
	started  bool
	err      error
	reported error
	dirty    bool
}

func (h *streamHandler) update(fn func(*model.PersonalChatMessage)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started {
		h.s.mu.Lock()
		h.message = h.s.messages[h.run.ReplyID]
		h.s.mu.Unlock()
		h.started = true
	}
	fn(&h.message)
	h.dirty = true
	return h.flushLocked(false)
}
func (h *streamHandler) flush(force bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started {
		return h.err
	}
	return h.flushLocked(force)
}
func (h *streamHandler) flushLocked(force bool) error {
	if h.err != nil || !h.dirty {
		return h.err
	}
	if !force && time.Since(h.last) < 250*time.Millisecond {
		return nil
	}
	h.last = time.Now()
	h.err = h.s.UpdateMessage(h.message)
	if h.err == nil {
		h.dirty = false
	}
	return h.err
}
func (h *streamHandler) OnMessageStart() error {
	return h.update(func(m *model.PersonalChatMessage) {
		if m.Content != "" {
			m.Content += "\n\n"
		}
	})
}
func (h *streamHandler) OnMessageDelta(v string) error {
	return h.update(func(m *model.PersonalChatMessage) { m.Content += v })
}
func (h *streamHandler) OnMessageEnd() error   { return h.flush(true) }
func (h *streamHandler) LastMessageID() string { return h.run.ReplyID }
func (h *streamHandler) OnThoughtStart() error {
	return h.update(func(m *model.PersonalChatMessage) {
		if m.Thought != "" {
			m.Thought += "\n\n"
		}
	})
}
func (h *streamHandler) OnThoughtDelta(v string) error {
	return h.update(func(m *model.PersonalChatMessage) { m.Thought += v })
}
func (h *streamHandler) OnThoughtEnd() error { return h.flush(true) }
func (h *streamHandler) OnToolCallStart(id, name string) error {
	return h.update(func(m *model.PersonalChatMessage) {
		m.Tools = append(m.Tools, model.PersonalChatTool{ID: id, Name: name, Status: "running"})
	})
}
func (h *streamHandler) OnToolCallArgs(id, args string, replace bool) error {
	return h.update(func(m *model.PersonalChatMessage) {
		for i := range m.Tools {
			if m.Tools[i].ID == id {
				if replace {
					m.Tools[i].Args = args
				} else {
					m.Tools[i].Args += args
				}
			}
		}
	})
}
func (h *streamHandler) OnToolCallResult(id, content string, success bool) error {
	return h.update(func(m *model.PersonalChatMessage) {
		for i := range m.Tools {
			if m.Tools[i].ID == id {
				m.Tools[i].Result = content
				if success {
					m.Tools[i].Status = "completed"
				} else {
					m.Tools[i].Status = "failed"
				}
			}
		}
	})
}
func (h *streamHandler) OnToolCallEnd(id string, success bool) error { return h.flush(true) }
func (h *streamHandler) OnToolCallInterrupted(id, reason string) error {
	return h.update(func(m *model.PersonalChatMessage) {
		for i := range m.Tools {
			if m.Tools[i].ID == id {
				m.Tools[i].Status = reason
			}
		}
	})
}
func (h *streamHandler) OnToolCallStitched(id, content string, success bool, ago int64) error {
	return h.OnToolCallResult(id, content, success)
}
func (h *streamHandler) OnTokenUsage(tokens int, estimated bool) error { return nil }
func (h *streamHandler) OnError(err error) {
	h.mu.Lock()
	h.reported = err
	h.mu.Unlock()
	h.update(func(m *model.PersonalChatMessage) { m.Error = err.Error() })
}
