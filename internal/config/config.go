package config

import (
	"os"

	"github.com/brody192/logger"
	"github.com/caarlos0/env/v10"
)

type aws struct {
	AccessKeyID     string `env:"AWS_ACCESS_KEY_ID,required,notEmpty"`
	SecretAccessKey string `env:"AWS_SECRET_ACCESS_KEY,required,notEmpty"`
	S3Bucket        string `env:"AWS_S3_BUCKET,required,notEmpty"`
	S3Region        string `env:"AWS_S3_REGION,required,notEmpty"`
	// Default ""
	S3Endpoint string `env:"AWS_S3_ENDPOINT" envDefault:""`
}

type backup struct {
	DatabaseURL string `env:"BACKUP_DATABASE_URL,required,notEmpty"`
	// Default false
	RunOnStart bool `env:"RUN_ON_STARTUP" envDefault:"false"`
	// Default 20
	GzipConcurrency int `env:"GZIP_CONCURRENCY" envDefault:"20"`
	// Default 20
	UploadConcurrency int `env:"UPLOAD_CONCURRENCY" envDefault:"20"`
}

var (
	AWS    = &aws{}
	Backup = &backup{}
)

func init() {
	toParse := []any{AWS, Backup}
	errors := []error{}

	for _, v := range toParse {
		if err := env.Parse(v); err != nil {
			if er, ok := err.(env.AggregateError); ok {
				for _, e := range er.Errors {
					errors = append(errors, e)
				}

				continue
			}

			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		logger.Stderr.Error("Errors found while parsing environment variables", logger.ErrorsAttr(errors...))

		os.Exit(1)
	}
}
