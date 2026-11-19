package awsx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

// These tests check the requests the AWS stores build and how they map
// errors, with stub clients: no network.

type stubS3 struct {
	put    *s3.PutObjectInput
	putErr error
	headIn *s3.HeadObjectInput
	head   *s3.HeadObjectOutput
	hErr   error
	getErr error
}

func (s *stubS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	s.put = in
	if in.Body != nil {
		_, _ = io.Copy(io.Discard, in.Body)
	}
	return &s3.PutObjectOutput{}, s.putErr
}

func (s *stubS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	s.headIn = in
	return s.head, s.hErr
}

func (s *stubS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, s.getErr
}

func TestS3Requests(t *testing.T) {
	ctx := context.Background()
	data, sha := randomBlob(t, 100)
	key, _ := ReleaseKey(sha)
	stub := &stubS3{}
	store := NewS3BlobStore(stub, "drop-releases-dev")

	created, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), 100, sha)
	if err != nil || !created {
		t.Fatalf("put: %v %v", created, err)
	}
	raw, _ := hex.DecodeString(sha)
	in := stub.put
	if aws.ToString(in.Bucket) != "drop-releases-dev" || aws.ToString(in.Key) != key ||
		aws.ToString(in.IfNoneMatch) != "*" || in.ChecksumAlgorithm != s3types.ChecksumAlgorithmSha256 ||
		aws.ToString(in.ChecksumSHA256) != base64.StdEncoding.EncodeToString(raw) || aws.ToInt64(in.ContentLength) != 100 {
		t.Fatalf("PutObject input = %+v", in)
	}

	stub.putErr = &smithy.GenericAPIError{Code: "PreconditionFailed"}
	if created, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), 100, sha); err != nil || created {
		t.Fatalf("412: %v %v, want false nil", created, err)
	}
	stub.putErr = &smithy.GenericAPIError{Code: "BadDigest"}
	if _, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), 100, sha); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("BadDigest: %v", err)
	}
	stub.putErr = nil
	stub.put = nil
	if _, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), 99, sha); !errors.Is(err, ErrDigestMismatch) || stub.put != nil {
		t.Fatalf("size mismatch must fail before upload: %v", err)
	}

	stub.head = &s3.HeadObjectOutput{ContentLength: aws.Int64(100), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw))}
	info, err := store.Head(ctx, key)
	if err != nil || info.SHA256 != sha || info.Size != 100 || stub.headIn.ChecksumMode != s3types.ChecksumModeEnabled {
		t.Fatalf("Head = %+v %v", info, err)
	}
	stub.head.ChecksumSHA256 = aws.String(base64.StdEncoding.EncodeToString(raw) + "-3")
	if info, _ := store.Head(ctx, key); info.SHA256 != "" {
		t.Fatalf("composite checksum gave %q", info.SHA256)
	}
	stub.hErr = &s3types.NotFound{}
	if _, err := store.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Head 404: %v", err)
	}
	stub.getErr = &s3types.NoSuchKey{}
	if _, _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get NoSuchKey: %v", err)
	}
	stub.getErr = errors.New("network down")
	if _, _, err := store.Get(ctx, key); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get other error: %v", err)
	}
}

type stubSSM struct {
	put    *ssm.PutParameterInput
	get    *ssm.GetParameterInput
	delErr error
	getErr error
}

func (s *stubSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	s.put = in
	return &ssm.PutParameterOutput{Version: 7}, nil
}

func (s *stubSSM) DeleteParameter(_ context.Context, _ *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	return &ssm.DeleteParameterOutput{}, s.delErr
}

func (s *stubSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	s.get = in
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String("v")}}, nil
}

func TestSSMRequests(t *testing.T) {
	ctx := context.Background()
	stub := &stubSSM{}
	store := NewSSMSecretStore(stub)
	name := testBase + "/TOKEN"

	v, err := store.Put(ctx, name, "SENTINEL-VALUE")
	if err != nil || v != 7 {
		t.Fatalf("Put = %d %v", v, err)
	}
	if stub.put.Type != ssmtypes.ParameterTypeSecureString || !aws.ToBool(stub.put.Overwrite) ||
		stub.put.Tier != ssmtypes.ParameterTierStandard || stub.put.KeyId != nil || aws.ToString(stub.put.Name) != name {
		t.Fatalf("PutParameter input = %+v", stub.put)
	}

	if got, err := store.Get(ctx, name, 3); err != nil || got != "v" {
		t.Fatalf("Get = %q %v", got, err)
	}
	if aws.ToString(stub.get.Name) != name+":3" || !aws.ToBool(stub.get.WithDecryption) {
		t.Fatalf("GetParameter input = %+v", stub.get)
	}
	stub.getErr = &ssmtypes.ParameterVersionNotFound{}
	if _, err := store.Get(ctx, name, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("version not found: %v", err)
	}
	stub.getErr = &ssmtypes.ParameterNotFound{}
	if _, err := store.Get(ctx, name, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("parameter not found: %v", err)
	}

	stub.delErr = &ssmtypes.ParameterNotFound{}
	if err := store.Delete(ctx, name); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	stub.delErr = errors.New("throttled")
	if err := store.Delete(ctx, name); err == nil {
		t.Fatal("Delete error swallowed")
	}
}
