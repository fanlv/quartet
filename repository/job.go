package repository

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

// validateJobID rejects empty or traversal-laden IDs so they can never be
// joined into a filesystem path. Mirrors validateTemplateID.
func validateJobID(id string) error {
	return validateID(id)
}

type JobRepo interface {
	Save(jobID string, job *model.Job) error
	Load(jobID string) (*model.Job, error)
	ListIDs() ([]string, error)
	LoadAll() ([]*model.Job, error)
}

type persistedJob struct {
	*model.Job
	ActiveClientMessageID   string                                `json:"activeClientMessageId,omitempty"`
	ClientMessageReceipts   map[string]model.ClientMessageReceipt `json:"clientMessageReceipts,omitempty"`
	CommandReceipts         map[string]model.CommandReceipt       `json:"commandReceipts,omitempty"`
	CreationClientMessageID string                                `json:"creationClientMessageId,omitempty"`
	CreationPayloadHash     string                                `json:"creationPayloadHash,omitempty"`
}

type jobRepo struct {
	sandbox fileserver.FileManager
	wsID    string
	baseDir string
	// locks shard Save per jobID so concurrent Save on different jobs don't
	// block each other while still preventing lost updates on the same job.
	locks [64]sync.Mutex
}

func (r *jobRepo) lockFor(id string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	idx := h.Sum32() % uint32(len(r.locks))
	return &r.locks[idx]
}

// NewJobRepo creates a JobRepo. Root dir: {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/.
func NewJobRepo(wsID string) (JobRepo, error) {
	baseDir := path.LocalJobsDirInWorkspace(wsID)
	sb := fileserver.GetFileManager()
	err := sb.MkDir(&fsmodel.MkDirRequest{
		Path: baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("mk dir failed: %w", err)
	}

	return &jobRepo{sandbox: sb, wsID: wsID, baseDir: baseDir}, nil
}

// ensureJobDir ensures dirs: {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/.meta/.
func (r *jobRepo) ensureJobDir(jobID string) (string, error) {
	if err := validateJobID(jobID); err != nil {
		return "", err
	}
	jobDir := path.LocalJobDirInWorkspace(r.wsID, jobID)
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: jobDir}); err != nil {
		return "", fmt.Errorf("mk job dir failed: %w", err)
	}
	metaDir := path.JobMetaDir(jobDir)
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: metaDir}); err != nil {
		return "", fmt.Errorf("mk meta dir failed: %w", err)
	}
	return jobDir, nil
}

// Save writes job metadata to {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/.meta/job.json.
// Uses atomic write (temp + rename) to prevent corruption on crash.
func (r *jobRepo) Save(jobID string, job *model.Job) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	mu := r.lockFor(jobID)
	mu.Lock()
	defer mu.Unlock()

	jobDir, err := r.ensureJobDir(jobID)
	if err != nil {
		return fmt.Errorf("ensure job dir failed: %w", err)
	}

	data, err := marshalPersistedJob(job)
	if err != nil {
		return fmt.Errorf("marshal job failed: %w", err)
	}

	metaPath := path.JobMetaFilePath(jobDir)
	if err := AtomicWriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("write job file failed: %w", err)
	}

	return nil
}

// Load reads job metadata from {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/.meta/job.json.
func (r *jobRepo) Load(jobID string) (*model.Job, error) {
	if err := validateJobID(jobID); err != nil {
		return nil, err
	}
	jobDir := path.LocalJobDirInWorkspace(r.wsID, jobID)
	metaPath := path.JobMetaFilePath(jobDir)
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: metaPath,
	})
	if err != nil {
		return nil, fmt.Errorf("read job file failed: %w", err)
	}

	job, err := unmarshalPersistedJob([]byte(result.Content))
	if err != nil {
		return nil, fmt.Errorf("unmarshal job failed: %w", err)
	}

	return job, nil
}

func marshalPersistedJob(job *model.Job) ([]byte, error) {
	return json.Marshal(persistedJob{
		Job:                     job,
		ActiveClientMessageID:   job.ActiveClientMessageID,
		ClientMessageReceipts:   job.ClientMessageReceipts,
		CommandReceipts:         job.CommandReceipts,
		CreationClientMessageID: job.CreationClientMessageID,
		CreationPayloadHash:     job.CreationPayloadHash,
	})
}

func unmarshalPersistedJob(data []byte) (*model.Job, error) {
	stored := persistedJob{Job: &model.Job{}}
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	stored.Job.ActiveClientMessageID = stored.ActiveClientMessageID
	stored.Job.ClientMessageReceipts = stored.ClientMessageReceipts
	stored.Job.CommandReceipts = stored.CommandReceipts
	stored.Job.CreationClientMessageID = stored.CreationClientMessageID
	stored.Job.CreationPayloadHash = stored.CreationPayloadHash
	return stored.Job, nil
}

// ListIDs lists subdirectories under {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/, returning those containing .meta/job.json.
func (r *jobRepo) ListIDs() ([]string, error) {
	result, err := r.sandbox.FileList(&fsmodel.FileListRequest{
		Path: r.baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("list jobs failed: %w", err)
	}

	var jobIDs []string
	for _, file := range result.Files {
		if !file.IsDir {
			continue
		}
		jobID := file.Name
		metaPath := path.JobMetaFilePath(path.LocalJobDirInWorkspace(r.wsID, jobID))
		exists, err := r.sandbox.FileExists(metaPath)
		if err != nil || !exists.Exists {
			continue
		}
		jobIDs = append(jobIDs, jobID)
	}

	return jobIDs, nil
}

// LoadAll loads all non-deleted jobs
func (r *jobRepo) LoadAll() ([]*model.Job, error) {
	jobIDs, err := r.ListIDs()
	if err != nil {
		return nil, err
	}

	var jobs []*model.Job
	for _, jobID := range jobIDs {
		job, err := r.Load(jobID)
		if err != nil {
			logger.Error("[jobRepo] load job %s failed: %v", jobID, err)
			continue
		}
		if job.Deleted {
			continue
		}
		jobs = append(jobs, job)
		// logger.Info("[jobRepo] loaded job: %s, title: %s", job.ID, job.Title)
	}

	return jobs, nil
}
