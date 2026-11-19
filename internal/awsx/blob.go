// Package awsx holds Drop's two AWS dependencies behind small interfaces:
// release executables in S3 (BlobStore) and app secrets in SSM Parameter
// Store (SecretStore). The AWS implementations use the SDK's default
// credential chain; nothing here reads or writes access keys. The fakes
// (in memory, and directory-backed for `--fake-aws DIR`) follow the same
// contract, which conformance_test.go checks for every implementation.
package awsx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ErrNotFound reports a missing blob, secret or secret version.
var ErrNotFound = errors.New("awsx: not found")

// ErrDigestMismatch reports a PutIfAbsent body whose size or SHA-256 differs
// from the declared values. Nothing is stored.
var ErrDigestMismatch = errors.New("awsx: body does not match declared size or sha256")

// ErrInvalid reports a malformed key, parameter name, digest, version or value.
var ErrInvalid = errors.New("awsx: invalid argument")

// BlobInfo describes a stored blob. SHA256 is lower-case hex; it is empty when
// the store cannot report it (for example an S3 object written without a
// full-object SHA-256 checksum).
type BlobInfo struct {
	Key    string
	Size   int64
	SHA256 string
}

// BlobWriter is the server's view of the blob store: it uploads releases and
// may check that one exists.
type BlobWriter interface {
	// PutIfAbsent stores body under key unless key already exists. body is
	// read from its start (it is seeked to offset 0) and must be exactly
	// size bytes with the given lower-case hex SHA-256; otherwise
	// ErrDigestMismatch is returned and nothing is stored. created is false
	// when the key already existed (its content is not compared: keys are
	// content addresses such as ReleaseKey(sha)).
	PutIfAbsent(ctx context.Context, key string, body io.ReadSeeker, size int64, sha256hex string) (created bool, err error)
	// Head returns the blob's metadata or ErrNotFound.
	Head(ctx context.Context, key string) (BlobInfo, error)
}

// BlobReader is the agent's view of the blob store.
type BlobReader interface {
	// Get opens the blob for reading or returns ErrNotFound. The caller
	// closes the reader and verifies the digest it needs.
	Get(ctx context.Context, key string) (io.ReadCloser, BlobInfo, error)
	// Head returns the blob's metadata or ErrNotFound.
	Head(ctx context.Context, key string) (BlobInfo, error)
}

// BlobStore is the full blob contract implemented by S3 and the fakes.
type BlobStore interface {
	BlobWriter
	BlobReader
}

var (
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keySegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`)
)

// ReleasePrefix is the key prefix of release executables.
const ReleasePrefix = "releases/"

// ReleaseKey returns the content-addressed key of a release, releases/<sha256>.
func ReleaseKey(sha256hex string) (string, error) {
	if !sha256Pattern.MatchString(sha256hex) {
		return "", fmt.Errorf("%w: sha256 must be 64 lower-case hex characters", ErrInvalid)
	}
	return ReleasePrefix + sha256hex, nil
}

// ValidSHA256 reports whether s is 64 lower-case hex characters.
func ValidSHA256(s string) bool { return sha256Pattern.MatchString(s) }

// ValidateKey accepts relative slash-separated keys of at most 8 segments,
// each starting with a letter, digit, '_' or '-' (so never "." or ".."), at
// most 512 bytes in total. Every store enforces it, so a key that works on a
// fake works on S3 and cannot escape the directory fake's root.
func ValidateKey(key string) error {
	if key == "" || len(key) > 512 {
		return fmt.Errorf("%w: blob key length", ErrInvalid)
	}
	parts := strings.Split(key, "/")
	if len(parts) > 8 {
		return fmt.Errorf("%w: blob key has too many segments", ErrInvalid)
	}
	for _, p := range parts {
		if !keySegmentPattern.MatchString(p) {
			return fmt.Errorf("%w: blob key segment %q", ErrInvalid, p)
		}
	}
	return nil
}

func checkPutArgs(key string, body io.ReadSeeker, size int64, sha256hex string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if body == nil {
		return fmt.Errorf("%w: nil body", ErrInvalid)
	}
	if size < 0 {
		return fmt.Errorf("%w: negative size", ErrInvalid)
	}
	if !sha256Pattern.MatchString(sha256hex) {
		return fmt.Errorf("%w: sha256 must be 64 lower-case hex characters", ErrInvalid)
	}
	return nil
}
