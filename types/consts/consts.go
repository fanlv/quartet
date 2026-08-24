package consts

const (
	KeyGroupChatPrompt = "group_chat_prompt"
)

// Default values
const (
	DefaultSessionTitle    = "New Chat"
	DefaultJobTitle        = "New Job"
	ScheduleJobTitlePrefix = "⏰ "
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
	EnvKeyListenAddr     = "QUARTET_LISTEN_ADDR"     // override default listen addr; always wins over the cert-based default (443 vs 8090)
	EnvKeyLogHTTPBody    = "QUARTET_LOG_HTTP_BODY"   // opt-in to log request/response bodies (noisy, may leak secrets; disabled by default)
	EnvKeyLogLevel       = "QUARTET_LOG_LEVEL"       // initial log level: debug|info|warn|error (default info)
	EnvKeyCORSOrigins    = "QUARTET_CORS_ORIGINS"    // comma-separated CORS allowlist; defaults to same-origin if unset
	EnvKeyTrustedProxies = "QUARTET_TRUSTED_PROXIES" // comma-separated trusted reverse-proxy IPs/CIDRs; defaults to loopback only
	EnvKeyStaticDir      = "QUARTET_STATIC_DIR"      // dir served as the web UI static root; default "static" (relative to cwd)
	EnvKeyCertsDir       = "QUARTET_CERTS_DIR"       // dir holding cert.pem/key.pem; presence enables HTTPS; default "certs" (relative to cwd)
)

// Error codes
const (
	ErrorCodeInit  = "INIT_ERROR"
	ErrorCodeAgent = "AGENT_ERROR"
)

// Builtin variable keys shared with the graph workflow engine.
const (
	// VarCurrentTime is injected per loop iteration by graph loopvars.
	VarCurrentTime = "_current_time"
)
