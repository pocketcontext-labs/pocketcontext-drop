package awsx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The directory fake keeps state in plain files so a server and an agent
// process can share it (`--fake-aws DIR` on both sides in end-to-end tests):
//
//	DIR/blobs/<key>                  one file per blob, e.g. blobs/releases/<sha256>
//	DIR/secrets/<sha256(name)>/name  the parameter name, for people reading the tree
//	DIR/secrets/<sha256(name)>/<n>   the value of version n (n = 1, 2, ...)
//
// Every access goes through os.Root, so even a symlink planted inside DIR
// cannot lead outside it, and keys are validated before use (no ".", ".."
// or empty segments). Files are 0600 and directories 0700. Writes are
// complete-or-absent: data goes to a ".tmp-*" file and is hard-linked into
// place, and link failing with "exists" is the if-absent check, so two
// processes cannot both create the same blob or secret version.

// CheckFakeAllowed refuses a fake AWS backend unless the server listens on
// a loopback address ("127.0.0.1:8100", "[::1]:8100", "localhost:8100").
// An empty host (":8100") listens on every interface and is refused.
func CheckFakeAllowed(listenAddr string) error {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("--fake-aws: listen address %q: %w", listenAddr, err)
	}
	if !IsLoopbackHost(host) {
		return fmt.Errorf("--fake-aws is for local testing and needs a loopback listen address, not %q", listenAddr)
	}
	return nil
}

// CheckFakeAllowedURL refuses a fake AWS backend for a client (the agent)
// unless the server it talks to is an http(s) URL on a loopback host.
func CheckFakeAllowedURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--fake-aws: server URL %q is not an http(s) URL", rawURL)
	}
	if !IsLoopbackHost(u.Hostname()) {
		return fmt.Errorf("--fake-aws is for local testing and needs a loopback server URL, not %q", rawURL)
	}
	return nil
}

// IsLoopbackHost accepts localhost and literal loopback addresses.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// OpenFakeDir returns directory-backed fakes rooted at dir, creating
// dir/blobs and dir/secrets (0700) when missing. dir must not be a symlink
// target outside the caller's control; it is used as given.
func OpenFakeDir(dir string) (*Stores, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: --fake-aws needs a directory", ErrInvalid)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	blobs, err := NewDirBlobStore(filepath.Join(abs, "blobs"))
	if err != nil {
		return nil, err
	}
	secrets, err := NewDirSecretStore(filepath.Join(abs, "secrets"))
	if err != nil {
		return nil, err
	}
	return &Stores{
		Blobs:       blobs,
		Secrets:     secrets,
		SSMPrefix:   DefaultSSMPrefix,
		Fake:        true,
		Description: "fake AWS in directory " + abs + " (testing only)",
	}, nil
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("awsx: create %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("awsx: %s is not a directory", dir)
	}
	return nil
}

func tempName() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return ".tmp-" + hex.EncodeToString(b[:])
}

// DirBlobStore is a BlobStore in a directory.
type DirBlobStore struct{ dir string }

// NewDirBlobStore creates dir (0700) if needed.
func NewDirBlobStore(dir string) (*DirBlobStore, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	return &DirBlobStore{dir: dir}, nil
}

func (s *DirBlobStore) PutIfAbsent(ctx context.Context, key string, body io.ReadSeeker, size int64, sha256hex string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := checkPutArgs(key, body, size, sha256hex); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if info, err := root.Lstat(key); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%w: blob key %q is not a file in the fake", ErrInvalid, key)
		}
		return false, nil
	}
	if parent := path.Dir(key); parent != "." {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return false, fmt.Errorf("%w: blob key %q: %v", ErrInvalid, key, err)
		}
	}
	tmp := path.Join(path.Dir(key), tempName())
	defer root.Remove(tmp)
	if err := writeVerified(root, tmp, body, size, sha256hex); err != nil {
		return false, err
	}
	if err := root.Link(tmp, key); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("awsx: fake put %s: %w", key, err)
	}
	return true, nil
}

func writeVerified(root *os.Root, name string, body io.ReadSeeker, size int64, sha256hex string) error {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("awsx: rewind body: %w", err)
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, size+1))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("awsx: fake write: %w", err)
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sha256hex {
		return ErrDigestMismatch
	}
	return nil
}

func (s *DirBlobStore) open(ctx context.Context, key string) (*os.File, BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, BlobInfo{}, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, BlobInfo{}, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	defer root.Close()
	f, err := root.Open(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) || isNotDir(err) {
			return nil, BlobInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, BlobInfo{}, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, BlobInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, BlobInfo{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, BlobInfo{}, err
	}
	return f, BlobInfo{Key: key, Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func isNotDir(err error) bool { return errors.Is(err, syscall.ENOTDIR) }

func (s *DirBlobStore) Head(ctx context.Context, key string) (BlobInfo, error) {
	f, info, err := s.open(ctx, key)
	if err != nil {
		return BlobInfo{}, err
	}
	f.Close()
	return info, nil
}

func (s *DirBlobStore) Get(ctx context.Context, key string) (io.ReadCloser, BlobInfo, error) {
	f, info, err := s.open(ctx, key)
	if err != nil {
		return nil, BlobInfo{}, err
	}
	return f, info, nil
}

// DirSecretStore is a SecretStore in a directory.
type DirSecretStore struct{ dir string }

// NewDirSecretStore creates dir (0700) if needed.
func NewDirSecretStore(dir string) (*DirSecretStore, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	return &DirSecretStore{dir: dir}, nil
}

func paramDir(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

func (s *DirSecretStore) Put(ctx context.Context, name, value string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateParameterName(name); err != nil {
		return 0, err
	}
	if err := checkSecretValue(value); err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	d := paramDir(name)
	// Retry when another process takes the next version or deletes the
	// parameter between our steps.
	for range 100 {
		if err := root.MkdirAll(d, 0o700); err != nil {
			return 0, fmt.Errorf("awsx: fake secret %s: %w", name, err)
		}
		_ = root.WriteFile(path.Join(d, "name"), []byte(name+"\n"), 0o600)
		next, err := latestVersion(root, d)
		if err != nil {
			continue
		}
		next++
		tmp := path.Join(d, tempName())
		if err := root.WriteFile(tmp, []byte(value), 0o600); err != nil {
			continue
		}
		err = root.Link(tmp, path.Join(d, strconv.FormatInt(next, 10)))
		_ = root.Remove(tmp)
		if err == nil {
			return next, nil
		}
	}
	return 0, fmt.Errorf("awsx: fake secret %s: too much contention", name)
}

func latestVersion(root *os.Root, d string) (int64, error) {
	f, err := root.Open(d)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, n := range names {
		if v, err := strconv.ParseInt(n, 10, 64); err == nil && v > latest {
			latest = v
		}
	}
	return latest, nil
}

func (s *DirSecretStore) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateParameterName(name); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	trash := ".trash" + tempName()[4:]
	if err := root.Rename(paramDir(name), trash); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("awsx: fake delete %s: %w", name, err)
	}
	return root.RemoveAll(trash)
}

func (s *DirSecretStore) Get(ctx context.Context, name string, version int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateParameterName(name); err != nil {
		return "", err
	}
	if err := checkVersion(version); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := root.ReadFile(path.Join(paramDir(name), strconv.FormatInt(version, 10)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s version %d", ErrNotFound, name, version)
		}
		return "", fmt.Errorf("awsx: fake get %s version %d: %w", name, version, err)
	}
	return string(data), nil
}
