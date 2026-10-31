package model

// PersonalAgent is a business identity, independent of its ACP harness.
type PersonalAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SystemPrompt string `json:"systemPrompt"`
	HarnessID    string `json:"harnessId,omitempty"`
	Main         bool   `json:"main"`
	Revision     int64  `json:"revision"`
	Deleted      bool   `json:"deleted,omitempty"`
}

type PersonalChatSettings struct {
	IdleMinutes   int  `json:"idleMinutes"`
	Debug         bool `json:"debug"`
	MaxConcurrent int  `json:"maxConcurrent"`
}

type PersonalChatSource struct {
	Platform  string `json:"platform,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	SenderID  string `json:"senderId,omitempty"`
	MessageID string `json:"messageId,omitempty"`
}

type PersonalChatTask struct {
	RotateOnNextMessage bool     `json:"rotateOnNextMessage,omitempty"`
	AwaitingInput       bool     `json:"awaitingInput"`
	WaitReason          string   `json:"waitReason,omitempty"`
	ID                  string   `json:"id"`
	AgentID             string   `json:"agentId"`
	Title               string   `json:"title"`
	ParentTaskID        string   `json:"parentTaskId,omitempty"`
	ParentRunID         string   `json:"parentRunId,omitempty"`
	SessionID           string   `json:"sessionId,omitempty"`
	Queue               []string `json:"queue"`
	Paused              bool     `json:"paused"`
	PriorityRunID       string   `json:"priorityRunId,omitempty"`
	IdleSince           int64    `json:"idleSince"`
	CreatedAt           int64    `json:"createdAt"`
}

type PersonalChatSession struct {
	SummaryAttempt string  `json:"summaryAttempt,omitempty"`
	Maintenance    bool    `json:"maintenance,omitempty"`
	ID             string  `json:"id"`
	TaskID         string  `json:"taskId"`
	AgentID        string  `json:"agentId"`
	HarnessID      string  `json:"harnessId"`
	AgentRevision  int64   `json:"agentRevision"`
	SystemPrompt   string  `json:"systemPrompt"`
	PreviousID     string  `json:"previousId,omitempty"`
	Summary        string  `json:"summary,omitempty"`
	SummaryThrough string  `json:"summaryThrough,omitempty"`
	SummaryError   string  `json:"summaryError,omitempty"`
	Meta           Session `json:"meta"`
	CreatedAt      int64   `json:"createdAt"`
}

type PersonalChatTool struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Status string `json:"status"`
}

type PersonalChatMessage struct {
	ID         string             `json:"id"`
	Order      uint64             `json:"order"`
	Role       string             `json:"role"`
	AgentID    string             `json:"agentId"`
	TaskID     string             `json:"taskId"`
	SessionID  string             `json:"sessionId"`
	RunID      string             `json:"runId"`
	Content    string             `json:"content"`
	RawContent string             `json:"rawContent,omitempty"`
	Thought    string             `json:"thought,omitempty"`
	Tools      []PersonalChatTool `json:"tools,omitempty"`
	ToolCount  int                `json:"toolCount"`
	State      string             `json:"state"`
	Error      string             `json:"error,omitempty"`
	Internal   bool               `json:"internal,omitempty"`
	Source     PersonalChatSource `json:"source"`
	CreatedAt  int64              `json:"createdAt"`
	UpdatedAt  int64              `json:"updatedAt"`
	Revision   uint64             `json:"revision"`
}

type PersonalChatRun struct {
	DeliveryNextPart int    `json:"deliveryNextPart"`
	ID               string `json:"id"`
	MessageID        string `json:"messageId"`
	ReplyID          string `json:"replyId"`
	TaskID           string `json:"taskId"`
	SessionID        string `json:"sessionId"`
	AgentID          string `json:"agentId"`
	UserID           string `json:"userId,omitempty"`
	Hash             string `json:"hash"`
	State            string `json:"state"`
	Error            string `json:"error,omitempty"`
	CreatedAt        int64  `json:"createdAt"`
	FinishedAt       int64  `json:"finishedAt"`
	Delivered        bool   `json:"delivered"`
	DeliveryError    string `json:"deliveryError,omitempty"`
	DeliveryState    string `json:"deliveryState,omitempty"`
	ResultReturned   bool   `json:"resultReturned"`
	ResultError      string `json:"resultError,omitempty"`
	HookError        string `json:"hookError,omitempty"`
}

// PersonalChatCommit is one atomic JSONL record. Materialized views are rebuilt
// from these commits; they never constitute a second source of truth.
type PersonalChatCommit struct {
	Version  int                   `json:"version"`
	Seq      uint64                `json:"seq"`
	Agents   []PersonalAgent       `json:"agents,omitempty"`
	Tasks    []PersonalChatTask    `json:"tasks,omitempty"`
	Sessions []PersonalChatSession `json:"sessions,omitempty"`
	Runs     []PersonalChatRun     `json:"runs,omitempty"`
	Messages []PersonalChatMessage `json:"messages,omitempty"`
	Settings *PersonalChatSettings `json:"settings,omitempty"`
}

type PersonalChatSnapshot struct {
	Epoch            string                `json:"epoch"`
	DefaultHarnessID string                `json:"defaultHarnessId"`
	Workdir          string                `json:"workdir"`
	Code             int                   `json:"code"`
	Seq              uint64                `json:"seq"`
	Agents           []PersonalAgent       `json:"agents"`
	Tasks            []PersonalChatTask    `json:"tasks"`
	Sessions         []PersonalChatSession `json:"sessions"`
	Runs             []PersonalChatRun     `json:"runs"`
	Messages         []PersonalChatMessage `json:"messages"`
	Settings         PersonalChatSettings  `json:"settings"`
	Before           uint64                `json:"before"`
	HasMore          bool                  `json:"hasMore"`
	Warnings         []string              `json:"warnings"`
}

type PersonalChatSendRequest struct {
	ClientMessageID string             `json:"clientMessageId"`
	Content         string             `json:"content"`
	AgentID         string             `json:"agentId,omitempty"`
	TaskID          string             `json:"taskId,omitempty"`
	HarnessID       string             `json:"harnessId,omitempty"`
	ModelID         string             `json:"modelId,omitempty"`
	Mode            string             `json:"mode,omitempty"`
	ThoughtLevel    string             `json:"thoughtLevel,omitempty"`
	Independent     bool               `json:"independent,omitempty"`
	StopAndSend     bool               `json:"stopAndSend,omitempty"`
	Internal        bool               `json:"internal,omitempty"`
	ParentTaskID    string             `json:"parentTaskId,omitempty"`
	ParentRunID     string             `json:"parentRunId,omitempty"`
	Source          PersonalChatSource `json:"source"`
}

type PersonalChatSearchHit struct {
	SessionID string `json:"sessionId"`
	MessageID string `json:"messageId"`
	AgentID   string `json:"agentId"`
	Role      string `json:"role"`
	Excerpt   string `json:"excerpt"`
	CreatedAt int64  `json:"createdAt"`
}
