package logger

import (
	"io"
	"log/slog"
	"os"
	"sync"
)

type lockedFile struct {
	mu sync.Mutex
	f  *os.File
}

func (l *lockedFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Write(p)
}

func (l *lockedFile) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Sync()
}

func (l *lockedFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

func New(level string, jsonFormat bool, logFile string) (*slog.Logger, func()) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: l}

	var output io.Writer = os.Stdout
	var lf *lockedFile
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			os.Stderr.WriteString("warning: failed to open log file: " + err.Error() + "\n")
		} else {
			lf = &lockedFile{f: f}
			output = io.MultiWriter(os.Stdout, lf)
		}
	}

	var handler slog.Handler
	if jsonFormat {
		handler = slog.NewJSONHandler(output, opts)
	} else {
		handler = slog.NewTextHandler(output, opts)
	}

	closer := func() {
		if lf != nil {
			lf.Sync()
			lf.Close()
		}
	}

	return slog.New(handler), closer
}
