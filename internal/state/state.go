package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type State struct {
	LastUpdateUnix    int64  `json:"last_update_unix"`
	LastUpdateSuccess bool   `json:"last_update_success"`
	LastCleanupUnix   int64  `json:"last_cleanup_unix"`
	LearnedTodayDate  string `json:"learned_today_date"`
	LearnedTodayCount int64  `json:"learned_today_count"`
	LearnedTotalCount int64  `json:"learned_total_count"`
}

var updateMu sync.Mutex

func Load(path string) *State {
	s := &State{}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, s); err != nil {
		return &State{}
	}
	return s
}

func Save(path string, s *State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func Update(path string, fn func(*State)) error {
	updateMu.Lock()
	defer updateMu.Unlock()
	s := Load(path)
	fn(s)
	return Save(path, s)
}
