package awsx

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// S3API is the subset of the S3 client the blob store calls.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3BlobStore stores blobs as objects in one bucket. Uploads send the
// SHA-256 checksum (S3 rejects a body that does not match) and
// If-None-Match: * (an existing key answers 412 and is left untouched).
type S3BlobStore struct {
	api    S3API
	bucket string
}

// NewS3BlobStore wraps an S3 client for one bucket.
func NewS3BlobStore(api S3API, bucket string) *S3BlobStore {
	return &S3BlobStore{api: api, bucket: bucket}
}

func (s *S3BlobStore) PutIfAbsent(ctx context.Context, key string, body io.ReadSeeker, size int64, sha256hex string) (bool, error) {
	if err := checkPutArgs(key, body, size, sha256hex); err != nil {
		return false, err
	}
	end, err := body.Seek(0, io.SeekEnd)
	if err != nil {
		return false, fmt.Errorf("awsx: measure body: %w", err)
	}
	if end != size {
		return false, ErrDigestMismatch
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("awsx: rewind body: %w", err)
	}
	raw, _ := hex.DecodeString(sha256hex)
	_, err = s.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(key),
		Body:              body,
		ContentLength:     aws.Int64(size),
		ContentType:       aws.String("application/octet-stream"),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(raw)),
		IfNoneMatch:       aws.String("*"),
	})
	if err == nil {
		return true, nil
	}
	switch s3ErrorCode(err) {
	case "PreconditionFailed":
		return false, nil
	case "ConditionalRequestConflict":
		// A concurrent upload of the same key; it exists once that one lands.
		if _, herr := s.Head(ctx, key); herr == nil {
			return false, nil
		}
	case "BadDigest", "InvalidDigest", "XAmzContentSHA256Mismatch":
		return false, ErrDigestMismatch
	}
	return false, fmt.Errorf("awsx: S3 put %s: %w", key, err)
}

func (s *S3BlobStore) Head(ctx context.Context, key string) (BlobInfo, error) {
	if err := ValidateKey(key); err != nil {
		return BlobInfo{}, err
	}
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		return BlobInfo{}, s3Err("head", key, err)
	}
	return BlobInfo{Key: key, Size: aws.ToInt64(out.ContentLength), SHA256: checksumHex(out.ChecksumSHA256)}, nil
}

func (s *S3BlobStore) Get(ctx context.Context, key string) (io.ReadCloser, BlobInfo, error) {
	if err := ValidateKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		return nil, BlobInfo{}, s3Err("get", key, err)
	}
	return out.Body, BlobInfo{Key: key, Size: aws.ToInt64(out.ContentLength), SHA256: checksumHex(out.ChecksumSHA256)}, nil
}

// checksumHex converts S3's base64 full-object SHA-256 to hex. Composite
// (multipart) checksums carry a "-N" suffix and yield "".
func checksumHex(b64 *string) string {
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(b64))
	if err != nil || len(raw) != 32 {
		return ""
	}
	return hex.EncodeToString(raw)
}

func s3ErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func s3Err(op, key string, err error) error {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	var re *smithyhttp.ResponseError
	if errors.As(err, &nsk) || errors.As(err, &nf) || (errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return fmt.Errorf("awsx: S3 %s %s: %w", op, key, err)
}
