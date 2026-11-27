package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/privateblob"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// maxReleaseBytes is the largest release the server accepts (90 MiB).
const maxReleaseBytes = appspec.MaxReleaseBytes

// ArtifactInfo is the state file's record of a staged artifact: its size and
// modification time when its digest was last verified, so an unchanged file
// is not hashed again.
type ArtifactInfo struct {
	Size          int64 `json:"size"`
	MtimeUnixNano int64 `json:"mtimeUnixNano"`
}

// Stager puts releases into nibrunner's artifact store under their sha256
// (the layer's objectKey). It never deletes an artifact.
type Stager struct {
	Dir   string
	Blobs awsx.BlobReader
	// Cache is shared with the state file.
	Cache map[string]ArtifactInfo
}

func checkRelease(rel render.Release) error {
	if !awsx.ValidSHA256(rel.SHA256) {
		return errors.New("release sha256 is not 64 lower-case hex characters")
	}
	if err := rel.ValidateObject(); err != nil {
		return err
	}
	if rel.SizeBytes <= 0 || rel.SizeBytes > maxReleaseBytes {
		return fmt.Errorf("release size %d is out of range", rel.SizeBytes)
	}
	return nil
}

func storedReleaseSize(rel render.Release) int64 {
	if rel.PrivateKey != "" {
		return privateblob.StoredSize(rel.SizeBytes)
	}
	return rel.SizeBytes
}

// Stage makes <Dir>/<sha256> hold exactly the release's bytes. It downloads
// to a private temporary file while hashing, refuses a size or digest
// mismatch, fsyncs, renames and fsyncs the directory.
func (s *Stager) Stage(ctx context.Context, rel render.Release) error {
	if err := checkRelease(rel); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(s.Dir, rel.SHA256)
	if ok, err := s.present(dest, rel); err != nil || ok {
		return err
	}
	body, info, err := s.Blobs.Get(ctx, rel.S3Key)
	if err != nil {
		if errors.Is(err, awsx.ErrNotFound) {
			return fmt.Errorf("release %s is not in the store", rel.S3Key)
		}
		return fmt.Errorf("download %s: %w", rel.S3Key, err)
	}
	defer body.Close()
	if info.Size != 0 && info.Size != storedReleaseSize(rel) {
		return fmt.Errorf("release %s has %d bytes in the store, expected %d", rel.S3Key, info.Size, storedReleaseSize(rel))
	}
	tmp, err := os.CreateTemp(s.Dir, ".dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	hash := sha256.New()
	out := io.MultiWriter(tmp, hash)
	if rel.PrivateKey != "" {
		err = privateblob.Open(out, body, rel.PrivateKey, rel.SizeBytes)
	} else {
		var n int64
		n, err = io.Copy(out, io.LimitReader(body, rel.SizeBytes+1))
		if err == nil && n != rel.SizeBytes {
			err = errors.New("release has incorrect length")
		}
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != rel.SHA256 {
		err = fmt.Errorf("release %s does not match its size or sha256; refused", rel.S3Key)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	if err := syncDir(s.Dir); err != nil {
		return err
	}
	return s.remember(dest, rel.SHA256)
}

// present reports whether dest already holds the release. A file whose size
// and mtime match the cache is trusted; any other file of the right size is
// hashed again. A wrong file is left for the download's rename to replace.
func (s *Stager) present(dest string, rel render.Release) (bool, error) {
	info, err := os.Lstat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != rel.SizeBytes {
		return false, nil
	}
	if c, ok := s.Cache[rel.SHA256]; ok && c.Size == info.Size() && c.MtimeUnixNano == info.ModTime().UnixNano() {
		return true, nil
	}
	sum, err := fileSHA256(dest)
	if err != nil {
		return false, err
	}
	if sum != rel.SHA256 {
		return false, nil
	}
	s.Cache[rel.SHA256] = ArtifactInfo{Size: info.Size(), MtimeUnixNano: info.ModTime().UnixNano()}
	return true, nil
}

func (s *Stager) remember(dest, sha string) error {
	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	s.Cache[sha] = ArtifactInfo{Size: info.Size(), MtimeUnixNano: info.ModTime().UnixNano()}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
