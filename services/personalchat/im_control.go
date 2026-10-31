package personalchat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// IMControl commits its routing decision and receipt together. Re-delivered
// /new messages cannot rotate a task twice after another message has arrived.
func (s *Service) IMControl(id, command string, source model.PersonalChatSource) (string, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", fmt.Errorf("IM control command is required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", fmt.Errorf("personal chat is shutting down")
	}
	if old, ok := s.messages[id]; ok {
		s.mu.Unlock()
		if old.RawContent != command {
			return "", fmt.Errorf("IM control message %s changed after acceptance", id)
		}
		return old.Content, nil
	}
	task := s.tasks["default-main"]
	if task.ID == "" {
		task = model.PersonalChatTask{ID: "default-main", AgentID: "main", Title: s.agents["main"].Name, CreatedAt: time.Now().UnixMilli()}
	}
	var cancel context.CancelFunc
	var c model.PersonalChatCommit
	var text string
	switch strings.ToLower(fields[0]) {
	case "/new":
		task.RotateOnNextMessage = true
		task.AwaitingInput = false
		task.WaitReason = ""
		text = "后续消息将使用新的 Session。已有执行和队列保持原顺序。"
		if task.Paused {
			text += " 队列已暂停，可发送 /resume 继续。"
		}
	case "/stop":
		task.Paused = true
		text = "已请求停止当前执行，后续队列已暂停。"
		for _, id := range task.Queue {
			if fn := s.active[id]; fn != nil {
				r := s.runs[id]
				r.State = "stopping"
				c.Runs = append(c.Runs, r)
				cancel = fn
				for _, mid := range []string{r.MessageID, r.ReplyID} {
					m := s.messages[mid]
					m.State = "stopping"
					c.Messages = append(c.Messages, m)
				}
				break
			}
		}
	case "/resume":
		task.Paused = false
		task.AwaitingInput = false
		task.WaitReason = ""
		text = "已恢复主对话队列。"
	case "/status":
		text = fmt.Sprintf("主 Agent：队列 %d 条，暂停=%t，等待回复=%t，Session=%s", len(task.Queue), task.Paused, task.AwaitingInput, task.SessionID)
	case "/help":
		text = "直接发送消息与 Web、iOS 共用主 Agent 对话。\n/new：后续消息使用新 Session\n/stop：停止并暂停队列\n/resume：继续队列\n/status：查看当前状态"
	default:
		s.mu.Unlock()
		return "", fmt.Errorf("unsupported personal chat control %q", command)
	}
	c.Tasks = []model.PersonalChatTask{task}
	c.Messages = append(c.Messages, model.PersonalChatMessage{ID: id, Order: (s.seq + 1) * 2, Role: "notification", Content: text, RawContent: command, State: "completed", Source: source, CreatedAt: time.Now().UnixMilli()})
	err := s.commitLocked(c)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if cancel != nil {
		cancel()
	}
	s.kick()
	return text, nil
}
