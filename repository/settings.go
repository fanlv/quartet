package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type ACPEnvVarEntry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// AgentPrefs holds per-ACP-agent-type UI preferences edited in the
// "Agent Defaults" settings tab. The map is keyed by agent type (= the ACP
// serve command, e.g. "claude").
//
//   - FavoriteModelIDs pins models to the top of the model dropdown.
//   - DefaultModelID / DefaultMode / DefaultThoughtLevel are applied when the
//     agent is selected; if a saved value is no longer in the agent's live
//     list the frontend falls back to the first available entry.
type AgentPrefs struct {
	FavoriteModelIDs    []string `json:"favorite_model_ids,omitempty"`
	DefaultModelID      string   `json:"default_model_id,omitempty"`
	DefaultMode         string   `json:"default_mode,omitempty"`
	DefaultThoughtLevel string   `json:"default_thought_level,omitempty"`
}

type Settings struct {
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`

	TitleGenerationAgent *model.AgentRoleConfig      `json:"title_generation_agent,omitempty"`
	GroupReplyAgent      *model.AgentRoleConfig      `json:"group_reply_agent,omitempty"`
	IMSessionAgent       *model.IMSessionAgentConfig `json:"im_session_agent,omitempty"`
	ACPEnvVars           map[string][]ACPEnvVarEntry `json:"acp_env_vars,omitempty"`
	ACPEnvVersions       map[string]int64            `json:"acp_env_versions,omitempty"`
	// AgentPrefs is per-ACP-agent-type favorite models + default
	// model/mode/thought_level, keyed by agent type. Owned by the
	// "Agent Defaults" settings tab.
	AgentPrefs    map[string]AgentPrefs `json:"agent_prefs,omitempty"`
	LarkAppID     string                `json:"lark_app_id,omitempty"`
	LarkAppSecret string                `json:"lark_app_secret,omitempty"`
	// LarkIMAdminSenderID is the OpenID of the admin user allowed to send IM commands in P2P.
	LarkIMAdminSenderID string `json:"lark_im_admin_sender_id,omitempty"`
	// LarkIMSophiaSenderID is the OpenID of the bot to be mentioned in group chats to trigger replies.
	LarkIMSophiaSenderID string `json:"lark_im_sophia_sender_id,omitempty"`
	IMWorkspaceID        string `json:"im_workspace_id,omitempty"`

	// WeChatAdminIDs is the whitelist of iLink user IDs allowed to send
	// messages to this quartet's WeChat bot. Populated via the scan-to-
	// login flow (seed entry = logged-in account itself) plus the first-
	// contact approval UI in Settings → WeChat panel.
	WeChatAdminIDs []string `json:"wechat_admin_ids,omitempty"`

	// EndHookScript is the global default shell script run when a unit of work
	// ends: a graph workflow End node with EndHookMode "default" is reached, or an
	// interactive round terminates (e.g. "send a Lark message when the task
	// finishes"). A pure side-effect: its output is ignored and a failure is
	// logged, never affecting the run.
	EndHookScript string `json:"end_hook_script,omitempty"`

	// EndHookSkipWhenWatched suppresses the interactive round end hook while
	// somebody is watching that Job's output live (chat page in a visible tab,
	// iOS chat page in the foreground, graph run page). Nil means enabled: a
	// config written before this switch existed should still get the quiet
	// behavior, and users who want a notification on every round opt out
	// explicitly. Graph node hooks ignore it.
	EndHookSkipWhenWatched *bool `json:"end_hook_skip_when_watched,omitempty"`
}

// EndHookSkipsWatchedJob reports whether the interactive end hook should stay
// quiet for a Job that currently has a live on-screen viewer. Absent config
// means yes — see EndHookSkipWhenWatched.
func (s *Settings) EndHookSkipsWatchedJob() bool {
	if s == nil || s.EndHookSkipWhenWatched == nil {
		return true
	}
	return *s.EndHookSkipWhenWatched
}

// UnmarshalJSON adopts the legacy graph_end_hook_script key when the current
// end_hook_script is absent, so renaming the field does not silently discard a
// user's configured script.
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

type SettingsRepo interface {
	Get() (*Settings, error)
	Save(s *Settings) error
}

type fileSettingsRepo struct {
	filePath string
	sandbox  fileserver.FileManager
	mu       sync.RWMutex
}

func NewSettingsRepo() (SettingsRepo, error) {
	fp, err := path.SettingsConfigFile()
	if err != nil {
		return nil, fmt.Errorf("get settings file path failed: %w", err)
	}
	sb := fileserver.GetFileManager()
	return &fileSettingsRepo{filePath: fp, sandbox: sb}, nil
}

func (r *fileSettingsRepo) Get() (*Settings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Settings{}, nil
		}
		return nil, fmt.Errorf("read settings file failed: %w", err)
	}

	var s Settings
	if err := json.Unmarshal([]byte(result.Content), &s); err != nil {
		backupCorruptFile(context.Background(), r.filePath, err)
		return &Settings{}, fmt.Errorf("parse settings file failed: %w", err)
	}
	return &s, nil
}

func (r *fileSettingsRepo) Save(s *Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Contains secrets (LarkAppSecret, admin IDs); restrict to owner-only.
	return AtomicWriteFile(r.filePath, data, 0600)
}
