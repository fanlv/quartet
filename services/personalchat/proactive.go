package personalchat

import "github.com/fanlv/quartet/types/model"

// RecordProactive projects delivery progress into the home without invoking an
// Agent. The existing durable IM outbox still owns sending and retrying.
func (s *Service) RecordProactive(task model.WeChatOutboxTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := "outbox-" + task.ID
	state := string(task.Status)
	switch task.Status {
	case model.WeChatOutboxStatusSent:
		state = "completed"
	case model.WeChatOutboxStatusSending:
		state = "running"
	}
	old := s.messages[id]
	if old.ID != "" && old.Content == task.Content && old.State == state && old.Error == task.LastError {
		return nil
	}
	m := model.PersonalChatMessage{ID: id, Order: old.Order, Role: "notification", Content: task.Content, State: state, Error: task.LastError, Source: model.PersonalChatSource{Platform: "wechat", ChatID: task.ToUserID}, CreatedAt: task.CreatedAt.UnixMilli()}
	if m.Order == 0 {
		m.Order = (s.seq + 1) * 2
	}
	return s.commitLocked(model.PersonalChatCommit{Messages: []model.PersonalChatMessage{m}})
}
