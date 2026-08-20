package catalog

import (
	"fmt"
	"strings"

	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/types/model"
)

type IdentifierKind string

const (
	IdentifierKindBin        IdentifierKind = "bin"
	IdentifierKindEnvKey     IdentifierKind = "env_key"
	IdentifierKindACPCommand IdentifierKind = "acp_command"
)

type HistoricalIdentifier struct {
	Kind  IdentifierKind
	Value string
}

// BuiltinAgent is one built-in catalog entry. ACPProgram and
// ACPArgs are authoritative; Command remains the current legacy serialization
// consumed by runtime paths that have not moved to structured argv yet.
type BuiltinAgent struct {
	AgentID               string
	Bin                   string
	ACPProgram            string
	ACPArgs               []string
	Command               string
	EnvKey                string
	HistoricalIdentifiers []HistoricalIdentifier
	DisplayName           string
	IconURL               string
	SupportsHeadlessPrint bool
	Deprecated            bool
	Install               agentinstall.InstallSpec
}

func (a BuiltinAgent) RuntimeDefinition() model.AgentRuntimeDefinition {
	return model.AgentRuntimeDefinition{
		Bin:        a.Bin,
		ACPProgram: a.ACPProgram,
		ACPArgs:    append([]string(nil), a.ACPArgs...),
	}
}

// MigrationIdentifiers returns identifiers in deterministic migration
// priority: AgentID, current Bin, stable env key, current ACP command, then the
// explicitly ordered historical identifiers.
func (a BuiltinAgent) MigrationIdentifiers() []string {
	values := []string{a.AgentID, a.Bin, a.EnvKey, a.Command}
	for _, identifier := range a.HistoricalIdentifiers {
		values = append(values, identifier.Value)
	}
	return uniqueNonEmpty(values)
}

const grokIconURL = "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIzNSIgaGVpZ2h0PSIzMyIgdmlld0JveD0iMCAwIDM1IDMzIiBmaWxsPSJub25lIj48cGF0aCBkPSJNMTMuMjM3MSAyMS4wNDA3TDI0LjMxODYgMTIuODUwNkMyNC44NjE5IDEyLjQ0OTEgMjUuNjM4NCAxMi42MDU3IDI1Ljg5NzMgMTMuMjI5NEMyNy4yNTk3IDE2LjUxODUgMjYuNjUxIDIwLjQ3MTIgMjMuOTQwMyAyMy4xODUxQzIxLjIyOTcgMjUuODk4OSAxNy40NTgxIDI2LjQ5NDEgMTQuMDEwOCAyNS4xMzg2TDEwLjI0NDkgMjYuODg0M0MxNS42NDYzIDMwLjU4MDYgMjIuMjA1MyAyOS42NjY1IDI2LjMwNCAyNS41NjAxQzI5LjU1NTEgMjIuMzA1MSAzMC41NjIgMTcuODY4MyAyOS42MjA1IDEzLjg2NzNMMjkuNjI5IDEzLjg3NThDMjguMjYzNyA3Ljk5ODA5IDI5Ljk2NDcgNS42NDg3MSAzMy40NDkgMC44NDQ1NzZDMzMuNTMxNCAwLjczMDY2NyAzMy42MTM5IDAuNjE2NzU3IDMzLjY5NjQgMC41TDI5LjExMTMgNS4wOTA1NVY1LjA3NjMxTDEzLjIzNDMgMjEuMDQzNiIgZmlsbD0iIzAwMDAwMCIvPjxwYXRoIGQ9Ik0xMC45NTAzIDIzLjAzMTNDNy4wNzM0MyAxOS4zMjM1IDcuNzQxODUgMTMuNTg1MyAxMS4wNDk4IDEwLjI3NjNDMTMuNDk1OSA3LjgyNzIyIDE3LjUwMzYgNi44Mjc2NyAyMS4wMDIxIDguMjk3MUwyNC43NTk1IDYuNTU5OThDMjQuMDgyNiA2LjA3MDE3IDIzLjIxNSA1LjU0MzM0IDIyLjIxOTUgNS4xNzMxM0MxNy43MTk4IDMuMzE5MjYgMTIuMzMyNiA0LjI0MTkyIDguNjc0NzkgNy45MDEyNkM1LjE1NjM1IDExLjQyMzkgNC4wNDk5IDE2Ljg0MDMgNS45NDk5MiAyMS40NjIyQzcuMzY5MjQgMjQuOTE2NSA1LjA0MjU3IDI3LjM1OTggMi42OTg4NCAyOS44MjZDMS44NjgyOSAzMC43MDAyIDEuMDM0OSAzMS41NzQ1IDAuMzYzNjQgMzIuNUwxMC45NDc0IDIzLjAzNDEiIGZpbGw9IiMwMDAwMDAiLz48L3N2Zz4K"

// builtinAgents defines the agents currently supported by the built-in catalog.
var builtinAgents = []BuiltinAgent{
	{
		AgentID: "eino-cli", Bin: "eino-cli", ACPProgram: "eino-cli", ACPArgs: []string{"acp"}, Command: "eino-cli acp", EnvKey: "eino-cli",
		DisplayName: "Eino", IconURL: "https://avatars.githubusercontent.com/u/79236453", SupportsHeadlessPrint: true,
		Install: agentinstall.InstallSpec{
			Method:       agentinstall.InstallMethodProject,
			Steps:        []agentinstall.InstallStep{agentinstall.ProjectMakeStep("build-eino-cli")},
			Instructions: "eino-cli 由本项目自带构建安装：点击安装会在仓库根目录执行 `make build-eino-cli`，构建完成后会自动安装到 PATH。",
		},
	},
	{
		AgentID: "traex", Bin: "traex", ACPProgram: "traex", ACPArgs: []string{"acp", "serve"}, Command: "traex acp serve", EnvKey: "traex",
		DisplayName: "TraeCLI", IconURL: "https://avatars.githubusercontent.com/u/192691831", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{
			Method:       agentinstall.InstallMethodManual,
			Instructions: "Trae CLI（traex）暂无独立官方安装包，随 Trae IDE / TraeX 分发；请从 Trae 官方渠道安装后重试。",
		},
	},
	{
		AgentID: "grok", Bin: "grok", ACPProgram: "grok", ACPArgs: []string{"--no-auto-update", "agent", "stdio"}, Command: "grok --no-auto-update agent stdio", EnvKey: "grok",
		DisplayName: "Grok", IconURL: grokIconURL, SupportsHeadlessPrint: true,
		Install: agentinstall.InstallSpec{
			Method:       agentinstall.InstallMethodScript,
			Steps:        []agentinstall.InstallStep{agentinstall.ScriptStep("https://x.ai/cli/install.sh", "bash")},
			UpgradeSteps: []agentinstall.InstallStep{agentinstall.CommandStep("grok", "update")},
			VersionURL:   "https://storage.googleapis.com/grok-build-public-artifacts/cli/stable",
		},
	},
	{
		AgentID: "openclaw", Bin: "openclaw", ACPProgram: "openclaw", ACPArgs: []string{"acp"}, Command: "openclaw acp", EnvKey: "openclaw",
		DisplayName: "OpenClaw", IconURL: "🦞", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodNPM, Steps: []agentinstall.InstallStep{agentinstall.NPMStep("openclaw")}},
	},
	{
		AgentID: "claude", Bin: "claude", ACPProgram: "claude-agent-acp", Command: "claude-agent-acp", EnvKey: "claude",
		HistoricalIdentifiers: []HistoricalIdentifier{
			{Kind: IdentifierKindACPCommand, Value: "npx @agentclientprotocol/claude-agent-acp"},
		},
		DisplayName: "Claude", IconURL: "https://avatars.githubusercontent.com/u/81847", SupportsHeadlessPrint: true,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodNPM, Steps: []agentinstall.InstallStep{
			agentinstall.NPMStep("@anthropic-ai/claude-code"),
			agentinstall.NPMStep("@agentclientprotocol/claude-agent-acp"),
		}},
	},
	{
		AgentID: "agy", Bin: "agy", ACPProgram: "antigravity-acp", Command: "antigravity-acp", EnvKey: "agy",
		DisplayName: "Antigravity", IconURL: "https://avatars.githubusercontent.com/u/242056456", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodScript, Steps: []agentinstall.InstallStep{
			agentinstall.ScriptStep("https://antigravity.google/cli/install.sh", "bash"),
			agentinstall.NPMStep("antigravity-acp"),
		}},
	},
	{
		AgentID: "cursor-agent", Bin: "cursor-agent", ACPProgram: "cursor-agent", ACPArgs: []string{"acp"}, Command: "cursor-agent acp", EnvKey: "cursor-agent",
		DisplayName: "Cursor", IconURL: "https://avatars.githubusercontent.com/u/126759922", SupportsHeadlessPrint: true,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodScript, Steps: []agentinstall.InstallStep{agentinstall.ScriptStep("https://cursor.com/install", "bash")}},
	},
	{
		AgentID: "copilot", Bin: "copilot", ACPProgram: "copilot", ACPArgs: []string{"--acp", "--stdio"}, Command: "copilot --acp --stdio", EnvKey: "copilot",
		DisplayName: "Copilot", IconURL: "🧑‍✈️", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodNPM, Steps: []agentinstall.InstallStep{agentinstall.NPMStep("@github/copilot")}},
	},
	{
		AgentID: "droid", Bin: "droid", ACPProgram: "droid", ACPArgs: []string{"exec", "--output-format", "acp"}, Command: "droid exec --output-format acp", EnvKey: "droid",
		DisplayName: "Droid", IconURL: "https://avatars.githubusercontent.com/u/131064358", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodScript, Steps: []agentinstall.InstallStep{agentinstall.ScriptStep("https://app.factory.ai/cli", "sh")}},
	},
	{
		AgentID: "kimi", Bin: "kimi", ACPProgram: "kimi", ACPArgs: []string{"acp"}, Command: "kimi acp", EnvKey: "kimi",
		DisplayName: "Kimi", IconURL: "https://avatars.githubusercontent.com/u/129152888", SupportsHeadlessPrint: true,
		Install: agentinstall.InstallSpec{
			Method:         agentinstall.InstallMethodScript,
			Steps:          []agentinstall.InstallStep{agentinstall.ScriptStep("https://code.kimi.com/kimi-code/install.sh", "bash")},
			UpgradeSteps:   []agentinstall.InstallStep{agentinstall.ScriptStep("https://code.kimi.com/kimi-code/install.sh", "bash")},
			VersionPackage: "@moonshot-ai/kimi-code",
		},
	},
	{
		AgentID: "codex", Bin: "codex", ACPProgram: "codex-acp", Command: "codex-acp", EnvKey: "codex",
		HistoricalIdentifiers: []HistoricalIdentifier{
			{Kind: IdentifierKindACPCommand, Value: "npx @agentclientprotocol/codex-acp"},
			{Kind: IdentifierKindACPCommand, Value: "npx @zed-industries/codex-acp"},
		},
		DisplayName: "Codex", IconURL: "https://avatars.githubusercontent.com/u/14957082", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodNPM, Steps: []agentinstall.InstallStep{
			agentinstall.NPMStep("@openai/codex"),
			agentinstall.NPMStep("@agentclientprotocol/codex-acp"),
		}},
	},
	{
		AgentID: "kiro-cli", Bin: "kiro-cli", ACPProgram: "kiro-cli", ACPArgs: []string{"acp"}, Command: "kiro-cli acp", EnvKey: "kiro-cli",
		DisplayName: "Kiro", IconURL: "https://avatars.githubusercontent.com/u/207925904", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodScript, Steps: []agentinstall.InstallStep{agentinstall.ScriptStep("https://cli.kiro.dev/install", "bash")}},
	},
	{
		AgentID: "opencode", Bin: "opencode", ACPProgram: "opencode", ACPArgs: []string{"acp"}, Command: "opencode acp", EnvKey: "opencode",
		HistoricalIdentifiers: []HistoricalIdentifier{
			{Kind: IdentifierKindACPCommand, Value: "npx -y opencode-ai acp"},
		},
		DisplayName: "OpenCode", IconURL: "https://avatars.githubusercontent.com/in/1549082", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodNPM, Steps: []agentinstall.InstallStep{agentinstall.NPMStep("opencode-ai")}},
	},
	{
		AgentID: "kilocode", Bin: "kilocode", ACPProgram: "npx", ACPArgs: []string{"-y", "@kilocode/cli", "acp"}, Command: "npx -y @kilocode/cli acp", EnvKey: "kilocode",
		DisplayName: "KiloCode", IconURL: "https://avatars.githubusercontent.com/u/201822503", SupportsHeadlessPrint: false,
		Install: agentinstall.InstallSpec{Method: agentinstall.InstallMethodNPM, Steps: []agentinstall.InstallStep{agentinstall.NPMStep("@kilocode/cli")}},
	},
	{
		AgentID: "qoderclicn", Bin: "qoderclicn", ACPProgram: "qoderclicn", ACPArgs: []string{"--acp"}, Command: "qoderclicn --acp", EnvKey: "qoderclicn",
		HistoricalIdentifiers: []HistoricalIdentifier{
			{Kind: IdentifierKindBin, Value: "qwen"},
			{Kind: IdentifierKindEnvKey, Value: "qwen"},
			{Kind: IdentifierKindACPCommand, Value: "qwen --acp"},
		},
		DisplayName: "QCode", IconURL: "https://avatars.githubusercontent.com/u/141221163", SupportsHeadlessPrint: true,
		Install: agentinstall.InstallSpec{
			Method:         agentinstall.InstallMethodNPM,
			Steps:          []agentinstall.InstallStep{agentinstall.NPMStep("@qodercn-ai/qoderclicn")},
			UpgradeSteps:   []agentinstall.InstallStep{agentinstall.CommandStep("qoderclicn", "update")},
			VersionPackage: "@qodercn-ai/qoderclicn",
		},
	},
}

func ValidateBuiltins() error {
	owners := make(map[string]string)
	for index, agent := range builtinAgents {
		if strings.TrimSpace(agent.AgentID) == "" ||
			strings.TrimSpace(agent.Bin) == "" ||
			strings.TrimSpace(agent.ACPProgram) == "" ||
			strings.TrimSpace(agent.Command) == "" ||
			strings.TrimSpace(agent.EnvKey) == "" ||
			strings.TrimSpace(agent.DisplayName) == "" {
			return fmt.Errorf("invalid built-in Agent catalog entry at index %d: %+v", index, agent)
		}
		rendered := strings.Join(append([]string{agent.ACPProgram}, agent.ACPArgs...), " ")
		if rendered != agent.Command {
			return fmt.Errorf(
				"built-in Agent %q structured ACP definition does not match current command: structured=%q command=%q",
				agent.AgentID,
				rendered,
				agent.Command,
			)
		}
		for _, identifier := range agent.MigrationIdentifiers() {
			if owner, exists := owners[identifier]; exists && owner != agent.AgentID {
				return fmt.Errorf(
					"built-in Agent migration identifier %q is declared by both %q and %q",
					identifier,
					owner,
					agent.AgentID,
				)
			}
			owners[identifier] = agent.AgentID
		}
	}
	return nil
}

func FindBuiltinByID(agentID string) (BuiltinAgent, bool) {
	for _, agent := range builtinAgents {
		if agent.AgentID == agentID {
			return cloneBuiltin(agent), true
		}
	}
	return BuiltinAgent{}, false
}

func ResolveBuiltin(identifier string) (BuiltinAgent, bool) {
	for _, agent := range builtinAgents {
		for _, candidate := range agent.MigrationIdentifiers() {
			if identifier == candidate {
				return cloneBuiltin(agent), true
			}
		}
	}
	return BuiltinAgent{}, false
}

func BuiltinSnapshot() []BuiltinAgent {
	out := make([]BuiltinAgent, len(builtinAgents))
	for index, agent := range builtinAgents {
		out[index] = cloneBuiltin(agent)
	}
	return out
}

func cloneBuiltin(agent BuiltinAgent) BuiltinAgent {
	agent.ACPArgs = append([]string(nil), agent.ACPArgs...)
	agent.HistoricalIdentifiers = append([]HistoricalIdentifier(nil), agent.HistoricalIdentifiers...)
	agent.Install.Steps = append([]agentinstall.InstallStep(nil), agent.Install.Steps...)
	for index := range agent.Install.Steps {
		agent.Install.Steps[index].Args = append([]string(nil), agent.Install.Steps[index].Args...)
	}
	agent.Install.UpgradeSteps = append([]agentinstall.InstallStep(nil), agent.Install.UpgradeSteps...)
	for index := range agent.Install.UpgradeSteps {
		agent.Install.UpgradeSteps[index].Args = append([]string(nil), agent.Install.UpgradeSteps[index].Args...)
	}
	return agent
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
