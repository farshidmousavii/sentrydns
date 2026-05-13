package logger

import (
	"io"
	"log/slog"
	"os"
)

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
	var file *os.File
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			os.Stderr.WriteString("warning: failed to open log file: " + err.Error() + "\n")
		} else {
			file = f
			output = io.MultiWriter(os.Stdout, f)
		}
	}

	var handler slog.Handler
	if jsonFormat {
		handler = slog.NewJSONHandler(output, opts)
	} else {
		handler = slog.NewTextHandler(output, opts)
	}

	closer := func() {
		if file != nil {
			file.Sync()
			file.Close()
		}
	}

	return slog.New(handler), closer
}
