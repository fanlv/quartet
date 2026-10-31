package personalchat

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"github.com/fanlv/quartet/types/model"
)

// ToolCredential is process-local and never enters message history or API snapshots.
func (s *Service) ToolCredential(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return "", fmt.Errorf("session %s not found", sessionID)
	}
	if s.toolTokens == nil {
		s.toolTokens = map[string]string{}
	}
	if token := s.toolTokens[sessionID]; token != "" {
		return token, nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	s.toolTokens[sessionID] = token
	return token, nil
}

func (s *Service) AuthenticateTool(sessionID, token string) (model.PersonalChatRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expected := s.toolTokens[sessionID]
	if expected == "" || token == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return model.PersonalChatRun{}, fmt.Errorf("invalid personal chat tool credential")
	}
	for id := range s.active {
		r := s.runs[id]
		if r.SessionID == sessionID && r.State == "running" {
			return r, nil
		}
	}
	return model.PersonalChatRun{}, fmt.Errorf("no active execution owns this session's tool credential")
}

// TaskForSource resolves explicit platform references, never the last sender.
func (s *Service) TaskForSource(platform, account, chat string, references ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, reference := range references {
		if reference == "" {
			continue
		}
		for _, m := range s.messages {
			if m.Source.Platform == platform && m.Source.AccountID == account && m.Source.ChatID == chat && m.Source.MessageID == reference {
				return m.TaskID
			}
		}
	}
	return ""
}

// AwaitInput suspends background dispatch while an Agent needs a user answer.
func (s *Service) AwaitInput(runID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || run.State != "running" {
		return fmt.Errorf("execution %s is not running", runID)
	}
	task := s.tasks[run.TaskID]
	task.AwaitingInput = true
	for _, id := range task.Queue {
		if id != runID && !s.messages[s.runs[id].MessageID].Internal {
			task.AwaitingInput = false
			break
		}
	}
	task.WaitReason = reason
	return s.commitLocked(model.PersonalChatCommit{Tasks: []model.PersonalChatTask{task}})
}

func (s *Service) RecordHookError(runID string, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return
	}
	run.HookError = err.Error()
	if saveErr := s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{run}}); saveErr != nil {
		s.warnings = append(s.warnings, saveErr.Error())
	}
}

func (s *Service) AdvanceDelivery(runID string, nextPart int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return fmt.Errorf("execution %s not found", runID)
	}
	if nextPart <= run.DeliveryNextPart {
		return nil
	}
	run.DeliveryNextPart = nextPart
	return s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{run}})
}
