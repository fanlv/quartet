package personalchat

import (
	"fmt"
	"os"

	"github.com/fanlv/quartet/types/model"
)

// NewMaintenanceSession gives background summarization its own ACP state and
// transcript, excluded from user history and recursive summary generation.
func (s *Service) NewMaintenanceSession(binding model.AgentRuntimeBinding, modelID, thought string) (*model.Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	meta := *model.NewSession()
	meta.AgentID = binding.AgentID
	meta.AgentRevision = binding.Revision
	meta.AgentRuntimeKey = binding.RuntimeKey
	meta.AgentDefinition = binding.Definition
	meta.Type = binding.RuntimeKey
	meta.ModelID = modelID
	meta.ACPThoughtLevel = thought
	meta.Workdir = home
	meta.JobID = "summary-" + meta.ID
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("personal chat is shutting down")
	}
	err = s.commitLocked(model.PersonalChatCommit{Sessions: []model.PersonalChatSession{{ID: meta.ID, HarnessID: binding.AgentID, Maintenance: true, Meta: meta}}})
	return &meta, err
}
