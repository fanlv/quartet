package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

func RevisionForDefinition(definition model.AgentRuntimeDefinition) string {
	if len(definition.ACPArgs) == 0 {
		definition.ACPArgs = nil
	}
	data, _ := json.Marshal(definition)
	sum := sha256.Sum256(data)
	return "rev-" + hex.EncodeToString(sum[:12])
}

func RuntimeKey(agentID, revision string) string {
	return agentID + "@" + revision
}

func BindingForBuiltin(agent BuiltinAgent) model.AgentRuntimeBinding {
	definition := agent.RuntimeDefinition()
	revision := RevisionForDefinition(definition)
	return model.AgentRuntimeBinding{
		AgentID:    agent.AgentID,
		Revision:   revision,
		RuntimeKey: RuntimeKey(agent.AgentID, revision),
		Definition: definition,
	}
}

func LegacyBindingForBuiltin(agent BuiltinAgent, identifier string) (model.AgentRuntimeBinding, error) {
	if identifier == agent.Command || identifier == agent.AgentID ||
		identifier == agent.Bin || identifier == agent.EnvKey {
		return BindingForBuiltin(agent), nil
	}
	for _, historical := range agent.HistoricalIdentifiers {
		if historical.Value != identifier {
			continue
		}
		if historical.Kind != IdentifierKindACPCommand {
			return BindingForBuiltin(agent), nil
		}
		definition, err := LegacyDefinition(identifier)
		if err != nil {
			return model.AgentRuntimeBinding{}, err
		}
		definition.Bin = agent.Bin
		revision := RevisionForDefinition(definition)
		return model.AgentRuntimeBinding{
			AgentID:    agent.AgentID,
			Revision:   revision,
			RuntimeKey: RuntimeKey(agent.AgentID, revision),
			Definition: definition,
		}, nil
	}
	return model.AgentRuntimeBinding{}, fmt.Errorf(
		"identifier %q is not declared by built-in AgentID %q",
		identifier,
		agent.AgentID,
	)
}

func BindingForCustom(agent model.CustomAgent, revisionID string) (model.AgentRuntimeBinding, error) {
	if revisionID == "" {
		revisionID = agent.CurrentRevision
	}
	for _, revision := range agent.Revisions {
		if revision.Revision != revisionID {
			continue
		}
		return model.AgentRuntimeBinding{
			AgentID:    agent.AgentID,
			Revision:   revision.Revision,
			RuntimeKey: RuntimeKey(agent.AgentID, revision.Revision),
			Definition: revision.Definition,
		}, nil
	}
	return model.AgentRuntimeBinding{}, fmt.Errorf(
		"AgentID %q does not contain runtime revision %q",
		agent.AgentID,
		revisionID,
	)
}

// LegacyDefinition is only used while migrating command-string sessions. New
// records always persist a structured definition and never pass through this
// parser.
func LegacyDefinition(command string) (model.AgentRuntimeDefinition, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return model.AgentRuntimeDefinition{}, fmt.Errorf("legacy ACP command is empty")
	}
	return model.AgentRuntimeDefinition{
		Bin:        parts[0],
		ACPProgram: parts[0],
		ACPArgs:    append([]string{}, parts[1:]...),
	}, nil
}
