package main

import (
	"context"

	"main/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
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

func CheckBucketAccess() error {
	if _, err := NewS3Client().ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket:  &config.AWS.S3Bucket,
		MaxKeys: aws.Int32(0),
	}); err != nil {
		return err
	}

	return nil
}
