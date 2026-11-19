package awsx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBase = "/drop/apps/abcdefghijklmno"

func TestMemoryConformance(t *testing.T) {
	s := NewMemoryStores()
	t.Run("blobs", func(t *testing.T) { runBlobConformance(t, s.Blobs, blobSuiteOptions{concurrency: true}) })
	t.Run("secrets", func(t *testing.T) {
		runSecretConformance(t, s.Secrets, testBase, secretSuiteOptions{concurrency: true})
	})
}

func TestDirConformance(t *testing.T) {
	s, err := OpenFakeDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Fake || !strings.Contains(s.Description, "testing only") {
		t.Fatalf("fake stores not marked: %+v", s)
	}
	t.Run("blobs", func(t *testing.T) { runBlobConformance(t, s.Blobs, blobSuiteOptions{concurrency: true}) })
	t.Run("secrets", func(t *testing.T) {
		runSecretConformance(t, s.Secrets, testBase, secretSuiteOptions{concurrency: true})
	})
}

// Two OpenFakeDir calls on one directory stand for the server and agent
// processes of the end-to-end test.
func TestDirSharedBetweenProcesses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	server, err := OpenFakeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := OpenFakeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, sha := randomBlob(t, 512)
	key, _ := ReleaseKey(sha)
	if _, err := server.Blobs.PutIfAbsent(ctx, key, bytes.NewReader(data), 512, sha); err != nil {
		t.Fatal(err)
	}
	rc, info, err := agent.Blobs.Get(ctx, key)
	if err != nil || info.SHA256 != sha {
		t.Fatalf("agent Get: %+v %v", info, err)
	}
	if !bytes.Equal(readAllClose(t, rc), data) {
		t.Fatal("agent read different bytes")
	}
	param, _ := SecretParameter(DefaultSSMPrefix, "d-abcdefghijklmno", "API_KEY")
	v, err := server.Secrets.Put(ctx, param, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := agent.Secrets.Get(ctx, param, v); err != nil || got != "s3cr3t" {
		t.Fatalf("agent secret: %q %v", got, err)
	}

	// Layout and permissions.
	blobPath := filepath.Join(dir, "blobs", "releases", sha)
	assertMode(t, blobPath, 0o600)
	assertMode(t, filepath.Join(dir, "blobs"), 0o700|os.ModeDir)
	assertMode(t, filepath.Join(dir, "blobs", "releases"), 0o700|os.ModeDir)
	paramPath := filepath.Join(dir, "secrets", paramDir(param))
	assertMode(t, paramPath, 0o700|os.ModeDir)
	assertMode(t, filepath.Join(paramPath, "1"), 0o600)
	if name, _ := os.ReadFile(filepath.Join(paramPath, "name")); string(name) != param+"\n" {
		t.Fatalf("name file = %q", name)
	}
	entries, _ := os.ReadDir(paramPath)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Fatalf("%s mode = %v, want %v", path, info.Mode(), want)
	}
}

// A symlink planted inside the fake directory never leads outside it.
func TestDirBlobSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	outside := t.TempDir()
	s, err := OpenFakeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "blobs", "releases")); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(outside, "victim")
	if err := os.WriteFile(secretFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, sha := randomBlob(t, 32)
	key, _ := ReleaseKey(sha)
	if created, err := s.Blobs.PutIfAbsent(ctx, key, bytes.NewReader(data), 32, sha); err == nil || created {
		t.Fatalf("put through escaping symlink: created=%v err=%v", created, err)
	}
	if _, _, err := s.Blobs.Get(ctx, "releases/victim"); err == nil {
		t.Fatal("Get followed a symlink out of the fake directory")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatalf("files written outside the fake directory: %v", entries)
	}

	// A symlink as the blob itself is not served either.
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretFile, filepath.Join(dir, "blobs", "other", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Blobs.Head(ctx, "other/link"); err == nil {
		t.Fatal("Head followed a symlink out of the fake directory")
	}
}

func TestDirSecretNamesNeverBecomePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenFakeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Secrets.Put(ctx, "/drop/apps/x/../../../../etc", "v"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("traversal name accepted: %v", err)
	}
	long := "/drop/" + strings.Repeat("n", 900)
	if _, err := s.Secrets.Put(ctx, long, "v"); err != nil {
		t.Fatalf("long valid name: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "secrets"))
	if len(entries) != 1 || len(entries[0].Name()) != 64 {
		t.Fatalf("secret directories = %v, want one sha256-named directory", entries)
	}
}

func TestMemKeyConflictMatchesDirFake(t *testing.T) {
	ctx := context.Background()
	dirStores, err := OpenFakeDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]BlobStore{"memory": NewMemBlobStore(), "dir": dirStores.Blobs} {
		data, sha := randomBlob(t, 4)
		if _, err := store.PutIfAbsent(ctx, "a/b", bytes.NewReader(data), 4, sha); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := store.PutIfAbsent(ctx, "a", bytes.NewReader(data), 4, sha); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: key over a directory: %v, want ErrInvalid", name, err)
		}
		if _, err := store.PutIfAbsent(ctx, "a/b/c", bytes.NewReader(data), 4, sha); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: key under a file: %v, want ErrInvalid", name, err)
		}
	}
}

func TestOpenFakeDirErrors(t *testing.T) {
	if _, err := OpenFakeDir(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty dir: %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFakeDir(file); err == nil {
		t.Fatal("OpenFakeDir accepted a file")
	}
}

func TestCheckFakeAllowed(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8100", "127.0.0.2:1", "[::1]:8100", "localhost:8100", "LOCALHOST:0"} {
		if err := CheckFakeAllowed(addr); err != nil {
			t.Errorf("CheckFakeAllowed(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{":8100", "0.0.0.0:8100", "[::]:8100", "10.0.0.5:8100", "drop-dev.bigconfig.space:443", "127.0.0.1", "", "localhost.evil.test:80"} {
		if err := CheckFakeAllowed(addr); err == nil {
			t.Errorf("CheckFakeAllowed(%q) = nil, want refusal", addr)
		}
	}
}

func TestCheckFakeAllowedURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8100", "http://localhost:8100/", "https://[::1]:8100"} {
		if err := CheckFakeAllowedURL(u); err != nil {
			t.Errorf("CheckFakeAllowedURL(%q) = %v, want nil", u, err)
		}
	}
	for _, u := range []string{"http://10.0.0.5:8100", "https://drop-dev.bigconfig.space", "ftp://127.0.0.1", "127.0.0.1:8100", "", "http://127.0.0.1.nip.io"} {
		if err := CheckFakeAllowedURL(u); err == nil {
			t.Errorf("CheckFakeAllowedURL(%q) = nil, want refusal", u)
		}
	}
}
