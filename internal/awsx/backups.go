package awsx

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
)

type BackupStore interface {
	BlobStore
	PurgeBackup(context.Context, string) error
}

// Each backup has its own immutable object in a dedicated platform bucket.
// Only complete record IDs are accepted for versioned-object deletion.
var backupKeyPattern = regexp.MustCompile("^backups/[a-z0-9]{15}/[a-z0-9]{15}$")

func OpenBackup(ctx context.Context, store BackupStore, key string) (io.ReadCloser, BlobInfo, error) {
	if !backupKeyPattern.MatchString(key) {
		return nil, BlobInfo{}, ErrInvalid
	}
	return store.Get(ctx, key)
}

func (s *S3BlobStore) PurgeBackup(ctx context.Context, key string) error {
	if !backupKeyPattern.MatchString(key) {
		return ErrInvalid
	}
	return s.purge(ctx, s.bucket, key, true)
}

func (s *DirBlobStore) PurgeBackup(_ context.Context, key string) error {
	if !backupKeyPattern.MatchString(key) {
		return ErrInvalid
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

func (s *MemBlobStore) PurgeBackup(_ context.Context, key string) error {
	if !backupKeyPattern.MatchString(key) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, key)
	return nil
}
