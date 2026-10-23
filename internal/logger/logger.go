package logger

import (
	"log/slog"
	"os"
)

var (
	Stdout = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	Stderr = slog.New(slog.NewJSONHandler(os.Stderr, nil))
)
