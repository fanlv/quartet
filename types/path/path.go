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

	quartetDir        = "quartet"
	quartetConfigDir  = "config"
	quartetDataDir    = "data"
	varDir            = "var"
	stateDir          = "state"
	cacheDir          = "cache"
	tmpDir            = "tmp"
	metaDir           = ".meta"
	jobsDir           = "jobs"
	sessionsDir       = "sessions"
	workspacesDir     = "workspaces"
	graphWorkflowsDir = "graph-workflows"
	graphRunDir       = "graph_run"

	// metaFile is the filename for session metadata
	metaFile = "meta.json"

	// messagesFile is the filename for chat messages
	messagesFile = "messages.jsonl"

	// settingsFile is the filename for general settings
	settingsFile = "settings.json"

	// agentCatalogFile stores the durable custom-agent directory. Built-in
	// entries remain code-owned and are merged by the catalog service.
	agentCatalogFile = "agents.json"

	// recentDirsFile is the filename for recent directory history
	recentDirsFile = "recent-dirs.json"

	// acpProbeCacheFile stores the last successfully refreshed ACP selector
	// snapshot so the Home agent list does not wait for subprocess cold starts.
	acpProbeCacheFile = "acp-probe.json"

	// jobMetaFile is the filename for job metadata
	jobMetaFile = "job.json"

	// workspaceMetaFile is the filename for workspace metadata
	workspaceMetaFile = "workspace.json"

	graphRunFile          = "run.json"
	graphRunInstancesFile = "instances.json"
	graphRunEdgesFile     = "edges.json"
	graphRunVariablesFile = "variables.json"
	graphRunLineageFile   = "session_lineage.json"
	graphRunProgressFile  = "progress.json"
	graphRunResumeFile    = "resume.json"
	graphRunEventsFile    = "events.jsonl"
)

// MetaDir returns {sessionDir}/.meta/.
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

// LocalMemoryDir returns {LOCAL_MEMORY}/, the stable Memory root configured by
// the LOCAL_MEMORY env var.
func LocalMemoryDir() (string, error) {
	root := strings.TrimSpace(os.Getenv(localMemoryEnvVar))
	if root == "" {
		return "", fmt.Errorf("%s env var is not set", localMemoryEnvVar)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%s must be an absolute path, got %q", localMemoryEnvVar, root)
	}
	return filepath.Clean(root), nil
}

// QuartetDir returns {LOCAL_MEMORY}/quartet/, the root of Quartet-owned files.
func QuartetDir() (string, error) {
	root, err := LocalMemoryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, quartetDir), nil
}

// QuartetConfigDir returns {LOCAL_MEMORY}/quartet/config/, the root of
// human-maintained Quartet configuration.
func QuartetConfigDir() (string, error) {
	dir, err := QuartetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, quartetConfigDir), nil
}

// QuartetDataDir returns {LOCAL_MEMORY}/quartet/data/, the root of persistent
// Quartet business data.
func QuartetDataDir() (string, error) {
	dir, err := QuartetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, quartetDataDir), nil
}

// QuartetRuntimeDir returns {LOCAL_MEMORY}/var/quartet/, the root of Quartet
// runtime state, cache and temp files.
func QuartetRuntimeDir() (string, error) {
	root, err := LocalMemoryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, varDir, quartetDir), nil
}

// QuartetStateDir returns {LOCAL_MEMORY}/var/quartet/state/.
func QuartetStateDir() (string, error) {
	dir, err := QuartetRuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateDir), nil
}

// QuartetCacheDir returns {LOCAL_MEMORY}/var/quartet/cache/.
func QuartetCacheDir() (string, error) {
	dir, err := QuartetRuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheDir), nil
}

// QuartetTmpDir returns {LOCAL_MEMORY}/var/quartet/tmp/.
func QuartetTmpDir() (string, error) {
	dir, err := QuartetRuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tmpDir), nil
}

// IconCacheDir returns {LOCAL_MEMORY}/var/quartet/cache/icons/.
func IconCacheDir() (string, error) {
	dir, err := QuartetCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "icons"), nil
}

// SettingsConfigFile returns the persistent settings.json path.
func SettingsConfigFile() (string, error) {
	dir, err := QuartetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFile), nil
}

// AgentCatalogFile returns the persistent custom-agent catalog path.
func AgentCatalogFile() (string, error) {
	dir, err := QuartetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentCatalogFile), nil
}

// RecentDirsFile returns the runtime recent-directory state file.
func RecentDirsFile() (string, error) {
	dir, err := QuartetStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, recentDirsFile), nil
}

// ACPProbeCacheFile returns the persisted ACP selector snapshot path.
func ACPProbeCacheFile() (string, error) {
	dir, err := QuartetCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, acpProbeCacheFile), nil
}

// FileSharesFile returns {LOCAL_MEMORY}/quartet/data/file-shares.json.
func FileSharesFile() (string, error) {
	dir, err := QuartetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "file-shares.json"), nil
}

// PromptsDir returns {LOCAL_MEMORY}/quartet/config/prompts/, the
// human-maintained prompt directory.
func PromptsDir() (string, error) {
	dir, err := QuartetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompts"), nil
}

// GraphWorkflowsDir returns {LOCAL_MEMORY}/quartet/config/graph-workflows/.
// Holds one JSON file per GraphWorkflow config (the editable/saved workflow
// definitions). GraphRun runtime artifacts (snapshots, instance/edge state)
// live elsewhere and are owned by the execution engine, not here.
func GraphWorkflowsDir() (string, error) {
	dir, err := QuartetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, graphWorkflowsDir), nil
}

// GraphRunDir returns the runtime artifact directory for a GraphRun bound to a
// Job: {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/graph_run/.
// The run data lives with the Job, not in the global agent directory.
func GraphRunDir(wsID, jobID string) string {
	return filepath.Join(LocalJobDirInWorkspace(wsID, jobID), graphRunDir)
}

// GraphRunFile returns the persisted GraphRun metadata and baseline snapshot.
func GraphRunFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunFile)
}

// GraphRunInstancesFile returns the instance-state snapshot file for a run.
func GraphRunInstancesFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunInstancesFile)
}

// GraphRunEdgesFile returns the edge-state snapshot file for a run.
func GraphRunEdgesFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunEdgesFile)
}

// GraphRunVariablesFile returns the visible-variable snapshot file for a run.
func GraphRunVariablesFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunVariablesFile)
}

// GraphRunSessionLineageFile returns the session-lineage snapshot file for a run.
func GraphRunSessionLineageFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunLineageFile)
}

// GraphRunProgressFile returns the progress snapshot file for a run.
func GraphRunProgressFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunProgressFile)
}

// GraphRunResumeFile returns the resume snapshot file for a run.
func GraphRunResumeFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunResumeFile)
}

// GraphRunEventsFile returns the append-only event log file for a run.
func GraphRunEventsFile(wsID, jobID string) string {
	return filepath.Join(GraphRunDir(wsID, jobID), graphRunEventsFile)
}

// ShellTempDir returns {LOCAL_MEMORY}/var/quartet/tmp/shell/, the process-owned
// temp directory used for shell helper scripts and control files when a job has
// no explicit workdir.
func ShellTempDir() (string, error) {
	dir, err := QuartetTmpDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "shell"), nil
}

// SchedulesDir returns {LOCAL_MEMORY}/quartet/config/schedules/, the Schedule
// definition directory.
func SchedulesDir() (string, error) {
	dir, err := QuartetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "schedules"), nil
}

// ScheduleStatesDir returns {LOCAL_MEMORY}/var/quartet/state/schedules/, the
// runtime Schedule state directory.
func ScheduleStatesDir() (string, error) {
	dir, err := QuartetStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "schedules"), nil
}

// UsageStatsDir returns {LOCAL_MEMORY}/quartet/usage-stats/. Usage statistics
// live outside quartet/data so they remain visible to the Memory repository's
// Git tracking rules.
func UsageStatsDir() (string, error) {
	dir, err := QuartetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "usage-stats"), nil
}

// LegacyUsageStatsDir returns the former usage-statistics directory. It is
// retained only so the usage service can copy existing monthly files into the
// Git-managed location during startup.
func LegacyUsageStatsDir() (string, error) {
	dir, err := QuartetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "usage-stats"), nil
}

// UsageStatsMonthFile returns {LOCAL_MEMORY}/quartet/usage-stats/YYYY-MM.json
// for the given time in the server's local timezone.
func UsageStatsMonthFile(t time.Time) (string, error) {
	dir, err := UsageStatsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, t.Format("2006-01")+".json"), nil
}

// JobMetaDir returns {jobDir}/.meta/.
func JobMetaDir(jobDir string) string {
	return filepath.Join(jobDir, metaDir)
}

// JobMetaFilePath returns the job.json file path within a job directory
func JobMetaFilePath(jobDir string) string {
	return filepath.Join(jobDir, metaDir, jobMetaFile)
}

// UploadsDir returns {LOCAL_MEMORY}/quartet/data/uploads/, the persistent
// upload directory.
func UploadsDir() (string, error) {
	dir, err := QuartetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "uploads"), nil
}

// PersistentIMMediaDir returns {LOCAL_MEMORY}/quartet/data/uploads/im-media/,
// the directory for media referenced by durable records.
func PersistentIMMediaDir() (string, error) {
	dir, err := UploadsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "im-media"), nil
}

// IMMediaCacheDir returns {LOCAL_MEMORY}/var/quartet/cache/im-media/, the
// disposable processing cache for IM media.
func IMMediaCacheDir() (string, error) {
	dir, err := QuartetCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "im-media"), nil
}

// LocalJobsDirInWorkspace returns {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/.
func LocalJobsDirInWorkspace(wsID string) string {
	return filepath.Join(LocalWorkspaceDir(wsID), jobsDir)
}

// LocalJobDirInWorkspace returns {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}.
func LocalJobDirInWorkspace(wsID, jobID string) string {
	return filepath.Join(LocalJobsDirInWorkspace(wsID), jobID)
}

// LocalSessionsDirInWorkspaceJob returns {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/sessions/.
func LocalSessionsDirInWorkspaceJob(wsID, jobID string) string {
	return filepath.Join(LocalJobDirInWorkspace(wsID, jobID), sessionsDir)
}

// LocalSessionDirInWorkspaceJob returns {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/sessions/{sessionID}.
func LocalSessionDirInWorkspaceJob(wsID, jobID, sessionID string) string {
	return filepath.Join(LocalJobDirInWorkspace(wsID, jobID), sessionsDir, sessionID)
}

// LocalWorkspacesDir returns {LOCAL_MEMORY}/quartet/data/workspaces/. Panics if the env
// var is not set — the previous behaviour (falling back to the relative path
// "workspaces") created data directories in whatever CWD the process happened
// to have, which routinely caused data to silently land outside LOCAL_MEMORY.
// Every binary that needs workspace I/O already validates LOCAL_MEMORY at
// startup, so reaching this path without the env var set is a programming
// bug. Panic (rather than log.Fatalf) runs deferred cleanup and yields a
// stack trace so the buggy caller is obvious.
func LocalWorkspacesDir() string {
	dir, err := QuartetDataDir()
	if err != nil {
		panic(fmt.Sprintf("cannot resolve workspaces dir: %v", err))
	}
	return filepath.Join(dir, workspacesDir)
}

// LocalWorkspaceDir returns {LOCAL_MEMORY}/quartet/data/workspaces/{id}.
func LocalWorkspaceDir(id string) string {
	return filepath.Join(LocalWorkspacesDir(), id)
}

// WorkspaceMetaDir returns {wsDir}/.meta/.
func WorkspaceMetaDir(wsDir string) string {
	return filepath.Join(wsDir, metaDir)
}

// WorkspaceMetaFilePath returns the workspace.json file path within a workspace directory
func WorkspaceMetaFilePath(wsDir string) string {
	return filepath.Join(wsDir, metaDir, workspaceMetaFile)
}

// IMJobMappingDir returns {LOCAL_MEMORY}/quartet/data/im/mappings/, the
// persistent IM mapping directory.
func IMJobMappingDir() string {
	dir, err := QuartetDataDir()
	if err != nil {
		panic(fmt.Sprintf("cannot resolve IM job mapping dir: %v", err))
	}
	return filepath.Join(dir, "im", "mappings")
}

// IMJobMappingFilePath returns {LOCAL_MEMORY}/quartet/data/im/mappings/{platform}/{chatID}.json.
func IMJobMappingFilePath(platform, chatID string) string {
	// chatID comes from third-party IM platforms; make sure it cannot escape
	// the IMJobMappingDir via path traversal or path separators.
	return filepath.Join(IMJobMappingDir(), safeExternalID(platform), safeExternalID(chatID)+".json")
}

// IMJobMappingLegacyFilePath returns the old flat layout used by early
// versions: {LOCAL_MEMORY}/quartet/data/im/mappings/{platform}_{chatID}.json.
//
// Kept for backward-compatible reads so existing deployments do not lose
// mappings after upgrading.
func IMJobMappingLegacyFilePath(platform, chatID string) string {
	return filepath.Join(IMJobMappingDir(), safeExternalID(platform)+"_"+safeExternalID(chatID)+".json")
}

// IMMessageDir returns {LOCAL_MEMORY}/quartet/data/im/{chatID}/, the persistent
// IM message directory for a chat.
func IMMessageDir(chatID string) string {
	dir, err := QuartetDataDir()
	if err != nil {
		panic(fmt.Sprintf("cannot resolve IM message dir: %v", err))
	}
	// chatID comes from third-party IM platforms; ensure it cannot traverse out
	// of {LOCAL_MEMORY}/quartet/data/im.
	return filepath.Join(dir, "im", safeExternalID(chatID))
}

// IMMessageFilePath returns {LOCAL_MEMORY}/quartet/data/im/{chatID}/YYYY-MM-DD.jsonl.
func IMMessageFilePath(chatID string, t time.Time) string {
	return filepath.Join(IMMessageDir(chatID), t.Format("2006-01-02")+".jsonl")
}

// safeExternalID normalizes an external identifier (e.g. chatID/platform)
// into a filesystem-safe single path component.
//
//   - For the common case (no separators and no ".."), it returns the input.
//   - If the input contains path traversal attempts, separators, null bytes,
//     or resolves to "." / ".." on its own, it returns a stable sha256-based
//     name so we stay within the intended directory without silently
//     collapsing distinct IDs.
//
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

// UserInputDir returns {LOCAL_MEMORY}/quartet/data/user-input/, the persistent
// flat user-input directory.
func UserInputDir() string {
	dir, err := QuartetDataDir()
	if err != nil {
		panic(fmt.Sprintf("cannot resolve user input dir: %v", err))
	}
	return filepath.Join(dir, "user-input")
}

// UserInputFilePath returns {LOCAL_MEMORY}/quartet/data/user-input/YYYY-MM-DD.jsonl for the
// given time in the server's local timezone.
func UserInputFilePath(t time.Time) string {
	return filepath.Join(UserInputDir(), t.Format("2006-01-02")+".jsonl")
}

// WeChatAccountsDir returns {LOCAL_MEMORY}/quartet/data/wechat/accounts/, the
// persistent WeChat account directory.
func WeChatAccountsDir() string {
	dir, err := QuartetDataDir()
	if err != nil {
		panic(fmt.Sprintf("cannot resolve wechat accounts dir: %v", err))
	}
	return filepath.Join(dir, "wechat", "accounts")
}

// WeChatSyncBufFile returns {LOCAL_MEMORY}/quartet/data/wechat/accounts/{botID}.sync.json.
// Used by the iLink long-poll monitor to persist get_updates_buf so reconnects
// after a restart resume from the last seen seq instead of replaying history.
func WeChatSyncBufFile(botID string) string {
	return filepath.Join(WeChatAccountsDir(), safeExternalID(botID)+".sync.json")
}

// WeChatUserTokensFile returns
// {LOCAL_MEMORY}/quartet/data/wechat/accounts/user_tokens.json. It persists the
// botID → fromUserID → latest ContextToken map so proactive sends
// (Replier.SendText) keep working across backend restarts and account changes.
func WeChatUserTokensFile() string {
	return filepath.Join(WeChatAccountsDir(), "user_tokens.json")
}

// WeChatOutboxDir returns the durable proactive-send queue directory. Each
// task is a standalone JSON file so enqueue/progress updates are atomic and a
// backend restart can resume from the last acknowledged chunk.
func WeChatOutboxDir() string {
	dir, err := QuartetDataDir()
	if err != nil {
		panic(fmt.Sprintf("cannot resolve wechat outbox dir: %v", err))
	}
	return filepath.Join(dir, "wechat", "outbox")
}

func WeChatOutboxTaskFile(taskID string) string {
	return filepath.Join(WeChatOutboxDir(), safeExternalID(taskID)+".json")
}

// SandboxComposeStateDir returns
// {LOCAL_MEMORY}/var/quartet/state/sandbox/compose/, the durable compose state
// directory.
func SandboxComposeStateDir() (string, error) {
	dir, err := QuartetStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sandbox", "compose"), nil
}
