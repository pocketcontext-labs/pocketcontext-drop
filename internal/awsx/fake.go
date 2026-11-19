package awsx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
)

// MemBlobStore is an in-memory BlobStore for unit tests.
type MemBlobStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

// NewMemBlobStore returns an empty in-memory blob store.
func NewMemBlobStore() *MemBlobStore { return &MemBlobStore{blobs: map[string][]byte{}} }

// readVerified reads body from its start and checks size and digest.
func readVerified(body io.ReadSeeker, size int64, sha256hex string) ([]byte, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("awsx: rewind body: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return nil, fmt.Errorf("awsx: read body: %w", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != size || hex.EncodeToString(sum[:]) != sha256hex {
		return nil, ErrDigestMismatch
	}
	return data, nil
}

func (s *MemBlobStore) PutIfAbsent(ctx context.Context, key string, body io.ReadSeeker, size int64, sha256hex string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := checkPutArgs(key, body, size, sha256hex); err != nil {
		return false, err
	}
	if err := checkPrefixConflict(s.keys(), key); err != nil {
		return false, err
	}
	s.mu.Lock()
	_, exists := s.blobs[key]
	s.mu.Unlock()
	if exists {
		return false, nil
	}
	data, err := readVerified(body, size, sha256hex)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.blobs[key]; exists {
		return false, nil
	}
	s.blobs[key] = data
	return true, nil
}

func (s *MemBlobStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.blobs))
	for k := range s.blobs {
		out = append(out, k)
	}
	return out
}

// checkPrefixConflict refuses a key that is a directory of another key or
// vice versa, so the memory fake fails the same way as the directory fake
// (S3 itself allows both).
func checkPrefixConflict(existing []string, key string) error {
	for _, k := range existing {
		if len(k) > len(key) && k[:len(key)+1] == key+"/" || len(key) > len(k) && key[:len(k)+1] == k+"/" {
			return fmt.Errorf("%w: blob key %q conflicts with %q", ErrInvalid, key, k)
		}
	}
	return nil
}

func (s *MemBlobStore) Head(ctx context.Context, key string) (BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, err
	}
	if err := ValidateKey(key); err != nil {
		return BlobInfo{}, err
	}
	s.mu.Lock()
	data, ok := s.blobs[key]
	s.mu.Unlock()
	if !ok {
		return BlobInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	sum := sha256.Sum256(data)
	return BlobInfo{Key: key, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (s *MemBlobStore) Get(ctx context.Context, key string) (io.ReadCloser, BlobInfo, error) {
	info, err := s.Head(ctx, key)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	s.mu.Lock()
	data := s.blobs[key]
	s.mu.Unlock()
	return io.NopCloser(bytes.NewReader(data)), info, nil
}

// MemSecretStore is an in-memory SecretStore for unit tests.
type MemSecretStore struct {
	mu     sync.Mutex
	params map[string][]string // versions[i] is version i+1
}

// NewMemSecretStore returns an empty in-memory secret store.
func NewMemSecretStore() *MemSecretStore { return &MemSecretStore{params: map[string][]string{}} }

func (s *MemSecretStore) Put(ctx context.Context, name, value string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateParameterName(name); err != nil {
		return 0, err
	}
	if err := checkSecretValue(value); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.params[name] = append(s.params[name], value)
	return int64(len(s.params[name])), nil
}

func (s *MemSecretStore) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateParameterName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.params, name)
	return nil
}

func (s *MemSecretStore) Get(ctx context.Context, name string, version int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateParameterName(name); err != nil {
		return "", err
	}
	if err := checkVersion(version); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.params[name]
	if version > int64(len(versions)) {
		return "", fmt.Errorf("%w: %s version %d", ErrNotFound, name, version)
	}
	return versions[version-1], nil
}
