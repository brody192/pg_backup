package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brody192/pg_backup/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type endpointTestHTTPClient func(*http.Request) (*http.Response, error)

func (f endpointTestHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestS3EndpointRequest(t *testing.T) {
	previous := *config.AWS
	t.Cleanup(func() { *config.AWS = previous })
	config.AWS.S3Region = "us-east-1"
	config.AWS.S3Bucket = "test-bucket"
	config.AWS.AccessKeyID = "test"
	config.AWS.SecretAccessKey = "test"
	for _, test := range []struct {
		name, endpoint, scheme, host string
	}{
		{"bare R2 hostname", "account.r2.cloudflarestorage.com", "https", "test-bucket.account.r2.cloudflarestorage.com"},
		{"HTTPS endpoint", " https://account.r2.cloudflarestorage.com ", "https", "test-bucket.account.r2.cloudflarestorage.com"},
		{"HTTP endpoint", "http://localhost:9000", "http", "test-bucket.localhost:9000"},
		{"default AWS endpoint", "", "https", "test-bucket.s3.us-east-1.amazonaws.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config.AWS.S3Endpoint = test.endpoint
			client, err := NewS3Client()
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
				Bucket: &config.AWS.S3Bucket,
			}, func(options *s3.Options) {
				options.HTTPClient = endpointTestHTTPClient(func(request *http.Request) (*http.Response, error) {
					called = true
					if request.URL.Scheme != test.scheme || request.URL.Host != test.host {
						t.Errorf("unexpected request URL: %s", request.URL)
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/xml"}},
						Body:       io.NopCloser(strings.NewReader("<ListBucketResult/>")),
					}, nil
				})
			})
			if err != nil || !called {
				t.Fatalf("bucket check failed: %v, called=%t", err, called)
			}
		})
	}
}

func TestS3EndpointRejectsInvalidURLs(t *testing.T) {
	previous := config.AWS.S3Endpoint
	t.Cleanup(func() { config.AWS.S3Endpoint = previous })
	for _, endpoint := range []string{"https://", "http:/broken", "ftp://example.com", "https://bad host", "https://user:password@example.com", "https://example.com?token=secret", "https://example.com#fragment"} {
		config.AWS.S3Endpoint = endpoint
		if _, err := NewS3Client(); err == nil || !strings.Contains(err.Error(), "AWS_S3_ENDPOINT") {
			t.Errorf("did not reject malformed endpoint %q: %v", endpoint, err)
		}
	}
}

const multipartDumpSize = 2*manager.MinUploadPartSize + 137

// Capture only in tests so the exact object sent through the SDK can be
// decompressed and checked, including the final gzip checksum and size trailer.
type archiveS3Client struct {
	testS3Client
	mu     sync.Mutex
	parts  map[int32][]byte
	object []byte
}

func (c *archiveS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	var err error
	c.object, err = io.ReadAll(input.Body)
	return &s3.PutObjectOutput{}, err
}

func (c *archiveS3Client) UploadPart(_ context.Context, input *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.parts[aws.ToInt32(input.PartNumber)] = data
	c.mu.Unlock()
	return &s3.UploadPartOutput{ETag: aws.String("test-etag")}, nil
}

func (c *archiveS3Client) CompleteMultipartUpload(_ context.Context, input *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	for _, part := range input.MultipartUpload.Parts {
		c.object = append(c.object, c.parts[aws.ToInt32(part.PartNumber)]...)
	}
	c.completed = true
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func TestBackupUploadedGzipIntegrity(t *testing.T) {
	configureBackupTest(t)
	// Exercise parallel compression and upload, plus a final partial block.
	config.Backup.GzipConcurrency = 2
	config.Backup.UploadConcurrency = 2
	for _, scenario := range []string{"success", "multipart"} {
		t.Run(scenario, func(t *testing.T) {
			client := &archiveS3Client{parts: make(map[int32][]byte)}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dump, _, err := streamBackup(ctx, backupTestCommand(scenario), func(ctx context.Context, filename string, body io.ReadCloser) (*uploadInfo, error) {
				return uploadToS3(ctx, client, filename, body)
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "multipart" && (!client.completed || len(client.parts) < 3) {
				t.Fatalf("did not exercise multipart upload: completed=%t, parts=%d", client.completed, len(client.parts))
			}
			reader, err := gzip.NewReader(bytes.NewReader(client.object))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			actualHash := sha256.New()
			n, err := io.Copy(actualHash, reader)
			if err != nil {
				t.Fatalf("uploaded gzip is incomplete or corrupt: %v", err)
			}
			wantHash := sha256.New()
			if scenario == "multipart" {
				io.CopyN(wantHash, rand.New(rand.NewSource(1)), multipartDumpSize)
			} else {
				io.WriteString(wantHash, "database contents\n")
			}
			if !bytes.Equal(actualHash.Sum(nil), wantHash.Sum(nil)) || uint64(n) != dump.uncompressedSize {
				t.Fatal("uploaded gzip does not contain the complete dump")
			}
			if uint64(len(client.object)) != dump.compressedSize {
				t.Fatal("uploaded byte count does not include the complete gzip trailer")
			}

			if scenario == "success" {
				trailerErr := errors.New("connection failed while writing gzip trailer")
				_, _, err := streamBackup(ctx, backupTestCommand(scenario), func(_ context.Context, _ string, body io.ReadCloser) (*uploadInfo, error) {
					// Consume everything except the eight-byte CRC/size trailer,
					// then fail while gzip.Close is trying to finish the archive.
					if _, err := io.CopyN(io.Discard, body, int64(len(client.object)-8)); err != nil {
						return nil, err
					}
					return nil, trailerErr
				})
				if !errors.Is(err, trailerErr) || !strings.Contains(err.Error(), "write compressed backup stream") {
					t.Fatalf("lost gzip finalization failure: %v", err)
				}
			}
		})
	}
}

type testS3Client struct {
	manager.UploadAPIClient
	partError     error
	completeError error
	abortError    error
	aborted       bool
	completed     bool
	cleanupError  error
}

func (c *testS3Client) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("test-upload")}, nil
}

func (c *testS3Client) UploadPart(_ context.Context, input *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if c.partError != nil {
		return nil, c.partError
	}
	if _, err := io.Copy(io.Discard, input.Body); err != nil {
		return nil, err
	}
	return &s3.UploadPartOutput{ETag: aws.String("test-etag")}, nil
}

func (c *testS3Client) CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	c.completed = true
	return &s3.CompleteMultipartUploadOutput{}, c.completeError
}

func (c *testS3Client) AbortMultipartUpload(ctx context.Context, input *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	c.aborted = aws.ToString(input.UploadId) == "test-upload"
	c.cleanupError = ctx.Err()
	if _, ok := ctx.Deadline(); !ok {
		c.cleanupError = errors.New("cleanup has no deadline")
	}
	return &s3.AbortMultipartUploadOutput{}, c.abortError
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestUploadPartFailureUnblocksReaderAndAborts(t *testing.T) {
	configureBackupTest(t)
	for _, abortFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup succeeds", true: "cleanup fails"}[abortFails], func(t *testing.T) {
			partErr := errors.New("part upload failed")
			client := &testS3Client{partError: partErr}
			if abortFails {
				client.abortError = errors.New("abort request failed")
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			// Supply exactly one part and leave the source open. The SDK will
			// block reading the next part when the first upload fails.
			written := make(chan error, 1)
			go func() {
				_, err := io.CopyN(writer, zeroReader{}, manager.MinUploadPartSize)
				written <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := uploadToS3(ctx, client, "backup.gz", reader)
			if !errors.Is(err, partErr) {
				t.Fatalf("lost upload error: %v", err)
			}
			if abortFails && !errors.Is(err, client.abortError) {
				t.Fatalf("lost cleanup error: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("failed part did not unblock the reader before timeout")
			}
			if !client.aborted || client.completed || client.cleanupError != nil {
				t.Fatalf("incorrect multipart cleanup: %+v", client)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUploadSourceFailureAbortsMultipart(t *testing.T) {
	configureBackupTest(t)
	client := &testS3Client{}
	sourceErr := errors.New("gzip stream failed")
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		_, err := io.CopyN(writer, zeroReader{}, manager.MinUploadPartSize+1)
		if err != nil {
			writer.CloseWithError(err)
			return
		}
		writer.CloseWithError(sourceErr)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := uploadToS3(ctx, client, "backup.gz", reader)
	if !errors.Is(err, sourceErr) || !strings.Contains(err.Error(), "upload backup to S3") {
		t.Fatalf("lost source error: %v", err)
	}
	if !client.aborted || client.completed || client.cleanupError != nil {
		t.Fatalf("incorrect multipart cleanup: %+v", client)
	}
}

func TestUploadMultipartCompletion(t *testing.T) {
	configureBackupTest(t)
	for _, completeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "completion fails"}[completeFails], func(t *testing.T) {
			client := &testS3Client{}
			if completeFails {
				client.completeError = errors.New("complete upload failed")
			}
			body := io.NopCloser(io.LimitReader(zeroReader{}, manager.MinUploadPartSize+1))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := uploadToS3(ctx, client, "backup.gz", body)
			if !errors.Is(err, client.completeError) {
				t.Fatalf("got %v, want %v", err, client.completeError)
			}
			if !client.completed || client.aborted != completeFails || client.cleanupError != nil {
				t.Fatalf("incorrect multipart completion: %+v", client)
			}
		})
	}
}
