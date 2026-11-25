package awsx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Usage metering reads two things the replica bucket delivers to a separate
// log bucket, S3 server access logs and the daily S3 Inventory, and keeps one
// CloudWatch request-metrics filter per app prefix on the replica bucket. The
// server holds the log bucket read-only (List and Get) and the replica bucket
// only through its metrics configurations: never its objects.

// Environment variables of the usage collector (internal/client/serve_aws.go).
const (
	EnvUsageLogBucket       = "DROP_USAGE_LOG_BUCKET"
	EnvUsageLogPrefix       = "DROP_USAGE_LOG_PREFIX"
	EnvUsageInventoryPrefix = "DROP_USAGE_INVENTORY_PREFIX"
)

// ObjectInfo is one listed object of the log bucket.
type ObjectInfo struct {
	Key  string
	Size int64
}

// MetricsFilter is one request-metrics configuration of the replica bucket
// that filters by a key prefix.
type MetricsFilter struct {
	ID     string
	Prefix string
}

// LogReader reads the log bucket.
type LogReader interface {
	// ListObjects returns every object whose key starts with prefix, in key
	// order.
	ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error)
	// OpenObject opens one object for reading or returns ErrNotFound.
	OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
}

// MetricsFilters manages the replica bucket's per-app request-metrics
// filters. IDs are app ids (^d-[a-z0-9]+$) and the prefix is always
// "<id>/": nothing else on the bucket is touched.
type MetricsFilters interface {
	// PutPrefixFilter creates or replaces the filter id with prefix id+"/".
	PutPrefixFilter(ctx context.Context, id string) error
	// DeleteFilter removes the filter id; a missing filter is success.
	DeleteFilter(ctx context.Context, id string) error
	// ListFilters lists the bucket's filters whose ID is an app id.
	ListFilters(ctx context.Context) ([]MetricsFilter, error)
}

// UsageStore is what the usage collector holds.
type UsageStore interface {
	LogReader
	MetricsFilters
}

var (
	metricsIDPattern = regexp.MustCompile(`^d-[a-z0-9]{1,61}$`)
	logKeyPattern    = regexp.MustCompile(`^[A-Za-z0-9!_.*'()=,:@+-]+(/[A-Za-z0-9!_.*'()=,:@+-]+)*/?$`)
)

// ValidMetricsID reports whether id is an app id usable as a metrics
// configuration ID.
func ValidMetricsID(id string) bool { return metricsIDPattern.MatchString(id) }

// ValidateLogKey accepts a log bucket key or prefix: relative, at most 1024
// bytes, safe characters only, no empty, "." or ".." segment. An empty prefix
// is refused: the collector always reads below a configured prefix.
func ValidateLogKey(key string) error {
	if key == "" || len(key) > 1024 || !logKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: log key %q", ErrInvalid, key)
	}
	for _, part := range strings.Split(strings.TrimSuffix(key, "/"), "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("%w: log key %q", ErrInvalid, key)
		}
	}
	return nil
}

// UsageConfig names the collector's buckets and prefixes. LogBucket and
// LogPrefix enable it; InventoryPrefix is optional; ReplicaBucket is the
// bucket whose metrics filters it manages (DROP_CREW_REPLICA_BUCKET).
type UsageConfig struct {
	Region          string
	LogBucket       string
	LogPrefix       string // ends with "/": the access-log partition root, before YYYY/MM/DD/
	InventoryPrefix string // ends with "/": <prefix>/<source bucket>/<configuration id>/
	ReplicaBucket   string
}

// Enabled reports whether the collector is configured.
func (c UsageConfig) Enabled() bool { return c.LogBucket != "" }

// UsageConfigFromEnv reads DROP_USAGE_LOG_BUCKET, DROP_USAGE_LOG_PREFIX and
// DROP_USAGE_INVENTORY_PREFIX, plus the region and replica bucket given.
// Without DROP_USAGE_LOG_BUCKET the collector is off and the rest is ignored.
func UsageConfigFromEnv(getenv func(string) string, region, replicaBucket string) (UsageConfig, error) {
	c := UsageConfig{Region: region, LogBucket: getenv(EnvUsageLogBucket), LogPrefix: getenv(EnvUsageLogPrefix),
		InventoryPrefix: getenv(EnvUsageInventoryPrefix), ReplicaBucket: replicaBucket}
	if !c.Enabled() {
		return UsageConfig{}, nil
	}
	return c, c.Validate()
}

// Validate checks an enabled configuration.
func (c UsageConfig) Validate() error {
	if !bucketPattern.MatchString(c.LogBucket) {
		return fmt.Errorf("%w: %s must be an S3 bucket name", ErrInvalid, EnvUsageLogBucket)
	}
	if !strings.HasSuffix(c.LogPrefix, "/") || ValidateLogKey(c.LogPrefix) != nil {
		return fmt.Errorf("%w: %s must be a key prefix ending with / (the access-log partition root, for example access-logs/ACCOUNT/REGION/BUCKET/)", ErrInvalid, EnvUsageLogPrefix)
	}
	if c.InventoryPrefix != "" && (!strings.HasSuffix(c.InventoryPrefix, "/") || ValidateLogKey(c.InventoryPrefix) != nil) {
		return fmt.Errorf("%w: %s must be a key prefix ending with / (inventory/BUCKET/CONFIGURATION/)", ErrInvalid, EnvUsageInventoryPrefix)
	}
	if c.ReplicaBucket != "" && !bucketPattern.MatchString(c.ReplicaBucket) {
		return fmt.Errorf("%w: the replica bucket must be an S3 bucket name", ErrInvalid)
	}
	return nil
}

// OpenUsageAWS builds the collector's S3 client on the default credential
// chain. It makes no network call.
func OpenUsageAWS(ctx context.Context, c UsageConfig) (*S3UsageStore, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Region == "" {
		return nil, fmt.Errorf("%w: %s is required for usage metering", ErrInvalid, EnvRegion)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("awsx: load AWS configuration: %w", err)
	}
	return NewS3UsageStore(s3.NewFromConfig(awsCfg), c.LogBucket, c.ReplicaBucket), nil
}

// S3UsageAPI is the subset of the S3 client the usage store calls.
type S3UsageAPI interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutBucketMetricsConfiguration(ctx context.Context, in *s3.PutBucketMetricsConfigurationInput, opts ...func(*s3.Options)) (*s3.PutBucketMetricsConfigurationOutput, error)
	DeleteBucketMetricsConfiguration(ctx context.Context, in *s3.DeleteBucketMetricsConfigurationInput, opts ...func(*s3.Options)) (*s3.DeleteBucketMetricsConfigurationOutput, error)
	ListBucketMetricsConfigurations(ctx context.Context, in *s3.ListBucketMetricsConfigurationsInput, opts ...func(*s3.Options)) (*s3.ListBucketMetricsConfigurationsOutput, error)
}

// S3UsageStore reads the log bucket and manages the replica bucket's
// metrics filters.
type S3UsageStore struct {
	api                      S3UsageAPI
	logBucket, replicaBucket string
}

// NewS3UsageStore wraps an S3 client. replicaBucket may be empty: the
// metrics calls then fail with ErrInvalid.
func NewS3UsageStore(api S3UsageAPI, logBucket, replicaBucket string) *S3UsageStore {
	return &S3UsageStore{api: api, logBucket: logBucket, replicaBucket: replicaBucket}
}

// maxListedObjects bounds one listing so a misconfigured prefix cannot
// exhaust memory.
const maxListedObjects = 200_000

func (s *S3UsageStore) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ValidateLogKey(prefix); err != nil {
		return nil, err
	}
	var out []ObjectInfo
	var token *string
	for {
		page, err := s.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.logBucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return nil, fmt.Errorf("awsx: S3 list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			out = append(out, ObjectInfo{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size)})
		}
		if len(out) > maxListedObjects {
			return nil, fmt.Errorf("%w: more than %d objects under %s", ErrInvalid, maxListedObjects, prefix)
		}
		if !aws.ToBool(page.IsTruncated) || page.NextContinuationToken == nil {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

func (s *S3UsageStore) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidateLogKey(key); err != nil {
		return nil, err
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.logBucket), Key: aws.String(key)})
	if err != nil {
		return nil, s3Err("get", key, err)
	}
	return out.Body, nil
}

func (s *S3UsageStore) checkMetrics(id string) error {
	if s.replicaBucket == "" {
		return fmt.Errorf("%w: no replica bucket", ErrInvalid)
	}
	if id != "" && !ValidMetricsID(id) {
		return fmt.Errorf("%w: metrics id %q", ErrInvalid, id)
	}
	return nil
}

func (s *S3UsageStore) PutPrefixFilter(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty metrics id", ErrInvalid)
	}
	if err := s.checkMetrics(id); err != nil {
		return err
	}
	_, err := s.api.PutBucketMetricsConfiguration(ctx, &s3.PutBucketMetricsConfigurationInput{
		Bucket: aws.String(s.replicaBucket), Id: aws.String(id),
		MetricsConfiguration: &types.MetricsConfiguration{Id: aws.String(id), Filter: &types.MetricsFilterMemberPrefix{Value: id + "/"}},
	})
	if err != nil {
		return fmt.Errorf("awsx: S3 put metrics configuration %s: %w", id, err)
	}
	return nil
}

func (s *S3UsageStore) DeleteFilter(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty metrics id", ErrInvalid)
	}
	if err := s.checkMetrics(id); err != nil {
		return err
	}
	_, err := s.api.DeleteBucketMetricsConfiguration(ctx, &s3.DeleteBucketMetricsConfigurationInput{Bucket: aws.String(s.replicaBucket), Id: aws.String(id)})
	if err != nil && s3ErrorCode(err) != "NoSuchConfiguration" {
		return fmt.Errorf("awsx: S3 delete metrics configuration %s: %w", id, err)
	}
	return nil
}

func (s *S3UsageStore) ListFilters(ctx context.Context) ([]MetricsFilter, error) {
	if err := s.checkMetrics(""); err != nil {
		return nil, err
	}
	var out []MetricsFilter
	var token *string
	for range 1000 {
		page, err := s.api.ListBucketMetricsConfigurations(ctx, &s3.ListBucketMetricsConfigurationsInput{Bucket: aws.String(s.replicaBucket), ContinuationToken: token})
		if err != nil {
			return nil, fmt.Errorf("awsx: S3 list metrics configurations: %w", err)
		}
		for _, c := range page.MetricsConfigurationList {
			id := aws.ToString(c.Id)
			if !ValidMetricsID(id) {
				continue
			}
			f := MetricsFilter{ID: id}
			if p, ok := c.Filter.(*types.MetricsFilterMemberPrefix); ok {
				f.Prefix = p.Value
			}
			out = append(out, f)
		}
		if !aws.ToBool(page.IsTruncated) || page.NextContinuationToken == nil {
			return out, nil
		}
		token = page.NextContinuationToken
	}
	return nil, errors.New("awsx: too many metrics configuration pages")
}

// ErrorCode returns the AWS error code of err ("" when it has none), for
// metadata-only diagnostics that never carry a message.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if code := s3ErrorCode(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return "NotFound"
	case errors.Is(err, ErrInvalid):
		return "Invalid"
	case errors.Is(err, context.DeadlineExceeded):
		return "Timeout"
	}
	return "Error"
}
