package catalog

import (
	"context"
	"testing"

	"github.com/fanlv/quartet/types/model"
)

type memoryCatalogRepo struct {
	snapshot *model.AgentCatalogSnapshot
}

func (r *memoryCatalogRepo) Load(context.Context) (*model.AgentCatalogSnapshot, error) {
	return &model.AgentCatalogSnapshot{
		Version:          r.snapshot.Version,
		Agents:           cloneCustomAgents(r.snapshot.Agents),
		BuiltinRevisions: cloneBuiltinRevisionMap(r.snapshot.BuiltinRevisions),
	}, nil
}

func (r *memoryCatalogRepo) Save(_ context.Context, snapshot *model.AgentCatalogSnapshot) error {
	r.snapshot = &model.AgentCatalogSnapshot{
		Version:          snapshot.Version,
		Agents:           cloneCustomAgents(snapshot.Agents),
		BuiltinRevisions: cloneBuiltinRevisionMap(snapshot.BuiltinRevisions),
	}
	return nil
}

func TestReconcileBuiltinRevisionsRetainsOlderDefinition(t *testing.T) {
	builtin, found := FindBuiltinByID("codex")
	if !found {
		t.Fatal("codex built-in is missing")
	}
	oldDefinition := model.AgentRuntimeDefinition{
		Bin:        builtin.Bin,
		ACPProgram: "legacy-codex-acp",
		ACPArgs:    []string{"serve"},
	}
	oldRevision := RevisionForDefinition(oldDefinition)
	repo := &memoryCatalogRepo{snapshot: &model.AgentCatalogSnapshot{
		Version: model.AgentCatalogVersion,
		Agents:  []model.CustomAgent{},
		BuiltinRevisions: map[string][]model.AgentRuntimeRevision{
			builtin.AgentID: {{
				Revision:   oldRevision,
				Definition: oldDefinition,
			}},
		},
	}}
	service := &Service{repo: repo}

	if err := service.reconcileBuiltinRevisions(context.Background()); err != nil {
		t.Fatalf("reconcileBuiltinRevisions failed: %v", err)
	}
	binding, resolved, err := service.ResolveBinding(context.Background(), builtin.AgentID, oldRevision)
	if err != nil {
		t.Fatalf("ResolveBinding retained revision failed: %v", err)
	}
	if !resolved || binding.Definition.ACPProgram != oldDefinition.ACPProgram {
		t.Fatalf("retained binding = %+v resolved=%t", binding, resolved)
	}
	current := BindingForBuiltin(builtin)
	if current.Revision == oldRevision {
		t.Fatal("test old revision unexpectedly equals current revision")
	}
	revisions, err := service.BuiltinRevisions(context.Background(), builtin.AgentID)
	if err != nil {
		t.Fatalf("BuiltinRevisions failed: %v", err)
	}
	seenOld, seenCurrent := false, false
	for _, revision := range revisions {
		seenOld = seenOld || revision.Revision == oldRevision
		seenCurrent = seenCurrent || revision.Revision == current.Revision
	}
	if !seenOld || !seenCurrent {
		t.Fatalf("revisions did not retain old and current definitions: %+v", revisions)
	}
}

