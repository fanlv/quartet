package model

const AgentRoleSettingsVersion = 1

// AgentRoleConfig identifies an Agent used by a one-shot role. The runtime
// definition is resolved from the Agent catalog when the role is executed.
// ModelID/ACPThoughtLevel are honored by headless agents that accept them
// (currently eino-cli's `-p --model/--thought`); empty values fall back to the
// agent's own defaults.
type AgentRoleConfig struct {
	AgentID         string `json:"agent_id"`
	ModelID         string `json:"model_id,omitempty"`
	ACPThoughtLevel string `json:"acp_thought_level,omitempty"`
}

// IMSessionAgentConfig is the ACP configuration used when an IM private chat
// creates or continues an interactive Job.
type IMSessionAgentConfig struct {
	AgentID         string `json:"agent_id"`
	ModelID         string `json:"model_id,omitempty"`
	ACPMode         string `json:"acp_mode,omitempty"`
	ACPThoughtLevel string `json:"acp_thought_level,omitempty"`
}
