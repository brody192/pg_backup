package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/brody192/pg_backup/internal/config"
)

// Run a real subprocess so failures exercise command cancellation and reaping.
func TestBackupProcess(t *testing.T) {
	if os.Getenv("PG_BACKUP_TEST_PROCESS") != "1" {
		return
	}
	scenario := os.Args[len(os.Args)-1]
	switch scenario {
	case "success", "failure", "blocked":
		fmt.Fprint(os.Stdout, "database contents\n")
	}
	switch scenario {
	case "success":
		fmt.Fprint(os.Stderr, "pg_dump: warning: test warning\n")
	case "failure":
		fmt.Fprint(os.Stderr, strings.Repeat("warning\n", 20000))
		fmt.Fprint(os.Stderr, "pg_dump: error: database connection lost\n")
		os.Exit(7)
	case "blocked":
		time.Sleep(time.Hour)
	case "multipart":
		if _, err := io.CopyN(os.Stdout, rand.New(rand.NewSource(1)), multipartDumpSize); err != nil {
			os.Exit(8)
		}
	}
	os.Exit(0)
}

func backupTestCommand(scenario string) func(context.Context) *exec.Cmd {
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackupProcess$", "--", scenario)
		cmd.Env = append(os.Environ(), "PG_BACKUP_TEST_PROCESS=1")
		return cmd
	}
}

func configureBackupTest(t *testing.T) {
	t.Helper()
	previous := *config.Backup
	config.Backup.GzipConcurrency = 1
	config.Backup.UploadConcurrency = 1
	t.Cleanup(func() { *config.Backup = previous })
}

func TestStreamBackup(t *testing.T) {
	configureBackupTest(t)
	for _, test := range []struct {
		name    string
		wantErr string
	}{
		{name: "success"},
		{name: "failure", wantErr: "database connection lost"},
		{name: "empty", wantErr: "pg_dump wrote 0 bytes"},
		{name: "missing command", wantErr: "run pg_dump"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			newCommand := backupTestCommand(test.name)
			if test.name == "missing command" {
				newCommand = func(ctx context.Context) *exec.Cmd {
					return exec.CommandContext(ctx, "/nonexistent/pg_dump")
				}
			}
			committed := false
			var contents strings.Builder
			dump, _, err := streamBackup(ctx, newCommand, func(_ context.Context, filename string, body io.ReadCloser) (*uploadInfo, error) {
				reader, err := gzip.NewReader(body)
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				if filename != reader.Name+".gz" {
					t.Errorf("gzip name %q does not match key %q", reader.Name, filename)
				}
				if _, err := io.Copy(&contents, reader); err != nil {
					return nil, err
				}
				committed = true
				return &uploadInfo{}, nil
			})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, test.wantErr)
				}
				if committed {
					t.Fatal("committed a failed or empty dump")
				}
				if test.name == "failure" {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
						t.Fatalf("lost pg_dump exit status: %v", err)
					}
					if len(err.Error()) > 4*maxStderrBytes {
						t.Fatal("stderr was not bounded")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !committed || contents.String() != "database contents\n" {
				t.Fatalf("incorrect uploaded dump: %q", contents.String())
			}
			if dump.uncompressedSize != uint64(contents.Len()) || dump.compressedSize == 0 {
				t.Fatalf("incorrect byte counts: %+v", dump)
			}
			if dump.stderr != "pg_dump: warning: test warning" {
				t.Fatalf("lost pg_dump warning: %q", dump.stderr)
			}
		})
	}
}

func TestStreamBackupStopsBlockedDump(t *testing.T) {
	configureBackupTest(t)
	for _, cancelBackup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancellation=%t", cancelBackup), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			uploadErr := errors.New("S3 connection lost")
			var cmd *exec.Cmd
			newCommand := func(ctx context.Context) *exec.Cmd {
				cmd = backupTestCommand("blocked")(ctx)
				return cmd
			}
			_, _, err := streamBackup(ctx, newCommand, func(_ context.Context, _ string, body io.ReadCloser) (*uploadInfo, error) {
				// Wait for pg_dump to start and the gzip header to reach the pipe.
				if _, err := io.ReadFull(body, make([]byte, 10)); err != nil {
					return nil, err
				}
				if cancelBackup {
					cancel()
					_, err := io.Copy(io.Discard, body)
					return nil, err
				}
				return nil, uploadErr
			})
			wantErr := uploadErr
			if cancelBackup {
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("got %v, want %v", err, wantErr)
			}
			if cmd.ProcessState == nil {
				t.Fatal("returned without reaping pg_dump")
			}
		})
	}
}

func TestStreamBackupRejectsInvalidConcurrency(t *testing.T) {
	configureBackupTest(t)
	for _, gzipInvalid := range []bool{false, true} {
		config.Backup.GzipConcurrency = 1
		config.Backup.UploadConcurrency = 1
		if gzipInvalid {
			config.Backup.GzipConcurrency = 0
		} else {
			config.Backup.UploadConcurrency = -1
		}
		_, _, err := streamBackup(context.Background(), func(context.Context) *exec.Cmd {
			t.Fatal("started pg_dump with invalid configuration")
			return nil
		}, nil)
		if err == nil {
			t.Fatal("accepted invalid concurrency")
		}
	}
}
