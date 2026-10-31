package consts

const (
	// EnvKeyBackendBaseURL points session-local CLI tools at their owning backend.
	EnvKeyBackendBaseURL = "QUARTET_BASE_URL"
	// EnvKeyPersonalChatToken authorizes session-local history/delegation tools only while a run is active.
	EnvKeyPersonalChatToken = "QUARTET_CHAT_TOOL_TOKEN"
	// EnvKeyPersonalChatSession identifies the business session attached to tool invocations.
	EnvKeyPersonalChatSession = "QUARTET_CHAT_SESSION_ID"
)
