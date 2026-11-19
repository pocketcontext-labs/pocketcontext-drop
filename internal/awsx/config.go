package awsx

import (
	"context"
	"fmt"
	"regexp"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Environment variables read by ConfigFromEnv. Credentials are never among
// them: they come from the SDK's default chain.
const (
	EnvRegion    = "DROP_AWS_REGION"
	EnvBucket    = "DROP_S3_BUCKET"
	EnvSSMPrefix = "DROP_SSM_PREFIX"
)

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Config names the AWS resources Drop uses.
type Config struct {
	Region    string // for example eu-north-1
	Bucket    string // release bucket
	SSMPrefix string // parameter prefix, default /drop
}

// ConfigFromEnv reads DROP_AWS_REGION, DROP_S3_BUCKET and DROP_SSM_PREFIX
// through getenv (os.Getenv in production).
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Region:    getenv(EnvRegion),
		Bucket:    getenv(EnvBucket),
		SSMPrefix: getenv(EnvSSMPrefix),
	}
	if cfg.SSMPrefix == "" {
		cfg.SSMPrefix = DefaultSSMPrefix
	}
	return cfg, cfg.Validate()
}

// Validate checks that every field is set and well formed.
func (c Config) Validate() error {
	if c.Region == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, EnvRegion)
	}
	if !bucketPattern.MatchString(c.Bucket) {
		return fmt.Errorf("%w: %s must be an S3 bucket name", ErrInvalid, EnvBucket)
	}
	return ValidateSSMPrefix(c.SSMPrefix)
}

// Stores bundles the blob and secret stores a process uses.
type Stores struct {
	Blobs     BlobStore
	Secrets   SecretStore
	SSMPrefix string
	// Fake is true for the in-memory and directory fakes; callers log it.
	Fake bool
	// Description names the backend for logs, without credentials.
	Description string
}

// OpenAWS builds S3 and SSM clients on the SDK's default credential chain
// (environment, shared config, then the EC2 instance profile through
// IMDSv2). It makes no network call; the first request does.
func OpenAWS(ctx context.Context, cfg Config) (*Stores, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("awsx: load AWS configuration: %w", err)
	}
	return &Stores{
		Blobs:       NewS3BlobStore(s3.NewFromConfig(awsCfg), cfg.Bucket),
		Secrets:     NewSSMSecretStore(ssm.NewFromConfig(awsCfg)),
		SSMPrefix:   cfg.SSMPrefix,
		Description: fmt.Sprintf("AWS (region %s, bucket %s, SSM prefix %s)", cfg.Region, cfg.Bucket, cfg.SSMPrefix),
	}, nil
}

// NewMemoryStores returns in-memory fakes for unit tests.
func NewMemoryStores() *Stores {
	return &Stores{
		Blobs:       NewMemBlobStore(),
		Secrets:     NewMemSecretStore(),
		SSMPrefix:   DefaultSSMPrefix,
		Fake:        true,
		Description: "in-memory fake AWS",
	}
}
