package logger

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew(t *testing.T) {
	t.Run("stdout only", func(t *testing.T) {
		log, closer := New("info", true, "")
		if log == nil {
			t.Fatal("expected non-nil logger")
		}
		closer()
	})

	t.Run("with log file", func(t *testing.T) {
		dir := t.TempDir()
		logFile := filepath.Join(dir, "test.log")

		log, closer := New("debug", false, logFile)
		if log == nil {
			t.Fatal("expected non-nil logger")
		}
		log.Info("test message")
		closer()

		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Error("log file should not be empty")
		}
	})

	t.Run("json format", func(t *testing.T) {
		dir := t.TempDir()
		logFile := filepath.Join(dir, "test.json")

		log, closer := New("info", true, logFile)
		log.Info("hello")
		closer()

		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		if data[0] != '{' {
			t.Errorf("expected JSON log, got: %s", string(data))
		}
	})

	t.Run("text format", func(t *testing.T) {
		dir := t.TempDir()
		logFile := filepath.Join(dir, "test.txt")

		log, closer := New("info", false, logFile)
		log.Info("hello")
		closer()

		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		if data[0] == '{' {
			t.Errorf("expected text log, got JSON: %s", string(data))
		}
	})

	t.Run("invalid log file", func(t *testing.T) {
		log, closer := New("info", true, "/nonexistent/dir/log.log")
		if log == nil {
			t.Fatal("expected non-nil logger even with bad file path")
		}
		log.Info("should not crash")
		closer()
	})

	t.Run("log levels", func(t *testing.T) {
		dir := t.TempDir()

		_, closer := New("error", true, filepath.Join(dir, "error.log"))
		closer()

		_, closer = New("warn", true, filepath.Join(dir, "warn.log"))
		closer()

		_, closer = New("debug", true, filepath.Join(dir, "debug.log"))
		closer()

		_, closer = New("unknown", true, filepath.Join(dir, "default.log"))
		closer()
	})
}
