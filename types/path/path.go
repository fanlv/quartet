package path

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	localMemoryEnvVar = "LOCAL_MEMORY"

	// metaDir is the directory name for quartet data
	metaDir       = ".meta"
	agentDir      = "agent"
	jobsDir       = "jobs"
	sessionsDir   = "sessions"
	workspacesDir = "workspaces"

	// metaFile is the filename for session metadata
	metaFile = "meta.json"

	// messagesFile is the filename for chat messages
	messagesFile = "messages.jsonl"

	// summaryFile is the filename for summary message
	summaryFile = "summary.json"

	// modelsFile is the filename for model configurations
	modelsFile = "models.json"

	// settingsFile is the filename for general settings
	settingsFile = "settings.json"

	// recentDirsFile is the filename for recent directory history
	recentDirsFile = "recent_dirs.json"

	// jobMetaFile is the filename for job metadata
	jobMetaFile = "job.json"

	// workspaceMetaFile is the filename for workspace metadata
	workspaceMetaFile = "workspace.json"
)

// SessionTasksDir returns the .tasks directory path within a session directory.
// Sibling to .meta; owned by the plan_task middleware (eino path only).
func SessionTasksDir(sessionDir string) string {
	return filepath.Join(sessionDir, ".tasks")
}

// SessionReductionDir returns the reduction cache directory path within a
// session directory. Sibling to .meta; owned by the reduction middleware
// (eino path only).
func SessionReductionDir(sessionDir string) string {
	return filepath.Join(sessionDir, "reduction")
}

// MetaDir returns the .meta directory path within a session directory
func MetaDir(sessionDir string) string {
	return filepath.Join(sessionDir, metaDir)
}

// MetaFilePath returns the meta.json file path within a session directory
func MetaFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, metaDir, metaFile)
}

// MessagesFilePath returns the messages.jsonl file path within a session directory
func MessagesFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, metaDir, messagesFile)
}

// SummaryFilePath returns the summary.json file path within a session directory
func SummaryFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, metaDir, summaryFile)
}

func AgentDir() (string, error) {
	if ws := os.Getenv(localMemoryEnvVar); ws != "" {
		return filepath.Join(ws, agentDir), nil
	}

	return "", fmt.Errorf("%s env var is not set", localMemoryEnvVar)
}

// ModelsConfigFile returns the models.json file path in user's home .quartet directory
func ModelsConfigFile() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, modelsFile), nil
}

// SettingsConfigFile returns the settings.json file path in AgentDir
func SettingsConfigFile() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFile), nil
}

// RecentDirsFile returns the recent_dirs.json file path in AgentDir
func RecentDirsFile() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, recentDirsFile), nil
}

// PromptsDir returns the prompts directory path within AgentDir
func PromptsDir() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompts"), nil
}

// TemplatesDir returns the templates directory path within AgentDir
func TemplatesDir() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "templates"), nil
}

// ShellTempDir returns the process-owned temp directory used for shell helper
// scripts and control files when a job has no explicit workdir.
func ShellTempDir() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tmp", "shell"), nil
}

// SchedulesDir returns the schedules directory path within AgentDir
func SchedulesDir() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "schedules"), nil
}

// UsageStatsDir returns {LOCAL_MEMORY}/agent/usage_stats/.
func UsageStatsDir() (string, error) {
	dir, err := AgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "usage_stats"), nil
}

// UsageStatsMonthFile returns {LOCAL_MEMORY}/agent/usage_stats/YYYY-MM.json
// for the given time in the server's local timezone.
func UsageStatsMonthFile(t time.Time) (string, error) {
	dir, err := UsageStatsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, t.Format("2006-01")+".json"), nil
}

// JobMetaDir returns the .meta directory path within a job directory
func JobMetaDir(jobDir string) string {
	return filepath.Join(jobDir, metaDir)
}

// JobMetaFilePath returns the job.json file path within a job directory
func JobMetaFilePath(jobDir string) string {
	return filepath.Join(jobDir, metaDir, jobMetaFile)
}

// UploadsDir returns {LOCAL_MEMORY}/uploads/
func UploadsDir() (string, error) {
	if ws := os.Getenv(localMemoryEnvVar); ws != "" {
		return filepath.Join(ws, "uploads"), nil
	}
	return "", fmt.Errorf("%s env var is not set", localMemoryEnvVar)
}

// LocalJobsDirInWorkspace returns {LOCAL_MEMORY}/workspaces/{wsID}/jobs/
func LocalJobsDirInWorkspace(wsID string) string {
	return filepath.Join(LocalWorkspaceDir(wsID), jobsDir)
}

// LocalJobDirInWorkspace returns {LOCAL_MEMORY}/workspaces/{wsID}/jobs/{jobID}
func LocalJobDirInWorkspace(wsID, jobID string) string {
	return filepath.Join(LocalJobsDirInWorkspace(wsID), jobID)
}

// LocalSessionsDirInWorkspaceJob returns {LOCAL_MEMORY}/workspaces/{wsID}/jobs/{jobID}/sessions/
func LocalSessionsDirInWorkspaceJob(wsID, jobID string) string {
	return filepath.Join(LocalJobDirInWorkspace(wsID, jobID), sessionsDir)
}

// LocalSessionDirInWorkspaceJob returns {LOCAL_MEMORY}/workspaces/{wsID}/jobs/{jobID}/sessions/{sessionID}
func LocalSessionDirInWorkspaceJob(wsID, jobID, sessionID string) string {
	return filepath.Join(LocalJobDirInWorkspace(wsID, jobID), sessionsDir, sessionID)
}

// LocalWorkspacesDir returns {LOCAL_MEMORY}/workspaces/. Panics if the env
// var is not set — the previous behaviour (falling back to the relative path
// "workspaces") created data directories in whatever CWD the process happened
// to have, which routinely caused data to silently land outside LOCAL_MEMORY.
// Every binary that needs workspace I/O already validates LOCAL_MEMORY at
// startup, so reaching this path without the env var set is a programming
// bug. Panic (rather than log.Fatalf) runs deferred cleanup and yields a
// stack trace so the buggy caller is obvious.
func LocalWorkspacesDir() string {
	ws := os.Getenv(localMemoryEnvVar)
	if ws == "" {
		panic(fmt.Sprintf("%s env var is not set; cannot resolve workspaces dir", localMemoryEnvVar))
	}
	return filepath.Join(ws, workspacesDir)
}

// LocalWorkspaceDir returns {LOCAL_MEMORY}/workspaces/{id}
func LocalWorkspaceDir(id string) string {
	return filepath.Join(LocalWorkspacesDir(), id)
}

// WorkspaceMetaDir returns the .meta directory within a workspace directory
func WorkspaceMetaDir(wsDir string) string {
	return filepath.Join(wsDir, metaDir)
}

// WorkspaceMetaFilePath returns the workspace.json file path within a workspace directory
func WorkspaceMetaFilePath(wsDir string) string {
	return filepath.Join(wsDir, metaDir, workspaceMetaFile)
}

// IMJobMappingDir returns {LOCAL_MEMORY}/im/mappings/. Panics on missing
// env var (see LocalWorkspacesDir for rationale).
func IMJobMappingDir() string {
	ws := os.Getenv(localMemoryEnvVar)
	if ws == "" {
		panic(fmt.Sprintf("%s env var is not set; cannot resolve IM job mapping dir", localMemoryEnvVar))
	}
	return filepath.Join(ws, "im", "mappings")
}

// IMJobMappingFilePath returns {LOCAL_MEMORY}/im/mappings/{platform}/{chatID}.json
func IMJobMappingFilePath(platform, chatID string) string {
	// chatID comes from third-party IM platforms; make sure it cannot escape
	// the IMJobMappingDir via path traversal or path separators.
	return filepath.Join(IMJobMappingDir(), safeExternalID(platform), safeExternalID(chatID)+".json")
}

// IMJobMappingLegacyFilePath returns the old flat layout used by early
// versions: {LOCAL_MEMORY}/im/mappings/{platform}_{chatID}.json.
//
// Kept for backward-compatible reads so existing deployments do not lose
// mappings after upgrading.
func IMJobMappingLegacyFilePath(platform, chatID string) string {
	return filepath.Join(IMJobMappingDir(), safeExternalID(platform)+"_"+safeExternalID(chatID)+".json")
}

// IMMessageDir returns {LOCAL_MEMORY}/im/{chatID}/. Panics on missing env
// var (see LocalWorkspacesDir for rationale).
func IMMessageDir(chatID string) string {
	ws := os.Getenv(localMemoryEnvVar)
	if ws == "" {
		panic(fmt.Sprintf("%s env var is not set; cannot resolve IM message dir", localMemoryEnvVar))
	}
	// chatID comes from third-party IM platforms; ensure it cannot traverse out
	// of {LOCAL_MEMORY}/im.
	return filepath.Join(ws, "im", safeExternalID(chatID))
}

// IMMessageFilePath returns {LOCAL_MEMORY}/im/{chatID}/YYYY-MM-DD.jsonl
func IMMessageFilePath(chatID string, t time.Time) string {
	return filepath.Join(IMMessageDir(chatID), t.Format("2006-01-02")+".jsonl")
}

// safeExternalID normalizes an external identifier (e.g. chatID/platform)
// into a filesystem-safe single path component.
//
// - For the common case (no separators and no ".."), it returns the input.
// - If the input contains path traversal attempts, separators, null bytes,
//   or resolves to "." / ".." on its own, it returns a stable sha256-based
//   name so we stay within the intended directory without silently
//   collapsing distinct IDs.
// safeExternalID is applied to externally-supplied identifiers (bot IDs,
// platform names, chat IDs) before joining into a filesystem path.
//
// Rules:
//   - Empty input maps to the hash of the empty string rather than a fixed
//     literal like "_" so it can't collide with a caller who legitimately
//     passes "_" as an ID.
//   - Any path-traversal character (., .., /, \, NUL) or embedded ".." is
//     replaced with a deterministic sha256 of the raw ID so the mapping
//     stays stable across calls while staying within the intended dir.
func safeExternalID(id string) string {
	if id == "" ||
		id == "." || id == ".." ||
		strings.Contains(id, "..") ||
		strings.ContainsAny(id, "/\\") ||
		strings.ContainsRune(id, 0) {
		sum := sha256.Sum256([]byte(id))
		return "sha256-" + hex.EncodeToString(sum[:])
	}
	return id
}

// UserInputDir returns {LOCAL_MEMORY}/user_input/. Flat, single-level layout so
// all "真实用户输入" (IM admin private chat + Web) from every source land in one
// place and can be scanned by date alone. Panics on missing env var (see
// LocalWorkspacesDir for rationale).
func UserInputDir() string {
	ws := os.Getenv(localMemoryEnvVar)
	if ws == "" {
		panic(fmt.Sprintf("%s env var is not set; cannot resolve user input dir", localMemoryEnvVar))
	}
	return filepath.Join(ws, "user_input")
}

// UserInputFilePath returns {LOCAL_MEMORY}/user_input/YYYY-MM-DD.jsonl for the
// given time in the server's local timezone.
func UserInputFilePath(t time.Time) string {
	return filepath.Join(UserInputDir(), t.Format("2006-01-02")+".jsonl")
}

// WeChatAccountsDir returns {LOCAL_MEMORY}/wechat/accounts/. Panics on
// missing env var (see LocalWorkspacesDir for rationale).
func WeChatAccountsDir() string {
	ws := os.Getenv(localMemoryEnvVar)
	if ws == "" {
		panic(fmt.Sprintf("%s env var is not set; cannot resolve wechat accounts dir", localMemoryEnvVar))
	}
	return filepath.Join(ws, "wechat", "accounts")
}

// WeChatSyncBufFile returns {LOCAL_MEMORY}/wechat/accounts/{botID}.sync.json.
// Used by the iLink long-poll monitor to persist get_updates_buf so reconnects
// after a restart resume from the last seen seq instead of replaying history.
func WeChatSyncBufFile(botID string) string {
	return filepath.Join(WeChatAccountsDir(), safeExternalID(botID)+".sync.json")
}
