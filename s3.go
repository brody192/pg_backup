package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/brody192/pg_backup/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func NewS3Client() *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region: config.AWS.S3Region,
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(func(_, _ string, _ ...any) (aws.Endpoint, error) {
			endpoint := aws.Endpoint{SigningRegion: config.AWS.S3Region}

			if config.AWS.S3Endpoint != "" {
				endpoint.URL = config.AWS.S3Endpoint
			}

			return endpoint, nil
		}),
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(config.AWS.AccessKeyID, config.AWS.SecretAccessKey, "")),
	})
}

func CheckBucketAccess(ctx context.Context) error {
	if _, err := NewS3Client().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
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

func UploadToS3(ctx context.Context, dumpFilename string, body io.ReadCloser) (*uploadInfo, error) {
	return uploadToS3(ctx, NewS3Client(), dumpFilename, body)
}

func uploadToS3(ctx context.Context, client manager.UploadAPIClient, dumpFilename string, body io.ReadCloser) (*uploadInfo, error) {
	if config.Backup.UploadConcurrency <= 0 {
		return nil, errors.New("UPLOAD_CONCURRENCY must be greater than zero")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// A multipart worker can fail while the uploader is blocked reading the
	// next part. Closing the pipe on cancellation interrupts that read.
	stopWatching := context.AfterFunc(ctx, func() { body.Close() })
	defer stopWatching()

	uploader := manager.NewUploader(cancelOnPartErrorClient{client, cancel}, func(u *manager.Uploader) {
		u.PartSize = 5 * 1024 * 1024
		u.Concurrency = config.Backup.UploadConcurrency
		// The SDK uses the upload context for aborts and discards abort errors.
		// Handle cleanup below with a fresh deadline, even after cancellation.
		u.LeavePartsOnError = true
	})

	startTime := time.Now()
	if _, err := uploader.Upload(ctx, &s3.PutObjectInput{
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
		var multipartErr manager.MultiUploadFailure
		if errors.As(err, &multipartErr) && multipartErr.UploadID() != "" {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cleanupCancel()
			if _, abortErr := client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{
				Bucket:   &config.AWS.S3Bucket,
				Key:      &dumpFilename,
				UploadId: aws.String(multipartErr.UploadID()),
			}); abortErr != nil {
				err = errors.Join(err, fmt.Errorf("abort multipart upload %q: %w", multipartErr.UploadID(), abortErr))
			}
		}
		return nil, fmt.Errorf("upload backup to S3: %w", err)
	}
	return &uploadInfo{funcTime: time.Since(startTime)}, nil
}

type cancelOnPartErrorClient struct {
	manager.UploadAPIClient
	cancel context.CancelCauseFunc
}

func (c cancelOnPartErrorClient) UploadPart(ctx context.Context, input *s3.UploadPartInput, opts ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	output, err := c.UploadAPIClient.UploadPart(ctx, input, opts...)
	if err != nil {
		c.cancel(err)
	}
	return output, err
}
