package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/brody192/pg_backup/internal/backup"
	"github.com/brody192/pg_backup/internal/config"

	"github.com/brody192/logger"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !config.Backup.RunOnStart {
		logger.Stdout.Warn("Skipping database backup, not configured to run on startup")
		os.Exit(0)
	}

	logger.Stdout.Info("Checking access to bucket...")

	if err := backup.CheckBucketAccess(ctx); err != nil {
		logger.Stderr.Error("Error checking bucket access", logger.ErrAttr(err))
		os.Exit(1)
	}

	logger.Stdout.Info("Access to bucket confirmed")

	logger.Stdout.Info("Starting backup...")

	if err := backup.RunBackup(ctx); err != nil {
		logger.Stderr.Error("Error while running backup.. Exiting..", logger.ErrAttr(err))
		os.Exit(1)
	}
}
