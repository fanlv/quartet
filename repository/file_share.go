package repository

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fanlv/quartet/types/path"
)

type FileShare struct {
	Token     string `json:"token"`
	Path      string `json:"path"`
	CreatedAt int64  `json:"createdAt"`
}

type FileShareRepo interface {
	Create(filePath string) (*FileShare, error)
	Get(token string) (*FileShare, bool)
	GetByPath(filePath string) (*FileShare, bool)
	Delete(token string) error
}

type fileShareRepo struct {
	mu       sync.RWMutex
	shares   map[string]*FileShare
	filePath string
}

var (
	fileShareOnce     sync.Once
	fileShareInstance FileShareRepo
)

func GetFileShareRepo() FileShareRepo {
	fileShareOnce.Do(func() {
		fp, err := path.FileSharesFile()
		if err != nil {
			panic(fmt.Sprintf("cannot resolve file shares path: %v", err))
		}
		repo := &fileShareRepo{
			shares:   make(map[string]*FileShare),
			filePath: fp,
		}
		repo.load()
		fileShareInstance = repo
	})
	return fileShareInstance
}

func (r *fileShareRepo) Create(filePath string) (*FileShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.shares {
		if s.Path == filePath {
			return s, nil
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(buf)

	share := &FileShare{
		Token:     token,
		Path:      filePath,
		CreatedAt: time.Now().UnixMilli(),
	}
	r.shares[token] = share
	if err := r.save(); err != nil {
		delete(r.shares, token)
		return nil, err
	}
	return share, nil
}

func (r *fileShareRepo) Get(token string) (*FileShare, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.shares[token]
	return s, ok
}

func (r *fileShareRepo) GetByPath(filePath string) (*FileShare, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.shares {
		if s.Path == filePath {
			return s, true
		}
	}
	return nil, false
}

func (r *fileShareRepo) Delete(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.shares[token]; !ok {
		return nil
	}
	delete(r.shares, token)
	return r.save()
}

func (r *fileShareRepo) load() {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		return
	}
	var shares []*FileShare
	if err := json.Unmarshal(data, &shares); err != nil {
		return
	}
	for _, s := range shares {
		r.shares[s.Token] = s
	}
}

func (r *fileShareRepo) save() error {
	shares := make([]*FileShare, 0, len(r.shares))
	for _, s := range r.shares {
		shares = append(shares, s)
	}
	data, err := json.Marshal(shares)
	if err != nil {
		return fmt.Errorf("marshal file shares: %w", err)
	}
	return AtomicWriteFile(r.filePath, data, 0644)
}
