package model

// AgentAuthMethod is the authentication surface advertised by an ACP agent.
// Credentials remain owned by the agent, or by the existing ACP environment
// settings; Quartet does not maintain a second credential store.
type AgentAuthMethod struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Command     string   `json:"command,omitempty"`
	Environment []string `json:"environment,omitempty"`
}

type AgentAuthRequest struct {
	Revision string `json:"revision"`
	MethodID string `json:"method_id"`
}

type AgentAuthLink struct {
	URL     string `json:"url"`
	Message string `json:"message,omitempty"`
}

type AgentAuthAttempt struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	Revision   string          `json:"revision"`
	MethodID   string          `json:"method_id"`
	Status     string          `json:"status"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	Links      []AgentAuthLink `json:"links,omitempty"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt int64           `json:"finished_at,omitempty"`
}

type AgentAuthResponse struct {
	Code     int               `json:"code"`
	AgentID  string            `json:"agent_id"`
	Revision string            `json:"revision"`
	Methods  []AgentAuthMethod `json:"methods"`
	Attempt  *AgentAuthAttempt `json:"attempt,omitempty"`
}
