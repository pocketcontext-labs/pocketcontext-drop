package awsx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type stubUsageS3 struct {
	listIn    []*s3.ListObjectsV2Input
	pages     []*s3.ListObjectsV2Output
	put       *s3.PutBucketMetricsConfigurationInput
	deleted   *s3.DeleteBucketMetricsConfigurationInput
	deleteErr error
	configs   []s3types.MetricsConfiguration
}

func (s *stubUsageS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	s.listIn = append(s.listIn, in)
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, nil
}

func (s *stubUsageS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(aws.ToString(in.Key)))}, nil
}

func (s *stubUsageS3) PutBucketMetricsConfiguration(_ context.Context, in *s3.PutBucketMetricsConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketMetricsConfigurationOutput, error) {
	s.put = in
	return &s3.PutBucketMetricsConfigurationOutput{}, nil
}

func (s *stubUsageS3) DeleteBucketMetricsConfiguration(_ context.Context, in *s3.DeleteBucketMetricsConfigurationInput, _ ...func(*s3.Options)) (*s3.DeleteBucketMetricsConfigurationOutput, error) {
	s.deleted = in
	return &s3.DeleteBucketMetricsConfigurationOutput{}, s.deleteErr
}

func (s *stubUsageS3) ListBucketMetricsConfigurations(_ context.Context, _ *s3.ListBucketMetricsConfigurationsInput, _ ...func(*s3.Options)) (*s3.ListBucketMetricsConfigurationsOutput, error) {
	return &s3.ListBucketMetricsConfigurationsOutput{MetricsConfigurationList: s.configs}, nil
}

func TestS3UsageRequests(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageS3{pages: []*s3.ListObjectsV2Output{
		{Contents: []s3types.Object{{Key: aws.String("logs/2026/11/25/a"), Size: aws.Int64(3)}}, IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("t1")},
		{Contents: []s3types.Object{{Key: aws.String("logs/2026/11/25/b"), Size: aws.Int64(4)}}},
	}}
	store := NewS3UsageStore(stub, "logs-example", "replicas-example")
	objects, err := store.ListObjects(ctx, "logs/2026/11/25/")
	if err != nil || len(objects) != 2 || objects[1].Key != "logs/2026/11/25/b" {
		t.Fatalf("list: %v %v", objects, err)
	}
	if aws.ToString(stub.listIn[0].Bucket) != "logs-example" || aws.ToString(stub.listIn[1].ContinuationToken) != "t1" {
		t.Fatal("pagination or bucket")
	}
	if err := store.PutPrefixFilter(ctx, "d-aaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(stub.put.Bucket) != "replicas-example" || aws.ToString(stub.put.Id) != "d-aaaaaaaaaaaaaaa" ||
		stub.put.MetricsConfiguration.Filter.(*s3types.MetricsFilterMemberPrefix).Value != "d-aaaaaaaaaaaaaaa/" {
		t.Fatalf("put %+v", stub.put)
	}
	stub.deleteErr = &smithy.GenericAPIError{Code: "NoSuchConfiguration"}
	if err := store.DeleteFilter(ctx, "d-aaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("delete of a missing filter: %v", err)
	}
	stub.deleteErr = &smithy.GenericAPIError{Code: "AccessDenied"}
	if err := store.DeleteFilter(ctx, "d-aaaaaaaaaaaaaaa"); ErrorCode(err) != "AccessDenied" {
		t.Fatalf("delete error %v", err)
	}
	for _, bad := range []string{"", "whole-bucket", "d-", "d-A"} {
		if err := store.PutPrefixFilter(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("put %q: %v", bad, err)
		}
	}
	stub.configs = []s3types.MetricsConfiguration{
		{Id: aws.String("EntireBucket")},
		{Id: aws.String("d-bbbbbbbbbbbbbbb"), Filter: &s3types.MetricsFilterMemberPrefix{Value: "d-bbbbbbbbbbbbbbb/"}},
	}
	filters, err := store.ListFilters(ctx)
	if err != nil || len(filters) != 1 || filters[0].Prefix != "d-bbbbbbbbbbbbbbb/" {
		t.Fatalf("filters %v %v", filters, err)
	}
	for _, bad := range []string{"", "/abs", "a/../b", "a//b", "a b"} {
		if _, err := store.ListObjects(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("list %q: %v", bad, err)
		}
	}
}

func runUsageConformance(t *testing.T, store UsageStore, put func(key, data string)) {
	ctx := context.Background()
	put("logs/2026/11/25/b", "two")
	put("logs/2026/11/25/a", "one")
	put("logs/2026/11/24/c", "old")
	objects, err := store.ListObjects(ctx, "logs/2026/11/25/")
	if err != nil || len(objects) != 2 || objects[0].Key != "logs/2026/11/25/a" || objects[0].Size != 3 {
		t.Fatalf("list %v %v", objects, err)
	}
	rc, err := store.OpenObject(ctx, "logs/2026/11/25/b")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "two" {
		t.Fatalf("read %q", data)
	}
	if _, err := store.OpenObject(ctx, "logs/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := store.OpenObject(ctx, "../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("escape: %v", err)
	}
	if err := store.PutPrefixFilter(ctx, "d-aaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPrefixFilter(ctx, "d-aaaaaaaaaaaaaaa"); err != nil {
		t.Fatal("put is not idempotent:", err)
	}
	if err := store.PutPrefixFilter(ctx, "../x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id: %v", err)
	}
	filters, err := store.ListFilters(ctx)
	if err != nil || len(filters) != 1 || filters[0] != (MetricsFilter{ID: "d-aaaaaaaaaaaaaaa", Prefix: "d-aaaaaaaaaaaaaaa/"}) {
		t.Fatalf("filters %v %v", filters, err)
	}
	for range 2 {
		if err := store.DeleteFilter(ctx, "d-aaaaaaaaaaaaaaa"); err != nil {
			t.Fatal("delete is not idempotent:", err)
		}
	}
	if filters, _ := store.ListFilters(ctx); len(filters) != 0 {
		t.Fatalf("left %v", filters)
	}
}

func TestMemUsageConformance(t *testing.T) {
	s := NewMemUsageStore()
	runUsageConformance(t, s, func(k, d string) { s.PutObject(k, []byte(d)) })
}

func TestDirUsageConformance(t *testing.T) {
	dir := t.TempDir()
	stores, err := OpenFakeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	runUsageConformance(t, stores.Usage, func(k, d string) {
		p := filepath.Join(dir, "usage", "logs", filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(d), 0o600); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUsageConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if c, err := UsageConfigFromEnv(get, "eu-north-1", ""); err != nil || c.Enabled() {
		t.Fatalf("off by default: %+v %v", c, err)
	}
	env[EnvUsageLogBucket] = "logs-example"
	if _, err := UsageConfigFromEnv(get, "eu-north-1", ""); err == nil {
		t.Fatal("a bucket without a prefix accepted")
	}
	env[EnvUsageLogPrefix] = "access-logs/111122223333/eu-north-1/replicas-example/"
	env[EnvUsageInventoryPrefix] = "inventory/replicas-example/daily-all-versions/"
	c, err := UsageConfigFromEnv(get, "eu-north-1", "replicas-example")
	if err != nil || !c.Enabled() || c.ReplicaBucket != "replicas-example" {
		t.Fatalf("%+v %v", c, err)
	}
	env[EnvUsageInventoryPrefix] = "inventory"
	if _, err := UsageConfigFromEnv(get, "eu-north-1", ""); err == nil {
		t.Fatal("an inventory prefix without / accepted")
	}
}
