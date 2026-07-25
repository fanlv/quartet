package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/fanlv/quartet/types/consts"
)

type Session struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Deleted      bool      `json:"deleted,omitempty"`
	ModelID      string    `json:"model_id,omitempty"`
	Type         string    `json:"type,omitempty"`         // ACP agent type (serve command key)
	Workdir      string    `json:"workdir,omitempty"`      // working directory for ACP agents
	JobID        string    `json:"job_id,omitempty"`       // associated job
	WorkspaceID  string    `json:"workspace_id,omitempty"` // associated workspace
	ACPSessionID string    `json:"acp_session_id,omitempty"`
	ACPMode      string    `json:"acp_mode,omitempty"`
	// ACPThoughtLevel is the thought_level config selection (e.g. "high")
	// last applied to the ACP subprocess session. Like ACPMode it is
	// persisted so a Run re-applies it after reconnect / restart.
	ACPThoughtLevel string `json:"acp_thought_level,omitempty"`
	// ACPLastSyncedMessageCount and ACPLastSyncedMessageHash record the
	// state of messages.jsonl at the end of the most recent ACP Run on
	// this session. Used by the ACP agent to detect cross-path drift —
	// if either field disagrees with the on-disk fingerprint at the
	// start of a new Run (e.g. a late ReplacePlaceholderToolResult rewrote
	// a row in place), the
	// subprocess's internal conversation state no longer matches disk
	// and the ACPSessionID is replaced with a fresh one rather than
	// continuing to prompt a stale view.
	//
	// Hash is the primary signal — count alone misses in-place row
	// rewrites that keep the row count stable but change content. Count
	// stays for diagnostic logging and so the persisted state still
	// carries a human-readable "we were at N messages" datum that's
	// useful even without a way to interpret the hash.
	ACPLastSyncedMessageCount int    `json:"acp_last_synced_message_count,omitempty"`
	ACPLastSyncedMessageHash  string `json:"acp_last_synced_message_hash,omitempty"`
}

func NewSession() *Session {
	return &Session{
		ID:        newSessionID(),
		Title:     consts.DefaultSessionTitle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func newSessionID() string {
	t := time.Now()
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("session-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}
