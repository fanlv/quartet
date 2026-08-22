package session

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

type barrierSessionRepo struct {
	mu sync.Mutex

	persisted  map[string]model.Session
	beforeSave func(model.Session) error
	saveCount  int
}

func (r *barrierSessionRepo) Save(sessionID string, meta *model.Session) error {
	cp := *meta
	if r.beforeSave != nil {
		if err := r.beforeSave(cp); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveCount++
	if r.persisted == nil {
		r.persisted = make(map[string]model.Session)
	}
	r.persisted[sessionID] = cp
	return nil
}

func (r *barrierSessionRepo) Load(sessionID string) (*model.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta, ok := r.persisted[sessionID]
	if !ok {
		return nil, errors.New("session not found")
	}
	return &meta, nil
}

func (r *barrierSessionRepo) ListIDs() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.persisted))
	for id := range r.persisted {
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *barrierSessionRepo) LoadAll() ([]*model.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	metas := make([]*model.Session, 0, len(r.persisted))
	for _, meta := range r.persisted {
		cp := meta
		metas = append(metas, &cp)
	}
	return metas, nil
}

func (r *barrierSessionRepo) persistedSession(sessionID string) (model.Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta, ok := r.persisted[sessionID]
	return meta, ok
}

func (r *barrierSessionRepo) saves() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saveCount
}

func TestDeleteWaitsForInFlightMetadataSave(t *testing.T) {
	const sessionID = "session-race"
	updateSaveStarted := make(chan struct{})
	releaseUpdateSave := make(chan struct{})
	tombstoneSaveStarted := make(chan struct{})
	var updateOnce sync.Once
	var tombstoneOnce sync.Once

	repo := &barrierSessionRepo{
		persisted: map[string]model.Session{
			sessionID: {ID: sessionID, Title: "before", ModelID: "model-before"},
		},
	}
	repo.beforeSave = func(meta model.Session) error {
		switch {
		case meta.Deleted:
			tombstoneOnce.Do(func() { close(tombstoneSaveStarted) })
		case meta.Title == "after":
			updateOnce.Do(func() { close(updateSaveStarted) })
			<-releaseUpdateSave
		}
		return nil
	}

	svc := &serviceImpl{
		sessions: map[string]*model.Session{
			sessionID: {ID: sessionID, Title: "before", ModelID: "model-before"},
		},
		repo:       repo,
		persistKey: t.TempDir(),
	}

	updateDone := make(chan error, 1)
	go func() { updateDone <- svc.UpdateTitle(sessionID, "after") }()
	awaitSignal(t, updateSaveStarted, "metadata update did not reach Save")

	deleteCallStarted := make(chan struct{})
	deleteDone := make(chan struct{})
	go func() {
		close(deleteCallStarted)
		if err := svc.Delete(sessionID); err != nil {
			t.Errorf("Delete() error = %v", err)
		}
		close(deleteDone)
	}()
	awaitSignal(t, deleteCallStarted, "delete goroutine did not start")

	// Delete must not overtake a Save that already owns this session's
	// persistence slot. Otherwise the delayed live Save can land after the
	// tombstone (and after the Job directory is removed), recreating it.
	select {
	case <-tombstoneSaveStarted:
		t.Error("Delete reached tombstone Save before the older metadata Save completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseUpdateSave)
	if err := <-updateDone; err != nil {
		t.Fatalf("UpdateTitle() error = %v", err)
	}
	awaitSignal(t, deleteDone, "Delete did not finish after metadata Save was released")

	persisted, ok := repo.persistedSession(sessionID)
	if !ok {
		t.Fatal("persisted session missing")
	}
	if !persisted.Deleted {
		t.Fatalf("persisted session was resurrected: %+v", persisted)
	}
	if _, ok := svc.Get(sessionID); ok {
		t.Fatal("deleted session remains in memory")
	}
}

func TestDeleteFencesStaleServiceInstance(t *testing.T) {
	persistKey := t.TempDir() + "/workspace/job/sessions"
	const sessionID = "session-shared-gate"
	repo := &barrierSessionRepo{
		persisted: map[string]model.Session{
			sessionID: {ID: sessionID, Title: "before", ModelID: "model-before"},
		},
	}
	newFixture := func() *serviceImpl {
		return &serviceImpl{
			sessions: map[string]*model.Session{
				sessionID: {ID: sessionID, Title: "before", ModelID: "model-before"},
			},
			repo:       repo,
			persistKey: persistKey,
		}
	}
	deletingService := newFixture()
	staleService := newFixture()

	if err := deletingService.Delete(sessionID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	savesAfterDelete := repo.saves()
	if err := staleService.UpdateModelID(sessionID, "model-after-delete"); err != nil {
		t.Fatalf("UpdateModelID() after Delete error = %v", err)
	}
	if got := repo.saves(); got != savesAfterDelete {
		t.Fatalf("stale service persisted after Delete: saves=%d, want %d", got, savesAfterDelete)
	}
	persisted, ok := repo.persistedSession(sessionID)
	if !ok || !persisted.Deleted {
		t.Fatalf("persisted tombstone was overwritten: %+v, ok=%v", persisted, ok)
	}
}

func TestDeleteSaveFailureIsRetryable(t *testing.T) {
	const sessionID = "session-delete-retry"
	errSave := errors.New("save tombstone failed")
	failDelete := true
	repo := &barrierSessionRepo{
		persisted: map[string]model.Session{
			sessionID: {ID: sessionID, Title: "before"},
		},
	}
	repo.beforeSave = func(meta model.Session) error {
		if meta.Deleted && failDelete {
			return errSave
		}
		return nil
	}
	svc := &serviceImpl{
		sessions: map[string]*model.Session{
			sessionID: {ID: sessionID, Title: "before"},
		},
		repo:       repo,
		persistKey: t.TempDir(),
	}

	if err := svc.Delete(sessionID); !errors.Is(err, errSave) {
		t.Fatalf("Delete() error = %v, want %v", err, errSave)
	}
	if _, ok := svc.Get(sessionID); !ok {
		t.Fatal("failed Delete removed session from memory")
	}
	if persisted, _ := repo.persistedSession(sessionID); persisted.Deleted {
		t.Fatal("failed Delete unexpectedly persisted tombstone")
	}

	// A failed tombstone must not poison the gate: ordinary writes and a
	// later Delete retry remain legal because disk still says live.
	if err := svc.UpdateTitle(sessionID, "after failed delete"); err != nil {
		t.Fatalf("UpdateTitle() after failed Delete error = %v", err)
	}
	failDelete = false
	if err := svc.Delete(sessionID); err != nil {
		t.Fatalf("Delete() retry error = %v", err)
	}
	if persisted, _ := repo.persistedSession(sessionID); !persisted.Deleted {
		t.Fatal("Delete retry did not persist tombstone")
	}
}

func TestDeleteBlocksQueuedMetadataUpdate(t *testing.T) {
	const sessionID = "session-delete-first"
	deleteSaveStarted := make(chan struct{})
	releaseDeleteSave := make(chan struct{})
	var deleteOnce sync.Once
	repo := &barrierSessionRepo{
		persisted: map[string]model.Session{
			sessionID: {ID: sessionID, Title: "before"},
		},
	}
	repo.beforeSave = func(meta model.Session) error {
		if meta.Deleted {
			deleteOnce.Do(func() { close(deleteSaveStarted) })
			<-releaseDeleteSave
		}
		return nil
	}
	svc := &serviceImpl{
		sessions: map[string]*model.Session{
			sessionID: {ID: sessionID, Title: "before"},
		},
		repo:       repo,
		persistKey: t.TempDir(),
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- svc.Delete(sessionID) }()
	awaitSignal(t, deleteSaveStarted, "Delete did not reach Save")

	updateDone := make(chan error, 1)
	go func() { updateDone <- svc.UpdateTitle(sessionID, "after") }()
	select {
	case err := <-updateDone:
		t.Fatalf("UpdateTitle completed while Delete was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseDeleteSave)
	if err := <-deleteDone; err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := <-updateDone; err != nil {
		t.Fatalf("UpdateTitle() after Delete error = %v", err)
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("queued update persisted after Delete: saves=%d, want 1", got)
	}
}

func awaitSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}
