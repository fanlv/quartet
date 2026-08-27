package model

import (
	"fmt"
	"path/filepath"
	"strings"
)

type RequestMessage struct {
	ID              string           `json:"id"`
	Type            string           `json:"type"`
	Content         string           `json:"content"`
	Timestamp       int64            `json:"timestamp"`
	Role            string           `json:"role"`
	ImageUrls       []string         `json:"imageUrls,omitempty"`
	FileAttachments []FileAttachment `json:"fileAttachments,omitempty"`
}

// FileAttachment is a file uploaded with a user message. Path is the local
// absolute path the Agent can read; Name, MIMEType and Size preserve the
// user-facing metadata because uploaded files have generated disk names.
type FileAttachment struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// AgentContent returns the text prompt sent through ACP. Attachments remain
// structured on RequestMessage for clients and history, while their local
// paths are also made explicit in the prompt so filesystem-capable Agents can
// consume them without requiring a binary ACP transport.
func (m RequestMessage) AgentContent() string {
	var prefix strings.Builder
	for _, imageURL := range m.ImageUrls {
		fmt.Fprintf(&prefix, "![image](%s)\n", imageURL)
	}
	for _, file := range m.FileAttachments {
		name := file.Name
		if name == "" {
			name = filepath.Base(file.Path)
		}
		fmt.Fprintf(&prefix, "Attached file: name=%q path=%q", name, file.Path)
		if file.MIMEType != "" {
			fmt.Fprintf(&prefix, " mime_type=%q", file.MIMEType)
		}
		if file.Size > 0 {
			fmt.Fprintf(&prefix, " size=%d_bytes", file.Size)
		}
		prefix.WriteByte('\n')
	}
	return prefix.String() + m.Content
}

// FileAttachmentPromptPrefix returns the exact prefix AgentContent emits for
// files. History projection uses it only as a fallback for records that have
// structured attachment metadata but predate original_user_content.
func (m RequestMessage) FileAttachmentPromptPrefix() string {
	copy := m
	copy.Content = ""
	copy.ImageUrls = nil
	return copy.AgentContent()
}

type GetPromptRequest struct {
	Key string `json:"key"`
}

type SavePromptRequest struct {
	Key    string `json:"key"`
	Prompt string `json:"prompt"`
}

type CreateJobRequest struct {
	ModelID   string `json:"modelId"`
	AgentType string `json:"agentType"`
	// ClientMessageID makes Job creation retry-safe. For a /new command this
	// is a command-action key derived from (source Job, command message ID), not
	// the message-send key itself.
	ClientMessageID string  `json:"clientMessageId,omitempty"`
	ACPMode         string  `json:"acpMode,omitempty"`
	ACPThoughtLevel string  `json:"acpThoughtLevel,omitempty"`
	Mode            JobMode `json:"mode,omitempty"`
	Workdir         string  `json:"workdir,omitempty"`
	WorkspaceID     string  `json:"workspaceId"`
}

type JobMessageRequest struct {
	Messages        []RequestMessage `json:"messages,omitempty"`
	ModelID         string           `json:"modelId,omitempty"`
	AgentType       string           `json:"agentType,omitempty"`
	SessionID       string           `json:"sessionId,omitempty"`
	ClientMessageID string           `json:"clientMessageId,omitempty"`
	ACPMode         string           `json:"acpMode,omitempty"`
	ACPThoughtLevel string           `json:"acpThoughtLevel,omitempty"`
	// BypassCommand forces the message to go through the regular Job message
	// flow even when the text starts with a known slash command. Set by the
	// Web home page when it builds a new Job from the user's first input:
	// the home page is a pure "message" surface and never executes commands.
	BypassCommand bool `json:"bypassCommand,omitempty"`
}

// ACPConfigTarget names which selector an ACP live-config switch changes.
type ACPConfigTarget string

const (
	ACPConfigTargetModel        ACPConfigTarget = "model"
	ACPConfigTargetMode         ACPConfigTarget = "mode"
	ACPConfigTargetThoughtLevel ACPConfigTarget = "thoughtLevel"
)

// SetACPConfigRequest switches an ACP selector (model / mode / thought_level)
// and asks for the refreshed selector lists back. When SessionID is set the
// switch applies to that session's live agent; otherwise it is a Home
// (session-less) cache selection on AgentType. Cached model-linked state is
// returned immediately and refreshed asynchronously; cache misses probe
// synchronously.
//
// Model / Mode / ThoughtLevel carry the current selection. For the session
// path only the Target's value is applied to the live agent.
type SetACPConfigRequest struct {
	SessionID    string          `json:"sessionId,omitempty"`
	AgentType    string          `json:"agentType,omitempty"`
	Target       ACPConfigTarget `json:"target"`
	Model        string          `json:"model,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	ThoughtLevel string          `json:"thoughtLevel,omitempty"`
}

// SetACPConfigResponse returns the refreshed selector lists after a switch.
// Each list is populated only when the ACP response carried a refreshed list
// for it (see ACPConfigState); the frontend keeps its current values for nil
// lists.
type SetACPConfigResponse struct {
	Code int `json:"code"`
	ACPConfigState
}

type CreateWorkspaceRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Workdir      string `json:"workdir"`
	DefaultAgent string `json:"defaultAgent,omitempty"`
	DefaultModel string `json:"defaultModel,omitempty"`
}

type UpdateWorkspaceRequest struct {
	ExpectedVersion uint64  `json:"expectedVersion"`
	Title           *string `json:"title,omitempty"`
	Description     *string `json:"description,omitempty"`
	Workdir         *string `json:"workdir,omitempty"`
	DefaultAgent    *string `json:"defaultAgent,omitempty"`
	DefaultModel    *string `json:"defaultModel,omitempty"`
}

type UpdateWorkspaceFavoriteRequest struct {
	Favorite bool `json:"favorite"`
}

type ReorderWorkspacesRequest struct {
	WorkspaceIDs []string `json:"workspaceIds"`
}

// ---- Scheduled Task requests ----

type CreateScheduleRequest struct {
	Name            string `json:"name"`
	CronExpr        string `json:"cronExpr"`
	GraphWorkflowID string `json:"graphWorkflowId,omitempty"`
	WorkspaceID     string `json:"workspaceId,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	MaxConcurrent   int    `json:"maxConcurrent,omitempty"`
	Timeout         int    `json:"timeout,omitempty"`
	// Enabled controls activation on the current machine. nil keeps the
	// historical "enabled by default on the creating machine" behavior.
	Enabled *bool `json:"enabled,omitempty"`
}

type UpdateScheduleRequest struct {
	Name            *string `json:"name,omitempty"`
	CronExpr        *string `json:"cronExpr,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
	GraphWorkflowID *string `json:"graphWorkflowId,omitempty"`
	WorkspaceID     *string `json:"workspaceId,omitempty"`
	Workdir         *string `json:"workdir,omitempty"`
	MaxConcurrent   *int    `json:"maxConcurrent,omitempty"`
	Timeout         *int    `json:"timeout,omitempty"`
}
