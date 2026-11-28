package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type TimerRepo struct {
	dir string
}

func NewTimerRepo() (*TimerRepo, error) {
	dir, err := path.TimerLedgerDir()
	if err != nil {
		return nil, err
	}
	return &TimerRepo{dir: dir}, nil
}

func (r *TimerRepo) Load(id string) (*model.TimerLedger, error) {
	if err := validateScheduleID(id); err != nil {
		return nil, err
	}
	name := filepath.Join(r.dir, id+".json")
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read timer ledger %s: %w", name, err)
	}
	var ledger model.TimerLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("decode timer ledger %s: %w", name, err)
	}
	if ledger.ID != id {
		return nil, fmt.Errorf("timer ledger %s contains ID %q", name, ledger.ID)
	}
	return &ledger, nil
}

func (r *TimerRepo) ListIDs() ([]string, error) {
	files, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			ids = append(ids, strings.TrimSuffix(file.Name(), ".json"))
		}
	}
	return ids, nil
}

func (r *TimerRepo) Save(ledger *model.TimerLedger) error {
	if err := validateScheduleID(ledger.ID); err != nil {
		return err
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.dir, 0700); err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, ledger.ID+".json"), append(data, '\n'), 0600)
}
