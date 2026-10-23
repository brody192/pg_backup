package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/brody192/pg_backup/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func NewS3Client() (*s3.Client, error) {
	endpoint, err := normalizeS3Endpoint(config.AWS.S3Endpoint)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(aws.Config{
		Region:      config.AWS.S3Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(config.AWS.AccessKeyID, config.AWS.SecretAccessKey, "")),
	}, func(options *s3.Options) {
		// Leave endpoint resolution to the SDK when using standard AWS S3.
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}

func normalizeS3Endpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", nil
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("AWS_S3_ENDPOINT must be an HTTP(S) URL or hostname, for example https://<account-id>.r2.cloudflarestorage.com")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("AWS_S3_ENDPOINT must not contain credentials, a query string, or a fragment")
	}
	return u.String(), nil
}

func CheckBucketAccess(ctx context.Context) error {
	client, err := NewS3Client()
	if err != nil {
		return err
	}
	if _, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &config.AWS.S3Bucket,
		MaxKeys: aws.Int32(0),
	}); err != nil {
		return err
	}

	return nil
}

type uploadInfo struct {
	funcTime time.Duration
}

const uploadPartSize = 5 * 1024 * 1024

func UploadToS3(ctx context.Context, dumpFilename string, body io.ReadCloser) (*uploadInfo, error) {
	client, err := NewS3Client()
	if err != nil {
		return nil, err
	}
	return uploadToS3(ctx, client, dumpFilename, body)
}

func uploadToS3(ctx context.Context, client transfermanager.S3APIClient, dumpFilename string, body io.ReadCloser) (*uploadInfo, error) {
	if config.Backup.UploadConcurrency <= 0 {
		return nil, errors.New("UPLOAD_CONCURRENCY must be greater than zero")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// A multipart worker can fail while the uploader is blocked reading the
	// next part. Closing the pipe on cancellation interrupts that read.
	stopWatching := context.AfterFunc(ctx, func() { body.Close() })
	defer stopWatching()

	uploadClient := &cancelOnPartErrorClient{S3APIClient: client, cancel: cancel}
	uploader := transfermanager.New(uploadClient, func(u *transfermanager.Options) {
		u.PartSizeBytes = uploadPartSize
		u.MultipartUploadThreshold = uploadPartSize
		u.Concurrency = config.Backup.UploadConcurrency
		// Abort failed uploads with a fresh deadline even after cancellation.
		u.FailTimeout = 30 * time.Second
	})

	startTime := time.Now()
	if _, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		ContentType: aws.String("application/gzip"),
		Bucket:      &config.AWS.S3Bucket,
		Key:         &dumpFilename,
		Body:        body,
	}); err != nil {
		// A canceled read can race the failing worker's error in the SDK.
		// Preserve the original failure rather than only reporting a closed pipe.
		if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
		if uploadClient.abortErr != nil {
			err = errors.Join(err, uploadClient.abortErr)
		}
		return nil, fmt.Errorf("upload backup to S3: %w", err)
	}
	return &uploadInfo{funcTime: time.Since(startTime)}, nil
}

type cancelOnPartErrorClient struct {
	transfermanager.S3APIClient
	cancel   context.CancelCauseFunc
	abortErr error
}

func (c *cancelOnPartErrorClient) UploadPart(ctx context.Context, input *s3.UploadPartInput, opts ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	output, err := c.S3APIClient.UploadPart(ctx, input, opts...)
	if err != nil {
		c.cancel(err)
	}
	return output, err
}

func (c *cancelOnPartErrorClient) AbortMultipartUpload(ctx context.Context, input *s3.AbortMultipartUploadInput, opts ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	output, err := c.S3APIClient.AbortMultipartUpload(ctx, input, opts...)
	if err != nil {
		// Preserve the cleanup error for errors.Is; the SDK formats it as text.
		c.abortErr = fmt.Errorf("abort multipart upload %q: %w", aws.ToString(input.UploadId), err)
	}
	return output, err
}
