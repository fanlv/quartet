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
	"github.com/fanlv/quartet/types/path"
)

type ACPEnvVarEntry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type AgentConfig struct {
	AgentType string `json:"agent_type"`
	ModelID   string `json:"model_id,omitempty"`
	ACPMode   string `json:"acp_mode,omitempty"`
}

type Settings struct {
	Username      string                      `json:"username"`
	AvatarURL     string                      `json:"avatar_url"`
	TitleAgent    *AgentConfig                `json:"title_agent,omitempty"`
	MessageAgent  *AgentConfig                `json:"message_agent,omitempty"`
	ACPEnvVars    map[string][]ACPEnvVarEntry `json:"acp_env_vars,omitempty"`
	LarkAppID     string                      `json:"lark_app_id,omitempty"`
	LarkAppSecret string                      `json:"lark_app_secret,omitempty"`
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
