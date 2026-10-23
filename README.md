# Postgres S3 backups

A Go application built for fast PostgreSQL backups to S3 or an S3-compatible bucket, with parallel gzip compression and concurrent multipart uploads. Dumping, compression, and uploading overlap in a streaming pipeline to keep data moving without saving the full backup to disk or holding it in memory. Compression and upload concurrency are independently configurable.

## Configuration

Set the following environment variables.

- `AWS_ACCESS_KEY_ID` - Access key ID for your S3 provider. Required.
- `AWS_SECRET_ACCESS_KEY` - Secret access key for your S3 provider. Required.
- `AWS_S3_BUCKET` - Name of the bucket to upload backups to. Required.
- `AWS_S3_REGION` - Region your bucket is located in. Required. Use the region value specified by your S3 provider.
- `BACKUP_DATABASE_URL` - PostgreSQL connection string for the database to back up. Required.
- `AWS_S3_ENDPOINT` - Optional custom endpoint for S3-compatible services such as Cloudflare R2. Accepts an HTTP(S) URL or hostname, with HTTPS used by default for hostnames. Leave unset for AWS S3.
- `RUN_ON_STARTUP` - Set to `true` to run a backup when the application starts. Defaults to `false`, which skips the backup and exits.
- `GZIP_CONCURRENCY` - Number of compression blocks processed concurrently. Defaults to `20` and must be greater than zero.
- `UPLOAD_CONCURRENCY` - Number of concurrent S3 upload workers. Defaults to `20` and must be greater than zero.

Higher concurrency can improve throughput but uses more memory. Compression blocks and S3 upload parts are each 5 MiB.

The bucket credentials must allow listing the bucket, uploading objects, and aborting multipart uploads for cleanup after failures.
