package state

import (
	"os"
	"testing"
	"time"
)

func TestLoadMissingFile(t *testing.T) {
	s := Load("/nonexistent/state.json")
	if s == nil {
		t.Fatal("Load should return empty state, not nil")
	}
	if s.LastUpdateUnix != 0 {
		t.Errorf("LastUpdateUnix = %d, want 0", s.LastUpdateUnix)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := tempFile(t)
	defer os.Remove(path)

	st := &State{
		LastUpdateUnix:         1000,
		LastUpdateSuccess:      true,
		LastCleanupUnix:        2000,
		LearnedTodayDate:       "2026-05-15",
		LearnedTodayCount:      42,
		LearnedTotalAtMidnight: 1000,
	}

	if err := Save(path, st); err != nil {
		t.Fatal(err)
	}

	loaded := Load(path)
	if loaded.LastUpdateUnix != 1000 {
		t.Errorf("LastUpdateUnix = %d, want 1000", loaded.LastUpdateUnix)
	}
	if !loaded.LastUpdateSuccess {
		t.Error("LastUpdateSuccess should be true")
	}
	if loaded.LastCleanupUnix != 2000 {
		t.Errorf("LastCleanupUnix = %d, want 2000", loaded.LastCleanupUnix)
	}
	if loaded.LearnedTodayDate != "2026-05-15" {
		t.Errorf("LearnedTodayDate = %q, want 2026-05-15", loaded.LearnedTodayDate)
	}
	if loaded.LearnedTodayCount != 42 {
		t.Errorf("LearnedTodayCount = %d, want 42", loaded.LearnedTodayCount)
	}
	if loaded.LearnedTotalAtMidnight != 1000 {
		t.Errorf("LearnedTotalAtMidnight = %d, want 1000", loaded.LearnedTotalAtMidnight)
	}
}

func TestOverwrite(t *testing.T) {
	path := tempFile(t)
	defer os.Remove(path)

	Save(path, &State{LastUpdateUnix: 1})
	Save(path, &State{LastUpdateUnix: 2})

	loaded := Load(path)
	if loaded.LastUpdateUnix != 2 {
		t.Errorf("LastUpdateUnix = %d, want 2", loaded.LastUpdateUnix)
	}
}

func TestConcurrentSave(t *testing.T) {
	path := tempFile(t)
	defer os.Remove(path)

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			Save(path, &State{LastUpdateUnix: time.Now().Unix()})
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	// should not panic or produce corrupt output
	loaded := Load(path)
	if loaded == nil {
		t.Fatal("Load returned nil after concurrent writes")
	}
}

func TestCorruptFile(t *testing.T) {
	path := tempFile(t)
	defer os.Remove(path)

	os.WriteFile(path, []byte("{corrupt"), 0644)
	s := Load(path)
	if s == nil {
		t.Fatal("Load should return empty state on corrupt file")
	}
}

func tempFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "state-test-*.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}
