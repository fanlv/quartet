package model

// CodexAccount contains display information only. Credential files never leave
// the backend through the management API.
type CodexAccount struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Note        string `json:"note"`
	AuthType    string `json:"auth_type"`
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	HasConfig   bool   `json:"has_config"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	LastUsedAt  int64  `json:"last_used_at,omitempty"`
}

type CodexAccountCurrent struct {
	Availability    string        `json:"availability"`
	ValidationError string        `json:"validation_error,omitempty"`
	ValidatedAt     int64         `json:"validated_at,omitempty"`
	Home            string        `json:"home"`
	Account         *CodexAccount `json:"account,omitempty"`
	ManagedID       string        `json:"managed_id,omitempty"`
	Status          string        `json:"status"`
	HasAuth         bool          `json:"has_auth"`
	HasConfig       bool          `json:"has_config"`
	ConfigApplied   bool          `json:"config_applied"`
	Error           string        `json:"error,omitempty"`
	BlockedReason   string        `json:"blocked_reason,omitempty"`
}

type CodexAccountOperation struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	AccountID  string `json:"account_id,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	FinishedAt int64  `json:"finished_at"`
}

type CodexAccountResponse struct {
	Code         int                     `json:"code"`
	Revision     string                  `json:"revision"`
	LiveRevision string                  `json:"live_revision"`
	Accounts     []CodexAccount          `json:"accounts"`
	Current      CodexAccountCurrent     `json:"current"`
	HasBackup    bool                    `json:"has_backup"`
	Operations   []CodexAccountOperation `json:"operations"`
	ActiveLogin  *CodexAccountLogin      `json:"active_login,omitempty"`
	Warning      string                  `json:"warning,omitempty"`
}

// File contents are accepted only on writes. Pointers distinguish omitted
// files from empty uploads and allow updating just one member of a profile.
type CodexAccountMutation struct {
	Revision     string  `json:"revision"`
	LiveRevision string  `json:"live_revision"`
	OperationID  string  `json:"operation_id"`
	Name         string  `json:"name"`
	Note         string  `json:"note"`
	AuthJSON     *string `json:"auth_json,omitempty"`
	ConfigTOML   *string `json:"config_toml,omitempty"`
	RemoveConfig bool    `json:"remove_config,omitempty"`
	ApplyConfig  bool    `json:"apply_config,omitempty"`
}

type CodexAccountLogin struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

type CodexAccountLoginResponse struct {
	Code  int               `json:"code"`
	Login CodexAccountLogin `json:"login"`
}
