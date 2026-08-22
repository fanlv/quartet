package workspace

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/sandbox"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

type Service interface {
	Create(ws *model.Workspace) error
	Get(id string) (*model.Workspace, bool)
	List() []*model.Workspace
	Patch(id string, expectedVersion uint64, patch Patch) (*model.Workspace, error)
	ClearAgentDefaults(agentID string) error
	SetFavorite(id string, favorite bool) (*model.Workspace, error)
	Reorder(ids []string) error
	SetSandboxRef(id string, ref *model.SandboxRef) error
	Revision() uint64
	// TrustedFileWorkspaceRoots returns the set of workspace Workdirs that
	// pass internal workdir validation, used to scope file-browsing endpoints.
	TrustedFileWorkspaceRoots() []string
	// MarkDeleted flips the Deleted flag so Get / List start returning nothing
	// for this workspace. The flag is persisted to meta.json but the directory
	// and in-memory entry stay in place. Used by the handler to close the
	// TOCTOU window while it cascades Job cleanup: once this returns, no new
	// Job can bind to the workspace (resolveWorkspaceID / any Get-based lookup
	// sees it as gone), and the caller can safely enumerate and delete jobs
	// without racing new arrivals. A subsequent Delete() finalizes removal
	// (map entry + disk). Persisting the flag makes the two-phase deletion
	// crash-safe: if the process dies between MarkDeleted and Delete, LoadAll
	// on next boot skips the Deleted=true entry instead of resurrecting it.
	MarkDeleted(id string) error
	Delete(id string) error
	EnsureDefault() error
	// DefaultWorkdir returns the canonical default workdir suggested for a
	// freshly created workspace (sandbox UserHomeDir → $HOME → sandbox TempDir).
	// Used by the new-workspace UI to prefill the path picker.
	DefaultWorkdir() string
	// GitBranch returns the checked-out git branch name for dir, or "" when dir
	// is not inside a git repository or is on a detached HEAD. Used by the
	// composer's workspace tag to surface the current branch next to the path.
	GitBranch(dir string) string
	// RegenerateAllColors assigns a fresh random color to every non-deleted
	// workspace and persists the change. Returns the updated list in the same
	// order as List().
	RegenerateAllColors() ([]*model.Workspace, error)
}

var ErrVersionConflict = errors.New("workspace has been modified")

// Patch contains only fields the caller explicitly owns. Nil leaves a field
// unchanged; a non-nil empty string clears an optional field.
type Patch struct {
	Title        *string
	Description  *string
	Workdir      *string
	DefaultAgent *string
	DefaultModel *string
}

type serviceImpl struct {
	mu         sync.RWMutex
	locks      [64]sync.Mutex
	workspaces map[string]*model.Workspace
	repo       repository.WorkspaceRepo
	revision   atomic.Uint64
}

func (s *serviceImpl) lockFor(id string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	idx := h.Sum32() % uint32(len(s.locks))
	return &s.locks[idx]
}

func cloneSandboxRef(ref *model.SandboxRef) *model.SandboxRef {
	if ref == nil {
		return nil
	}
	c := *ref
	return &c
}

func cloneWorkspace(ws *model.Workspace) *model.Workspace {
	if ws == nil {
		return nil
	}
	c := *ws
	c.Sandbox = cloneSandboxRef(ws.Sandbox)
	return &c
}

func (s *serviceImpl) Revision() uint64 { return s.revision.Load() }

func (s *serviceImpl) bumpRevisionLocked() {
	s.revision.Add(1)
}

func NewService() (Service, error) {
	repo, err := repository.NewWorkspaceRepo()
	if err != nil {
		return nil, err
	}

	s := &serviceImpl{
		workspaces: make(map[string]*model.Workspace),
		repo:       repo,
	}

	if err := s.load(); err != nil {
		logger.Error("[workspace.Service] load workspaces failed: %v", err)
	}
	// Sweep residue from crashed / failed two-phase deletes. Best-effort —
	// a failure here does not block boot; the next restart will retry.
	if err := repo.SweepDeleted(); err != nil {
		logger.Error("[workspace.Service] sweep deleted workspaces failed: %v", err)
	}

	if err := s.EnsureDefault(); err != nil {
		// On first boot, an EnsureDefault failure means we cannot guarantee a
		// usable workspace context. Treat it as fatal so the service doesn't
		// start in a broken state.
		return nil, fmt.Errorf("ensure default workspace failed: %w", err)
	}

	return s, nil
}

func (s *serviceImpl) load() error {
	all, err := s.repo.LoadAll()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ws := range all {
		if ws.Version == 0 {
			ws.Version = 1
		}
		// Backfill color for legacy records persisted before the Color field
		// existed. Save-through so the value sticks across restarts; a failed
		// save still keeps the in-memory value so the UI renders consistently
		// this session.
		if ws.Color == "" {
			ws.Color = model.RandomWorkspaceColor()
			ws.UpdatedAt = time.Now()
			if err := s.repo.Save(ws.ID, ws); err != nil {
				logger.Error("[workspace.Service] backfill color for %s failed: %v", ws.ID, err)
			}
		}
		s.workspaces[ws.ID] = ws
	}
	return nil
}

func (s *serviceImpl) Create(ws *model.Workspace) error {
	copy := cloneWorkspace(ws)
	if copy == nil {
		return fmt.Errorf("workspace is nil")
	}
	if copy.Workdir == "" {
		return fmt.Errorf("invalid workdir: workdir is empty")
	}
	if !filepath.IsAbs(copy.Workdir) {
		return fmt.Errorf("invalid workdir: workdir must be absolute: %s", copy.Workdir)
	}
	if copy.Version == 0 {
		copy.Version = 1
	}
	mu := s.lockFor(copy.ID)
	mu.Lock()
	defer mu.Unlock()
	copy.SortOrder = s.nextSortOrder()

	if err := s.repo.Save(copy.ID, copy); err != nil {
		return fmt.Errorf("save workspace failed: %w", err)
	}

	s.mu.Lock()
	s.workspaces[copy.ID] = copy
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return nil
}

func (s *serviceImpl) nextSortOrder() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	maxOrder := -1
	for _, ws := range s.workspaces {
		if ws != nil && !ws.Deleted && ws.SortOrder > maxOrder {
			maxOrder = ws.SortOrder
		}
	}
	return maxOrder + 1
}

func (s *serviceImpl) Get(id string) (*model.Workspace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ws, ok := s.workspaces[id]
	if ok && ws.Deleted {
		return nil, false
	}
	return cloneWorkspace(ws), ok
}

func (s *serviceImpl) List() []*model.Workspace {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*model.Workspace
	for _, ws := range s.workspaces {
		if !ws.Deleted {
			result = append(result, cloneWorkspace(ws))
		}
	}
	sortWorkspaceList(result)
	return result
}

func sortWorkspaceList(workspaces []*model.Workspace) {
	sort.SliceStable(workspaces, func(i, j int) bool {
		if workspaces[i].Favorite != workspaces[j].Favorite {
			return workspaces[i].Favorite
		}
		if workspaces[i].SortOrder != workspaces[j].SortOrder {
			return workspaces[i].SortOrder < workspaces[j].SortOrder
		}
		if !workspaces[i].CreatedAt.Equal(workspaces[j].CreatedAt) {
			return workspaces[i].CreatedAt.Before(workspaces[j].CreatedAt)
		}
		return workspaces[i].ID < workspaces[j].ID
	})
}

func (s *serviceImpl) Patch(id string, expectedVersion uint64, patch Patch) (*model.Workspace, error) {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	s.mu.RLock()
	ws, ok := s.workspaces[id]
	if !ok || ws.Deleted {
		s.mu.RUnlock()
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	updated := cloneWorkspace(ws)
	s.mu.RUnlock()

	if expectedVersion == 0 || expectedVersion != updated.Version {
		return nil, fmt.Errorf("%w: current version=%d, expected version=%d", ErrVersionConflict, updated.Version, expectedVersion)
	}
	if patch.Title != nil {
		updated.Title = *patch.Title
	}
	if patch.Description != nil {
		updated.Description = *patch.Description
	}
	if patch.Workdir != nil {
		if *patch.Workdir == "" {
			return nil, fmt.Errorf("invalid workdir: workdir is empty")
		}
		if !filepath.IsAbs(*patch.Workdir) {
			return nil, fmt.Errorf("invalid workdir: workdir must be absolute: %s", *patch.Workdir)
		}
		updated.Workdir = *patch.Workdir
	}
	if patch.DefaultAgent != nil {
		updated.DefaultAgent = strings.TrimSpace(*patch.DefaultAgent)
	}
	if patch.DefaultModel != nil {
		updated.DefaultModel = strings.TrimSpace(*patch.DefaultModel)
	}
	updated.Version++
	updated.UpdatedAt = time.Now()

	if err := s.repo.Save(updated.ID, updated); err != nil {
		return nil, fmt.Errorf("save workspace failed: %w", err)
	}

	s.mu.Lock()
	// Re-check existence under the map lock (Delete could have removed it).
	cur, ok := s.workspaces[id]
	if !ok || cur == nil || cur.Deleted {
		s.mu.Unlock()
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	// In-place update: a pointer-replace here would clobber any field
	// another goroutine legitimately changed while we were doing disk I/O
	// (e.g. SetSandboxRef racing with Update). We only own the fields
	// this call actually modifies; everything else is left untouched.
	cur.Title = updated.Title
	cur.Description = updated.Description
	cur.Workdir = updated.Workdir
	cur.DefaultAgent = updated.DefaultAgent
	cur.DefaultModel = updated.DefaultModel
	cur.Version = updated.Version
	cur.UpdatedAt = updated.UpdatedAt
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return cloneWorkspace(updated), nil
}

// ClearAgentDefaults removes a deleted Agent from every workspace preference.
// It only owns the two preference fields, so an unrelated stale/missing
// workdir cannot block Agent deletion.
func (s *serviceImpl) ClearAgentDefaults(agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	for _, listed := range s.List() {
		if listed.DefaultAgent != agentID {
			continue
		}
		mu := s.lockFor(listed.ID)
		mu.Lock()

		s.mu.RLock()
		current, ok := s.workspaces[listed.ID]
		if !ok || current == nil || current.Deleted || current.DefaultAgent != agentID {
			s.mu.RUnlock()
			mu.Unlock()
			continue
		}
		updated := cloneWorkspace(current)
		s.mu.RUnlock()
		updated.DefaultAgent = ""
		updated.DefaultModel = ""
		updated.Version++
		updated.UpdatedAt = time.Now()
		if err := s.repo.Save(updated.ID, updated); err != nil {
			mu.Unlock()
			return fmt.Errorf("clear deleted Agent %s from workspace %s defaults: %w", agentID, listed.ID, err)
		}

		s.mu.Lock()
		current, ok = s.workspaces[listed.ID]
		if ok && current != nil && !current.Deleted && current.DefaultAgent == agentID {
			current.DefaultAgent = ""
			current.DefaultModel = ""
			current.Version = updated.Version
			current.UpdatedAt = updated.UpdatedAt
			s.bumpRevisionLocked()
		}
		s.mu.Unlock()
		mu.Unlock()
	}
	return nil
}

func (s *serviceImpl) SetFavorite(id string, favorite bool) (*model.Workspace, error) {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	s.mu.RLock()
	ws, ok := s.workspaces[id]
	if !ok || ws == nil || ws.Deleted {
		s.mu.RUnlock()
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	updated := cloneWorkspace(ws)
	s.mu.RUnlock()

	updated.Favorite = favorite
	updated.UpdatedAt = time.Now()
	if err := s.repo.Save(updated.ID, updated); err != nil {
		return nil, fmt.Errorf("save workspace failed: %w", err)
	}

	s.mu.Lock()
	cur, ok := s.workspaces[id]
	if !ok || cur == nil || cur.Deleted {
		s.mu.Unlock()
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	cur.Favorite = updated.Favorite
	cur.UpdatedAt = updated.UpdatedAt
	s.bumpRevisionLocked()
	result := cloneWorkspace(cur)
	s.mu.Unlock()
	return result, nil
}

func (s *serviceImpl) Reorder(ids []string) error {
	s.mu.RLock()
	activeCount := 0
	for _, ws := range s.workspaces {
		if ws != nil && !ws.Deleted {
			activeCount++
		}
	}
	s.mu.RUnlock()
	if len(ids) != activeCount {
		return fmt.Errorf("workspaceIds must contain all %d active workspaces, got %d", activeCount, len(ids))
	}

	seen := make(map[string]struct{}, len(ids))
	lockIndexes := make([]int, 0, len(ids))
	locked := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			return fmt.Errorf("workspaceIds contains duplicate id: %s", id)
		}
		seen[id] = struct{}{}
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		idx := int(h.Sum32() % uint32(len(s.locks)))
		if _, exists := locked[idx]; !exists {
			locked[idx] = struct{}{}
			lockIndexes = append(lockIndexes, idx)
		}
	}
	sort.Ints(lockIndexes)
	for _, idx := range lockIndexes {
		s.locks[idx].Lock()
	}
	defer func() {
		for i := len(lockIndexes) - 1; i >= 0; i-- {
			s.locks[lockIndexes[i]].Unlock()
		}
	}()

	s.mu.RLock()
	originals := make([]*model.Workspace, 0, len(ids))
	updates := make([]*model.Workspace, 0, len(ids))
	now := time.Now()
	for order, id := range ids {
		ws, ok := s.workspaces[id]
		if !ok || ws == nil || ws.Deleted {
			s.mu.RUnlock()
			return fmt.Errorf("workspace not found: %s", id)
		}
		originals = append(originals, cloneWorkspace(ws))
		updated := cloneWorkspace(ws)
		updated.SortOrder = order
		updated.UpdatedAt = now
		updates = append(updates, updated)
	}
	s.mu.RUnlock()

	for i, updated := range updates {
		if err := s.repo.Save(updated.ID, updated); err != nil {
			rollbackErrors := make([]string, 0)
			for j := 0; j < i; j++ {
				if rollbackErr := s.repo.Save(originals[j].ID, originals[j]); rollbackErr != nil {
					rollbackErrors = append(rollbackErrors, fmt.Sprintf("%s: %v", originals[j].ID, rollbackErr))
				}
			}
			if len(rollbackErrors) > 0 {
				return fmt.Errorf("save workspace order failed at %s: %w; rollback failed: %s", updated.ID, err, strings.Join(rollbackErrors, "; "))
			}
			return fmt.Errorf("save workspace order failed at %s: %w", updated.ID, err)
		}
	}

	s.mu.Lock()
	for _, updated := range updates {
		cur, ok := s.workspaces[updated.ID]
		if ok && cur != nil && !cur.Deleted {
			cur.SortOrder = updated.SortOrder
			cur.UpdatedAt = updated.UpdatedAt
		}
	}
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return nil
}

func (s *serviceImpl) SetSandboxRef(id string, ref *model.SandboxRef) error {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	s.mu.RLock()
	ws, ok := s.workspaces[id]
	if !ok || ws.Deleted {
		s.mu.RUnlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	updated := cloneWorkspace(ws)
	s.mu.RUnlock()
	updated.Sandbox = cloneSandboxRef(ref)
	updated.UpdatedAt = time.Now()
	// Use the repo's field-specific helper so concurrent updates preserve
	// other workspace fields.
	if err := s.repo.SetSandboxRef(id, updated.Sandbox); err != nil {
		return fmt.Errorf("persist sandbox ref failed: %w", err)
	}
	s.mu.Lock()
	cur, ok := s.workspaces[id]
	if !ok || cur == nil || cur.Deleted {
		s.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	cur.Sandbox = updated.Sandbox
	cur.UpdatedAt = updated.UpdatedAt
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return nil
}

func (s *serviceImpl) Delete(id string) error {
	// Defense-in-depth: reject deletion of the default workspace even when the
	// HTTP layer's check has already run. The default workspace is the
	// system-wide fallback for scheduled tasks with no explicit workspace and
	// for first-boot routing; its absence would silently break those flows.
	// Keeping the check here means any future caller (CLI, internal script,
	// tests) that bypasses the handler can't accidentally break the invariant.
	if id == consts.DefaultWorkspaceID {
		return fmt.Errorf("default workspace %s cannot be deleted", id)
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	s.mu.RLock()
	ws, ok := s.workspaces[id]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	snapshot := cloneWorkspace(ws)
	s.mu.RUnlock()

	// Remove the workspace directory. RemoveDir is idempotent for already-
	// missing directories, so a prior partial delete still succeeds on retry.
	// If it genuinely fails (permissions, busy mount, etc.), roll the Deleted
	// flag back so Get/List surface the workspace again and the user can
	// retry through the normal DELETE handler instead of being locked out by
	// the hidden-but-on-disk ghost state.
	if err := s.repo.RemoveDir(id); err != nil {
		if snapshot != nil && snapshot.Deleted {
			rolled := cloneWorkspace(snapshot)
			rolled.Deleted = false
			rolled.UpdatedAt = time.Now()
			saveErr := s.repo.Save(rolled.ID, rolled)
			if saveErr != nil {
				// Persisting the rollback failed too: keep in-memory state
				// consistent with the rollback intent (Deleted=false) so the
				// workspace remains visible for a retry. Disk may still say
				// Deleted=true and would hide it on next boot.
				logger.Error("[workspace.Service] rollback Deleted=false failed for %s: %v", id, saveErr)
			}
			// Always update memory to match the rollback intent, even if the
			// persistence layer errored out.
			s.mu.Lock()
			if cur, ok := s.workspaces[id]; ok && cur != nil {
				cur.Deleted = false
				cur.UpdatedAt = rolled.UpdatedAt
				// Rollback changes in-memory state; bump so callers refresh caches.
				s.bumpRevisionLocked()
			}
			s.mu.Unlock()
		}
		return fmt.Errorf("remove workspace dir failed: %w", err)
	}
	s.mu.Lock()
	delete(s.workspaces, id)
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return nil
}

// MarkDeleted flags the workspace as deleted so Get/List stop returning it,
// without removing the directory or the in-memory entry. Idempotent: calling
// twice is a no-op. Returns an error only if the workspace never existed.
//
// The flag is persisted so that a crash between MarkDeleted and the final
// Delete (which removes the on-disk directory) cannot let the workspace
// resurrect on next boot: LoadAll skips entries with Deleted=true.
func (s *serviceImpl) MarkDeleted(id string) error {
	// Same invariant as Delete: the default workspace must never disappear
	// from Get/List. MarkDeleted persists Deleted=true which would hide ws-1
	// from every subsequent lookup even without the physical Delete step.
	if id == consts.DefaultWorkspaceID {
		return fmt.Errorf("default workspace %s cannot be deleted", id)
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	s.mu.RLock()
	ws, ok := s.workspaces[id]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.Deleted {
		s.mu.RUnlock()
		return nil
	}
	updated := cloneWorkspace(ws)
	s.mu.RUnlock()
	updated.Deleted = true
	updated.UpdatedAt = time.Now()
	if err := s.repo.Save(updated.ID, updated); err != nil {
		// Roll back the in-memory flag so the caller sees a consistent state.
		return fmt.Errorf("persist deleted flag failed: %w", err)
	}
	s.mu.Lock()
	cur, ok := s.workspaces[id]
	if !ok || cur == nil {
		s.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	// Mark deleted in-place so Get/List immediately stop returning this
	// workspace. The disk record was already written above.
	cur.Deleted = true
	cur.UpdatedAt = updated.UpdatedAt
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return nil
}

// EnsureDefault makes sure the default workspace (consts.DefaultWorkspaceID)
// exists. If the user has deleted it out of band, it is re-created with the
// initial workdir ($HOME). Safe to call on every boot.
//
// Only heals when Workdir is EMPTY (e.g. the record was partially written on a
// previous boot's crash). A non-empty but currently-unresolvable Workdir is
// left alone: it likely reflects a deliberate user edit pointing at a
// removable drive / network mount / not-yet-created directory. Silently
// overwriting that with $HOME every boot would erase the user's choice.
// Actual dir usability is re-checked at Job creation time where a proper
// error can surface.
func (s *serviceImpl) EnsureDefault() error {
	// IMPORTANT: Always acquire the per-workspace shard lock BEFORE touching
	// the map lock. Other mutation paths (Update/Delete/MarkDeleted/Create)
	// follow the same order (shard -> map), preventing deadlocks.
	mu := s.lockFor(consts.DefaultWorkspaceID)
	mu.Lock()
	defer mu.Unlock()

	// Snapshot current state under a read lock.
	s.mu.RLock()
	cur, ok := s.workspaces[consts.DefaultWorkspaceID]
	if ok && cur != nil && !cur.Deleted {
		if cur.Workdir != "" {
			s.mu.RUnlock()
			return nil
		}
		// Heal empty workdir for an existing default workspace.
		fresh := resolveDefaultWorkdir()
		logger.Warn("[workspace.Service] default workspace workdir is empty; healing to %q", fresh)
		updated := cloneWorkspace(cur)
		s.mu.RUnlock()
		updated.Workdir = fresh
		updated.UpdatedAt = time.Now()
		if err := s.repo.Save(updated.ID, updated); err != nil {
			return fmt.Errorf("heal default workspace workdir failed: %w", err)
		}
		s.mu.Lock()
		// Under shard lock, the entry cannot be concurrently deleted/updated by
		// other writers, but still re-check for safety.
		if cur2, ok := s.workspaces[consts.DefaultWorkspaceID]; ok && cur2 != nil && !cur2.Deleted {
			cur2.Workdir = updated.Workdir
			cur2.UpdatedAt = updated.UpdatedAt
			s.bumpRevisionLocked()
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.RUnlock()

	// Create default workspace if missing.
	now := time.Now()
	ws := &model.Workspace{
		ID:        consts.DefaultWorkspaceID,
		Version:   1,
		Title:     consts.DefaultWorkspaceTitle,
		Workdir:   resolveDefaultWorkdir(),
		Color:     model.RandomWorkspaceColor(),
		SortOrder: s.nextSortOrder(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.Save(ws.ID, ws); err != nil {
		return fmt.Errorf("save default workspace failed: %w", err)
	}
	s.mu.Lock()
	s.workspaces[ws.ID] = ws
	s.bumpRevisionLocked()
	s.mu.Unlock()
	return nil
}

// RegenerateAllColors assigns a new random color to every non-deleted
// workspace (default workspace included) and persists each one. If a Save
// fails mid-loop, the offending workspace's color is rolled back in memory
// and the error is returned; workspaces that were already saved keep their
// new color.
func (s *serviceImpl) RegenerateAllColors() ([]*model.Workspace, error) {
	// Snapshot non-deleted workspace IDs so we can do disk I/O outside the map lock.
	s.mu.RLock()
	ids := make([]string, 0, len(s.workspaces))
	for id, ws := range s.workspaces {
		if ws == nil || ws.Deleted {
			continue
		}
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	now := time.Now()
	updatedAny := false
	for _, id := range ids {
		mu := s.lockFor(id)
		mu.Lock()

		s.mu.RLock()
		ws, ok := s.workspaces[id]
		if !ok || ws == nil || ws.Deleted {
			s.mu.RUnlock()
			mu.Unlock()
			continue
		}
		updated := cloneWorkspace(ws)
		s.mu.RUnlock()

		updated.Color = model.RandomWorkspaceColor()
		updated.UpdatedAt = now
		if err := s.repo.Save(updated.ID, updated); err != nil {
			mu.Unlock()
			return nil, fmt.Errorf("save workspace %s failed: %w", id, err)
		}

		s.mu.Lock()
		// Under shard lock, we should still exist, but keep a defensive re-check.
		if cur, ok := s.workspaces[id]; ok && cur != nil && !cur.Deleted {
			cur.Color = updated.Color
			cur.UpdatedAt = updated.UpdatedAt
			updatedAny = true
		}
		s.mu.Unlock()
		mu.Unlock()
	}

	if updatedAny {
		s.mu.Lock()
		s.bumpRevisionLocked()
		s.mu.Unlock()
	}

	// Build result list.
	s.mu.RLock()
	result := make([]*model.Workspace, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		if ws != nil && !ws.Deleted {
			result = append(result, cloneWorkspace(ws))
		}
	}
	s.mu.RUnlock()
	sortWorkspaceList(result)
	return result, nil
}

// FileAccessBaseRoots returns the canonical allowlist roots for workspace
// workdirs and file RW endpoints. $HOME is always included so users can
// browse and pick directories anywhere under their own home tree.
func FileAccessBaseRoots() []string {
	var roots []string
	if lm := os.Getenv("LOCAL_MEMORY"); lm != "" {
		roots = append(roots, lm)
	}
	if home := os.Getenv("HOME"); home != "" {
		roots = append(roots, home)
	}
	return roots
}

// TrustedFileWorkspaceRoots returns the set of non-deleted workspace Workdirs
// with basic valid paths, used to scope file-browsing endpoints.
func (s *serviceImpl) TrustedFileWorkspaceRoots() []string {
	workspaces := s.List()
	roots := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		if ws == nil || ws.Workdir == "" {
			continue
		}
		if !filepath.IsAbs(ws.Workdir) {
			continue
		}
		roots = append(roots, ws.Workdir)
	}
	sort.Strings(roots)
	return roots
}

// DefaultWorkdir is the public-interface wrapper around resolveDefaultWorkdir.
// It returns the same path EnsureDefault would pick for a freshly created
// default workspace, so callers (e.g. the new-workspace UI) can prefill the
// workdir picker with a consistent default.
func (s *serviceImpl) DefaultWorkdir() string {
	return resolveDefaultWorkdir()
}

// GitBranch returns the current git branch for dir. It resolves the repository
// by walking up from dir, understands the ".git file" indirection used by
// linked worktrees and submodules, and returns "" when no repository is found
// or HEAD is detached. Pure filesystem reads — no git process is spawned.
func (s *serviceImpl) GitBranch(dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	return resolveGitBranch(dir)
}

// resolveGitBranch walks up from startDir looking for a git directory and
// returns the checked-out branch name, or "" when none is found.
func resolveGitBranch(startDir string) string {
	dir := filepath.Clean(startDir)
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			gitDir := gitPath
			if !info.IsDir() {
				// ".git" is a file (linked worktree / submodule); it points at
				// the real git directory via a "gitdir: <path>" line.
				gitDir = readGitdirPointer(gitPath, dir)
			}
			if gitDir != "" {
				return branchFromHead(filepath.Join(gitDir, "HEAD"))
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// readGitdirPointer parses the "gitdir: <path>" pointer stored in a ".git"
// file and returns the absolute git directory it references (resolving
// relative pointers against baseDir).
func readGitdirPointer(gitFile, baseDir string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if target == "" || target == line {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, target)
	}
	return target
}

// branchFromHead reads a git HEAD file and returns the branch name it points
// at. A detached HEAD (raw commit hash, with no "ref:" line) yields "".
func branchFromHead(headPath string) string {
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
	if ref == "" || ref == line {
		return ""
	}
	return strings.TrimPrefix(ref, "refs/heads/")
}

// resolveDefaultWorkdir picks a writable directory for the default workspace,
// in order of preference: sandbox UserHomeDir → $HOME → sandbox TempDir. Only
// the TempDir case logs a warning, since the other two are normal.
func resolveDefaultWorkdir() string {
	sb := sandbox.GetFileManager()
	if home, err := sb.UserHomeDir(); err == nil && home.Path != "" {
		return home.Path
	}
	if envHome := os.Getenv("HOME"); envHome != "" {
		// Note: the sandbox's UserHomeDir is the preferred source so the
		// path stays within the sandbox trust boundary. Falling back to
		// the host $HOME can straddle that boundary — log so operators
		// can see it happened and intervene (set up home mount, fix
		// sandbox config, etc.) if it wasn't intentional.
		logger.Warn("[workspace.Service] sandbox UserHomeDir unavailable; defaulting workdir to host $HOME=%s", envHome)
		return envHome
	}
	// Last-resort fallback: both UserHomeDir and $HOME are unavailable
	// (happens in some container / minimal images). Use TempDir so the
	// workspace is at least writable. A Workspace with empty Workdir
	// would silently produce broken Jobs down the line.
	tmp, err := sb.TempDir()
	if err != nil || tmp.Path == "" {
		logger.Warn("[workspace.Service] no HOME and no temp dir available; default workspace workdir unresolved")
		return ""
	}
	logger.Warn("[workspace.Service] no HOME available; default workspace workdir falling back to %s", tmp.Path)
	return tmp.Path
}
