package state

import (
	"encoding/json"
	"os"
	"sync"
)

type State struct {
	LastUpdateUnix    int64  `json:"last_update_unix"`
	LastUpdateSuccess bool   `json:"last_update_success"`
	LastCleanupUnix   int64  `json:"last_cleanup_unix"`
	LearnedTodayDate  string `json:"learned_today_date"`
	LearnedTodayCount int64  `json:"learned_today_count"`
}

var mu sync.Mutex

func Load(path string) *State {
	mu.Lock()
	defer mu.Unlock()

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
	mu.Lock()
	defer mu.Unlock()

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
