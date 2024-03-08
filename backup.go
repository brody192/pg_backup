package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"main/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/brody192/logger"
	"github.com/klauspost/pgzip"
	"github.com/miolini/datacounter"
)

func RunBackup() error {
	logger.Stdout.Info("Dumping database to disk...")

	sTime := time.Now()

	dump, err := dumpToFile()
	if err != nil {
		return err
	}

	dumpedLog := logger.Stdout.With(
		slog.String("filename", dump.filename),
		logger.SizeAttrIEC(dump.compressedSize, "compressed_filesize"),
		logger.SizeAttrIEC(dump.uncompressedSize, "un_compressed_filesize"),
		logger.DurationAttr(dump.funcTime),
	)

	if dump.stderr != "" {
		dumpedLog.Warn("Database dumped with warnings",
			slog.String("stderr", dump.stderr),
		)
	} else {
		dumpedLog.Info("Database dumped")
	}

	dumpFile, err := os.Open(dump.filepath)
	if err != nil {
		return err
	}

	defer dumpFile.Close()

	logger.Stdout.Info("Uploading backup to S3...")

	uploadInfo, err := UploadToS3(dump.filename, dumpFile)
	if err != nil {
		return err
	}

	logger.Stdout.Info("Backup Uploaded",
		slog.String("filename_in_bucket", dump.filename),
		logger.DurationAttr(uploadInfo.funcTime),
		logger.SizeAttrIEC(dump.compressedSize, "uploaded_size"),
	)

	logger.Stdout.Info("Deleting local file...")

	dumpFile.Close()

	if err := os.Remove(dump.filepath); err != nil {
		return err
	}

	logger.Stdout.Info("Local file deleted")

	memStats := runtime.MemStats{}
	runtime.ReadMemStats(&memStats)

	logger.Stdout.Info("Database backup complete",
		logger.DurationAttr(time.Since(sTime)),
		slog.Group("mem_stats",
			logger.SizeAttrIEC(memStats.TotalAlloc, "total_alloc"),
			logger.SizeAttrIEC(memStats.Sys, "sys"),
		),
	)

	return nil
}

type dumpInfo struct {
	basename         string
	filename         string
	filepath         string
	compressedSize   uint64
	uncompressedSize uint64
	stderr           string
	funcTime         time.Duration
}

func dumpToFile() (*dumpInfo, error) {
	dump := &dumpInfo{}

	timeNowUTC := time.Now().UTC()

	timestamp := timeNowUTC.Format("Mon_02_Jan_2006_15-04-05_MST")
	dump.basename = fmt.Sprintf("Backup_%s.dump", timestamp)
	dump.filename = fmt.Sprintf("%s.gz", dump.basename)
	dump.filepath = filepath.Join(os.TempDir(), dump.filename)

	dumpFile, err := os.Create(dump.filepath)
	if err != nil {
		return nil, err
	}

	defer dumpFile.Close()

	pgDumpCmd := exec.Command("pg_dump",
		"-d", config.Backup.DatabaseURL,
		"-F", "custom",
		"-Z", "0",
	)
	if pgDumpCmd.Err != nil {
		return nil, pgDumpCmd.Err
	}

	dumpFileCompressedCounter := datacounter.NewWriterCounter(dumpFile)

	dumpGziper := pgzip.NewWriter(dumpFileCompressedCounter)

	dumpGziper.UncompressedSize()

	dumpGziper.SetConcurrency((5 * 1024 * 1024), config.Backup.GzipConcurrency)

	dumpGziper.Header = pgzip.Header{
		Name:    dump.basename,
		ModTime: timeNowUTC,
	}

	dumpFileUnCompressedCounter := datacounter.NewWriterCounter(dumpGziper)

	pgDumpCmd.Stdout = dumpFileUnCompressedCounter

	stderr := &strings.Builder{}

	pgDumpCmd.Stderr = stderr

	startTime := time.Now()

	if err := pgDumpCmd.Run(); err != nil {
		dumpGziper.Close()
		dumpFile.Close()

		os.Remove(dump.filepath)

		if stderr.Len() > 0 {
			return nil, getPgDumpError(stderr.String())
		}

		return nil, err
	}

	dumpGziper.Flush()
	dumpGziper.Close()

	dump.funcTime = time.Since(startTime)

	if dumpFileUnCompressedCounter.Count() == 0 {
		dumpFile.Close()

		os.Remove(dump.filepath)

		if stderr.Len() > 0 {
			return nil, getPgDumpError(stderr.String())
		}

		return nil, fmt.Errorf("backup wrote 0 bytes")
	}

	dump.compressedSize = dumpFileCompressedCounter.Count()
	dump.uncompressedSize = dumpFileUnCompressedCounter.Count()
	dump.stderr = strings.TrimSpace(stderr.String())

	return dump, nil
}

type uploadInfo struct {
	funcTime time.Duration
	size     uint64
}

func UploadToS3(dumpFilename string, body io.ReadCloser) (*uploadInfo, error) {
	info := &uploadInfo{}

	uploader := manager.NewUploader(NewS3Client(), func(u *manager.Uploader) {
		u.PartSize = 5 * 1024 * 1024
		u.Concurrency = config.Backup.UploadConcurrency
	})

	startTime := time.Now()

	if _, err := uploader.Upload(context.Background(), &s3.PutObjectInput{
		ContentType: aws.String("application/gzip"),
		Bucket:      &config.AWS.S3Bucket,
		Key:         &dumpFilename,
		Body:        body,
	}); err != nil {
		return nil, err
	}

	info.funcTime = time.Since(startTime)

	return info, nil
}

// looks for error text and returns it, stripping away any potential prefixed warnings.
//
// if no error text is found, the original string is returned.
func getPgDumpError(stderr string) error {
	const errorSearchText = "pg_dump: error:"

	if index := strings.Index(stderr, errorSearchText); index != -1 {
		return errors.New(stderr[index:])
	}

	return errors.New(stderr)
}
