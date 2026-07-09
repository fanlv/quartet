package consts

const (
	KeySystemPrompt    = "system_prompt"
	KeyGroupChatPrompt = "group_chat_prompt"
)

// Agent types
const (
	AgentTypeEino = "eino"
)

// Default values
const (
	DefaultSessionTitle    = "New Chat"
	DefaultJobTitle        = "New Job"
	ScheduleJobTitlePrefix = "[Scheduled] "
)

// Default workspace. Exactly one workspace with this fixed ID is guaranteed
// to exist per installation: it is auto-created on first boot with workdir
// defaulting to $HOME, and the delete handler refuses requests for it so the
// system always has a fallback target (e.g. scheduled tasks with no explicit
// workspace, or new users who haven't created one yet).
const (
	DefaultWorkspaceID    = "ws-1"
	DefaultWorkspaceTitle = "Default Workspace"
)

// Environment variable keys
const (
	EnvKeyAgentAuth   = "X_AGENT_AUTH"
	EnvKeyListenAddr  = "QUARTET_LISTEN_ADDR"  // override default listen addr; always wins over the cert-based default (443 vs 8090)
	EnvKeyLogHTTPBody = "QUARTET_LOG_HTTP_BODY" // opt-in to log request/response bodies (noisy, may leak secrets; disabled by default)
	EnvKeyLogLevel    = "QUARTET_LOG_LEVEL"     // initial log level: debug|info|warn|error (default info)
	EnvKeyCORSOrigins = "QUARTET_CORS_ORIGINS"  // comma-separated CORS allowlist; defaults to same-origin if unset
	EnvKeyStaticDir   = "QUARTET_STATIC_DIR"    // dir served as the web UI static root; default "static" (relative to cwd)
	EnvKeyCertsDir    = "QUARTET_CERTS_DIR"     // dir holding cert.pem/key.pem; presence enables HTTPS; default "certs" (relative to cwd)
)

// HTTP header names exchanged with the web client. Use the hyphen form —
// many reverse proxies (nginx default) drop headers containing underscores.
const (
	HeaderAgentAuth = "X-AGENT-AUTH"
)

// Error codes
const (
	ErrorCodeInit  = "INIT_ERROR"
	ErrorCodeAgent = "AGENT_ERROR"
)

// Builtin loop/job variable keys
const (
	VarJobID       = "_job_id"
	VarJobTitle    = "_job_title"
	VarJobWorkdir  = "_job_workdir"
	VarWorkspaceID = "_workspace_id"

	// Per-round dynamic builtin variables (injected before each step execution)
	VarCurrentTime      = "_current_time"
	VarCurrentPath      = "_current_path"
	VarLastAssistantMsg = "_last_assistant_msg"
)
