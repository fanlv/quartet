package repository

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fanlv/quartet/types/model"
	quartetpath "github.com/fanlv/quartet/types/path"
)

const personalChatSegmentBytes = 32 << 20

type PersonalChatRepo struct {
	mu       sync.Mutex
	file     *os.File
	filename string
	lock     *os.File
}

func NewPersonalChatRepo() (*PersonalChatRepo, []model.PersonalChatCommit, []string, error) {
	filename, err := quartetpath.PersonalChatJournal()
	if err != nil {
		return nil, nil, nil, err
	}
	if err = os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, nil, nil, err
	}
	lock, err := os.OpenFile(filename+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = lockPersonalChatFile(lock); err != nil {
		lock.Close()
		return nil, nil, nil, fmt.Errorf("lock personal chat journal %s: %w", filename, err)
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, nil, err
	}
	r := &PersonalChatRepo{file: f, filename: filename, lock: lock}
	segments, err := filepath.Glob(filepath.Join(filepath.Dir(filename), "commits-*.jsonl"))
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	sort.Strings(segments)
	segments = append(segments, filename)
	var commits []model.PersonalChatCommit
	var warnings []string
	for _, name := range segments {
		source := f
		if name != filename {
			source, err = os.Open(name)
			if err != nil {
				f.Close()
				return nil, nil, nil, err
			}
		}
		loaded, notes, readErr := r.readSegment(source, name, name == filename, uint64(len(commits)))
		if name != filename {
			source.Close()
		}
		if readErr != nil {
			f.Close()
			return nil, nil, nil, readErr
		}
		commits = append(commits, loaded...)
		warnings = append(warnings, notes...)
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	success = true
	return r, commits, warnings, nil
}

func (r *PersonalChatRepo) readSegment(f *os.File, name string, repairTail bool, previous uint64) ([]model.PersonalChatCommit, []string, error) {
	reader := bufio.NewReader(f)
	var offset int64
	var commits []model.PersonalChatCommit
	var warnings []string
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if errors.Is(readErr, io.EOF) {
				if !repairTail {
					return nil, nil, fmt.Errorf("incomplete immutable personal chat segment %s at byte %d", name, offset)
				}
				backup := fmt.Sprintf("%s.incomplete.%d", name, time.Now().UnixNano())
				if err := os.WriteFile(backup, line, 0600); err != nil {
					return nil, nil, err
				}
				if err := f.Truncate(offset); err != nil {
					return nil, nil, err
				}
				if err := f.Sync(); err != nil {
					return nil, nil, err
				}
				warnings = append(warnings, fmt.Sprintf("Incomplete uncommitted JSONL tail preserved at %s; recovered through byte %d", backup, offset))
				break
			}
			var c model.PersonalChatCommit
			if err := json.Unmarshal(bytes.TrimSpace(line), &c); err != nil {
				return nil, nil, fmt.Errorf("read %s at byte %d: %w", name, offset, err)
			}
			if c.Version != 1 || c.Seq != previous+uint64(len(commits))+1 {
				return nil, nil, fmt.Errorf("invalid personal chat journal version/sequence in %s at byte %d: version=%d seq=%d", name, offset, c.Version, c.Seq)
			}
			commits = append(commits, c)
			offset += int64(len(line))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	return commits, warnings, nil
}

func (r *PersonalChatRepo) Append(c model.PersonalChatCommit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	offset, err := r.file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if offset > 0 && offset+int64(len(b)) > personalChatSegmentBytes {
		if err = r.rotate(c.Seq - 1); err != nil {
			return fmt.Errorf("rotate personal chat journal: %w", err)
		}
		offset = 0
	}
	if _, err = r.file.Write(b); err == nil {
		err = r.file.Sync()
	}
	if err != nil {
		return errors.Join(fmt.Errorf("append personal chat journal: %w", err), r.file.Truncate(offset), r.file.Sync())
	}
	return nil
}
func (r *PersonalChatRepo) rotate(lastSeq uint64) error {
	if err := r.file.Sync(); err != nil {
		return err
	}
	if err := r.file.Close(); err != nil {
		return err
	}
	segment := filepath.Join(filepath.Dir(r.filename), fmt.Sprintf("commits-%020d.jsonl", lastSeq))
	if err := os.Rename(r.filename, segment); err != nil {
		return err
	}
	f, err := os.OpenFile(r.filename, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	r.file = f
	return syncPersonalChatDir(filepath.Dir(r.filename))
}
func (r *PersonalChatRepo) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.file.Close(), r.lock.Close())
}

func NewPersonalChatContextRepo(sessionID string) (PagedChatContextRepo, error) {
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	dir, err := quartetpath.PersonalChatSessionDir(sessionID)
	if err != nil {
		return nil, err
	}
	return newChatContextRepoAt(dir)
}
