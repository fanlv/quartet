package model

import "encoding/json"

// ACPEnvVarEntry is one persisted environment-variable setting for an ACP
// Agent. It lives in the shared model layer because both the HTTP API and the
// settings service exchange this value; repository only owns its persistence.
type ACPEnvVarEntry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// AgentPrefs holds per-Agent UI defaults edited in Settings.
type AgentPrefs struct {
	FavoriteModelIDs    []string `json:"favorite_model_ids,omitempty"`
	DefaultModelID      string   `json:"default_model_id,omitempty"`
	DefaultMode         string   `json:"default_mode,omitempty"`
	DefaultThoughtLevel string   `json:"default_thought_level,omitempty"`
}

// Settings is the shared settings contract used by the API and config service.
// The repository persists this model but does not own its schema.
type Settings struct {
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`

	TitleGenerationAgent *AgentRoleConfig      `json:"title_generation_agent,omitempty"`
	GroupReplyAgent      *AgentRoleConfig      `json:"group_reply_agent,omitempty"`
	IMSessionAgent       *IMSessionAgentConfig `json:"im_session_agent,omitempty"`
	ACPEnvVars           map[string][]ACPEnvVarEntry `json:"acp_env_vars,omitempty"`
	ACPEnvVersions       map[string]int64            `json:"acp_env_versions,omitempty"`
	AgentPrefs           map[string]AgentPrefs       `json:"agent_prefs,omitempty"`
	LarkAppID            string                      `json:"lark_app_id,omitempty"`
	LarkAppSecret        string                      `json:"lark_app_secret,omitempty"`
	LarkIMAdminSenderID  string                      `json:"lark_im_admin_sender_id,omitempty"`
	LarkIMSophiaSenderID string                      `json:"lark_im_sophia_sender_id,omitempty"`
	IMWorkspaceID        string                      `json:"im_workspace_id,omitempty"`
	WeChatAdminIDs       []string                    `json:"wechat_admin_ids,omitempty"`
	EndHookScript        string                      `json:"end_hook_script,omitempty"`
	EndHookSkipWhenWatched *bool                     `json:"end_hook_skip_when_watched,omitempty"`
}

// EndHookSkipsWatchedJob reports whether an interactive end hook should stay
// quiet while the Job has a visible live viewer. Missing config keeps the
// historical default of suppressing the hook.
func (s *Settings) EndHookSkipsWatchedJob() bool {
	if s == nil || s.EndHookSkipWhenWatched == nil {
		return true
	}
	return *s.EndHookSkipWhenWatched
}

// UnmarshalJSON adopts the legacy graph_end_hook_script key when the current
// end_hook_script is absent.
func (s *Settings) UnmarshalJSON(data []byte) error {
	type alias Settings
	var raw struct {
		alias
		LegacyGraphEndHookScript string `json:"graph_end_hook_script,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = Settings(raw.alias)
	if s.EndHookScript == "" {
		s.EndHookScript = raw.LegacyGraphEndHookScript
	}
	return nil
}
