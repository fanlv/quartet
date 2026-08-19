package model

// AgentVersionComponent is one versioned part of an Agent installation. NPM
// components carry both current and latest versions; binary components may
// only expose a current local version when no authoritative remote version
// source exists.
type AgentVersionComponent struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"` // "npm" | "binary"
	CurrentVersion  string `json:"current_version,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Error           string `json:"error,omitempty"`
}

// AgentVersionInfo is the aggregate version state for one installed Agent.
// UpgradeSupported means the built-in catalog has a controlled preset flow;
// it never permits the client to supply an arbitrary command.
type AgentVersionInfo struct {
	AgentID          string                  `json:"agent_id"`
	Components       []AgentVersionComponent `json:"components"`
	UpdateAvailable  bool                    `json:"update_available"`
	UpgradeSupported bool                    `json:"upgrade_supported"`
}

type AgentVersionCheckResponse struct {
	Code      int                `json:"code"`
	CheckedAt int64              `json:"checked_at"`
	Agents    []AgentVersionInfo `json:"agents"`
}
