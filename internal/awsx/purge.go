package awsx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// AppPurger is the control plane's deletion-only view. The replica prefix
// and release key are derived here from validated IDs and digests.
type AppPurger interface {
	PurgeReplica(context.Context, string, string) error
	PurgeRelease(context.Context, string) error
}
type versionClient interface {
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

func (s *S3BlobStore) PurgeReplica(ctx context.Context, bucket, id string) error {
	if !bucketPattern.MatchString(bucket) || !appIDPattern.MatchString(id) || bucket == s.bucket {
		return ErrInvalid
	}
	return s.purge(ctx, bucket, id+"/", false)
}
func (s *S3BlobStore) PurgeRelease(ctx context.Context, digest string) error {
	key, err := ReleaseKey(digest)
	if err != nil {
		return err
	}
	return s.purge(ctx, s.bucket, key, true)
}

// Bound each pass so a large versioned prefix does not monopolize the writer.
// Re-list from the beginning on retry because the listed versions were removed.
func (s *S3BlobStore) purge(ctx context.Context, bucket, prefix string, exact bool) error {
	api, ok := s.api.(versionClient)
	if !ok {
		return fmt.Errorf("versioned deletion unavailable")
	}
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}
	for {
		out, err := api.ListObjectVersions(ctx, in)
		if err != nil {
			return err
		}
		var ids []types.ObjectIdentifier
		add := func(key, version *string) {
			if key != nil && strings.HasPrefix(*key, prefix) && (!exact || *key == prefix) {
				ids = append(ids, types.ObjectIdentifier{Key: key, VersionId: version})
			}
		}
		for _, v := range out.Versions {
			add(v.Key, v.VersionId)
		}
		for _, v := range out.DeleteMarkers {
			add(v.Key, v.VersionId)
		}
		if len(ids) > 0 {
			deleted, err := api.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: in.Bucket, Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
			if err != nil {
				return err
			}
			if len(deleted.Errors) > 0 {
				return fmt.Errorf("S3 declined a version deletion")
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		if out.NextKeyMarker == nil {
			return fmt.Errorf("S3 omitted pagination markers")
		}
		in.KeyMarker = out.NextKeyMarker
		in.VersionIdMarker = out.NextVersionIdMarker
	}
}
func (s *DirBlobStore) PurgeReplica(ctx context.Context, _ string, id string) error {
	if !appIDPattern.MatchString(id) {
		return ErrInvalid
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(id)
}
func (s *DirBlobStore) PurgeRelease(ctx context.Context, digest string) error {
	key, err := ReleaseKey(digest)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s *MemBlobStore) PurgeReplica(ctx context.Context, _ string, id string) error {
	if !appIDPattern.MatchString(id) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.blobs {
		if strings.HasPrefix(k, id+"/") {
			delete(s.blobs, k)
		}
	}
	return nil
}
func (s *MemBlobStore) PurgeRelease(ctx context.Context, digest string) error {
	key, err := ReleaseKey(digest)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, key)
	return nil
}
