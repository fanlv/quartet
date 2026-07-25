package acp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/chatctx"
	"github.com/fanlv/quartet/types/model"
)

// stubRepo is a minimal in-memory ChatContextRepo. Only CountMessage and
// MessagesFingerprint are exercised by updateSyncBaseline tests; the
// other methods exist so the stub satisfies repository.ChatContextRepo
// and chatctx.New is happy.
type stubRepo struct {
	count    int
	hash     string
	countErr error
}

func (r *stubRepo) LoadAllMessages(context.Context) ([]*schema.Message, error) { return nil, nil }
func (r *stubRepo) CountMessage(context.Context) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.count, nil
}
func (r *stubRepo) MessagesFingerprint(context.Context) (repository.MessagesFingerprint, error) {
	if r.countErr != nil {
		return repository.MessagesFingerprint{}, r.countErr
	}
	return repository.MessagesFingerprint{Count: r.count, Hash: r.hash}, nil
}
func (r *stubRepo) AppendMessages(context.Context, []*schema.Message) error  { return nil }
func (r *stubRepo) ReplaceMessages(context.Context, []*schema.Message) error { return nil }
func (r *stubRepo) ReplacePlaceholderToolResult(context.Context, string, *schema.Message) (bool, error) {
	return false, nil
}
func (r *stubRepo) WithLock(context.Context, func(repository.LockedRepo) error) error    { return nil }

// stubSessionStore captures sync-fingerprint writes so tests can assert
// whether persistence ran. Only UpdateACPSyncFingerprint is exercised by
// updateSyncBaseline; the other methods exist to satisfy SessionStore.
type stubSessionStore struct {
	mu                sync.Mutex
	fingerprintWrites []repository.MessagesFingerprint
	updateErr         error
}

func (s *stubSessionStore) Get(string) (*model.Session, bool) { return nil, false }
func (s *stubSessionStore) UpdateACPState(string, string, repository.MessagesFingerprint) error {
	return nil
}
func (s *stubSessionStore) UpdateACPSyncFingerprint(_ string, fp repository.MessagesFingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	s.fingerprintWrites = append(s.fingerprintWrites, fp)
	return nil
}
func (s *stubSessionStore) Touch(string) error { return nil }

func (s *stubSessionStore) writes() []repository.MessagesFingerprint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]repository.MessagesFingerprint, len(s.fingerprintWrites))
	copy(out, s.fingerprintWrites)
	return out
}

// newACPAgentForBaselineTest builds a bare-bones ACPAgent that only has
// the fields updateSyncBaseline touches. Constructing through NewACPAgent
// would require a live subprocess; updateSyncBaseline is a pure
// finalize-step helper so a hand-rolled instance is sufficient.
func newACPAgentForBaselineTest(repo repository.ChatContextRepo, store SessionStore, initialBaseline repository.MessagesFingerprint) *ACPAgent {
	a := &ACPAgent{
		ctxManager:   chatctx.New(repo, nil, "sess-test"),
		sessionStore: store,
		sessionID:    "sess-test",
	}
	a.storeFingerprint(initialBaseline)
	return a
}

// When persistErr is non-nil, the sync baseline MUST NOT advance —
// messages.jsonl is missing rounds the subprocess already saw, so
// recording "we are in sync" would hide that gap. Leaving the baseline
// stale lets the next Run's drift check observe and re-align it.
func TestUpdateSyncBaseline_PersistErr_LeavesBaseline(t *testing.T) {
	repo := &stubRepo{count: 5, hash: "hash-after"} // disk has 5 msgs after the failed Run
	store := &stubSessionStore{}
	prior := repository.MessagesFingerprint{Count: 2, Hash: "hash-before"} // baseline before the Run
	a := newACPAgentForBaselineTest(repo, store, prior)

	a.updateSyncBaseline(context.Background(), errors.New("flush failed"))

	if got := a.loadFingerprint(); got != prior {
		t.Errorf("baseline must stay at prior on persistErr, got %+v want %+v (advancing it would mask drift on the next Run)", got, prior)
	}
	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("session store must not be written on persistErr, got writes=%v", writes)
	}
}

// Happy path: persistErr nil → baseline advances to current disk fingerprint
// and the sync fingerprint is persisted to the session store.
func TestUpdateSyncBaseline_Success_AdvancesBaselineAndPersists(t *testing.T) {
	repo := &stubRepo{count: 7, hash: "hash-after"}
	store := &stubSessionStore{}
	prior := repository.MessagesFingerprint{Count: 3, Hash: "hash-before"}
	a := newACPAgentForBaselineTest(repo, store, prior)

	a.updateSyncBaseline(context.Background(), nil)

	want := repository.MessagesFingerprint{Count: 7, Hash: "hash-after"}
	if got := a.loadFingerprint(); got != want {
		t.Errorf("baseline should advance to current disk fingerprint, got %+v want %+v", got, want)
	}
	writes := store.writes()
	if len(writes) != 1 || writes[0] != want {
		t.Errorf("expected one persist of %+v, got %v", want, writes)
	}
}

// Fingerprint failure: leave baseline untouched — overwriting it with a
// fabricated zero would make the next Run's drift check flag a spurious
// divergence on every subsequent Run until the I/O issue clears.
func TestUpdateSyncBaseline_CountErr_KeepsBaseline(t *testing.T) {
	repo := &stubRepo{countErr: errors.New("disk read failed")}
	store := &stubSessionStore{}
	prior := repository.MessagesFingerprint{Count: 4, Hash: "hash-prior"}
	a := newACPAgentForBaselineTest(repo, store, prior)

	a.updateSyncBaseline(context.Background(), nil)

	if got := a.loadFingerprint(); got != prior {
		t.Errorf("baseline must stay at previous value on fingerprint error, got %+v want %+v", got, prior)
	}
	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("session store must not be written when fingerprint failed, got %v", writes)
	}
}

// Persisted-store write failure must not mask the in-memory baseline
// update — the in-memory field gates the very next drift check, and
// keeping it correct preserves correctness for the rest of the process
// lifetime even if disk persistence is broken.
func TestUpdateSyncBaseline_StoreWriteErr_StillUpdatesInMemory(t *testing.T) {
	repo := &stubRepo{count: 9, hash: "hash-9"}
	store := &stubSessionStore{updateErr: errors.New("disk full")}
	a := newACPAgentForBaselineTest(repo, store, repository.MessagesFingerprint{Count: 1, Hash: "hash-1"})

	a.updateSyncBaseline(context.Background(), nil)

	want := repository.MessagesFingerprint{Count: 9, Hash: "hash-9"}
	if got := a.loadFingerprint(); got != want {
		t.Errorf("in-memory baseline should still advance even when persisted-store write fails, got %+v want %+v", got, want)
	}
}

// nil sessionStore (e.g. in test or partially-wired paths) must not
// panic and must still update the in-memory baseline.
func TestUpdateSyncBaseline_NilStore_OK(t *testing.T) {
	repo := &stubRepo{count: 4, hash: "hash-4"}
	a := newACPAgentForBaselineTest(repo, nil, repository.MessagesFingerprint{})

	a.updateSyncBaseline(context.Background(), nil)

	want := repository.MessagesFingerprint{Count: 4, Hash: "hash-4"}
	if got := a.loadFingerprint(); got != want {
		t.Errorf("in-memory baseline should advance with nil store, got %+v want %+v", got, want)
	}
}

// Drift detection: same count + different hash must fire reset. This is
// exactly the case the count-only check missed —
// ReplacePlaceholderToolResult / hand edits that swap content for
// equal-length content used to slip past the drift check.
func TestFingerprint_DriftBySameCountDifferentHash(t *testing.T) {
	a := repository.MessagesFingerprint{Count: 5, Hash: "hash-a"}
	b := repository.MessagesFingerprint{Count: 5, Hash: "hash-b"}
	if a.Equal(b) {
		t.Error("same count + different hash must NOT compare equal — drift would be missed")
	}
	if !a.Equal(a) {
		t.Error("same fingerprint must compare equal")
	}
	zero := repository.MessagesFingerprint{}
	if !zero.Equal(repository.MessagesFingerprint{}) {
		t.Error("two zero values must compare equal")
	}
}
