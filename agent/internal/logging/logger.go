package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func New(logDir, level string) (*slog.Logger, func(), error) {
	parsed := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	}
	var writers []io.Writer
	writers = append(writers, os.Stdout)
	var file *os.File
	if strings.TrimSpace(logDir) != "" {
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return nil, func() {}, err
		}
		opened, err := os.OpenFile(filepath.Join(logDir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, func() {}, err
		}
		file = opened
		writers = append(writers, file)
	}
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: parsed}))
	cleanup := func() {
		if file != nil {
			_ = file.Close()
		}
	}
	return logger, cleanup, nil
}
