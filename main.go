package main

import (
	"os"

	"main/internal/config"

	"github.com/brody192/logger"
)

func main() {
	if !config.Backup.RunOnStart {
		logger.Stdout.Warn("Skipping database backup, not configured to run on startup")
		os.Exit(0)
	}

	logger.Stdout.Info("Checking access to bucket...")

	if err := CheckBucketAccess(); err != nil {
		logger.Stderr.Error("Error checking bucket access", logger.ErrAttr(err))
		os.Exit(1)
	}

	logger.Stdout.Info("Access to bucket confirmed")

	logger.Stdout.Info("Starting backup...")

	if err := RunBackup(); err != nil {
		logger.Stderr.Error("Error while running backup.. Exiting..", logger.ErrAttr(err))
		os.Exit(1)
	}
}
