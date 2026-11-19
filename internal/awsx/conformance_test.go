package awsx

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// The conformance suites define the store contract. They run against both
// fakes (fake_test.go) and, only with DROP_LIVE_AWS=1, against S3 and SSM
// (live_test.go), so they never delete blobs (the IAM policy has no
// s3:DeleteObject) and they clean up their secrets.

func randomBlob(t *testing.T, n int) ([]byte, string) {
	t.Helper()
	data := make([]byte, n)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

func readAllClose(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type blobSuiteOptions struct {
	concurrency bool // parallel puts of one key (fakes; costs requests live)
}

func runBlobConformance(t *testing.T, store BlobStore, opts blobSuiteOptions) {
	ctx := context.Background()

	t.Run("missing", func(t *testing.T) {
		_, sha := randomBlob(t, 16)
		key, _ := ReleaseKey(sha)
		if _, err := store.Head(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Head missing: %v, want ErrNotFound", err)
		}
		if _, _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get missing: %v, want ErrNotFound", err)
		}
	})

	t.Run("put head get and if-absent", func(t *testing.T) {
		data, sha := randomBlob(t, 70_000)
		key, _ := ReleaseKey(sha)
		body := bytes.NewReader(data)
		_, _ = body.Seek(0, io.SeekEnd) // the store reads from the start anyway
		created, err := store.PutIfAbsent(ctx, key, body, int64(len(data)), sha)
		if err != nil || !created {
			t.Fatalf("first put: created=%v err=%v", created, err)
		}
		info, err := store.Head(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if info.Key != key || info.Size != int64(len(data)) || info.SHA256 != sha {
			t.Fatalf("Head = %+v", info)
		}
		rc, ginfo, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got := readAllClose(t, rc); !bytes.Equal(got, data) {
			t.Fatal("Get returned different bytes")
		}
		if ginfo.Size != int64(len(data)) || ginfo.SHA256 != sha {
			t.Fatalf("Get info = %+v", ginfo)
		}

		created, err = store.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), sha)
		if err != nil || created {
			t.Fatalf("second put: created=%v err=%v, want false nil", created, err)
		}
		// A different (correctly declared) body under an existing key is
		// not stored: the first write wins.
		other, otherSHA := randomBlob(t, 10)
		created, err = store.PutIfAbsent(ctx, key, bytes.NewReader(other), int64(len(other)), otherSHA)
		if err != nil || created {
			t.Fatalf("overwrite attempt: created=%v err=%v", created, err)
		}
		rc, _, err = store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got := readAllClose(t, rc); !bytes.Equal(got, data) {
			t.Fatal("existing blob was replaced")
		}
	})

	t.Run("empty blob", func(t *testing.T) {
		// Unique key for an empty body: a nested key under a random prefix.
		_, nonce := randomBlob(t, 8)
		sum := sha256.Sum256(nil)
		sha := hex.EncodeToString(sum[:])
		key := ReleasePrefix + "empty-" + nonce[:16]
		created, err := store.PutIfAbsent(ctx, key, bytes.NewReader(nil), 0, sha)
		if err != nil || !created {
			t.Fatalf("empty put: created=%v err=%v", created, err)
		}
		info, err := store.Head(ctx, key)
		if err != nil || info.Size != 0 || info.SHA256 != sha {
			t.Fatalf("Head empty = %+v, %v", info, err)
		}
	})

	t.Run("digest mismatch stores nothing", func(t *testing.T) {
		data, sha := randomBlob(t, 1000)
		key, _ := ReleaseKey(sha)
		_, wrongSHA := randomBlob(t, 4)
		cases := []struct {
			name string
			body []byte
			size int64
			sha  string
		}{
			{"wrong sha", data, int64(len(data)), wrongSHA},
			{"body longer than size", data, int64(len(data)) - 1, sha},
			{"body shorter than size", data, int64(len(data)) + 1, sha},
		}
		for _, c := range cases {
			created, err := store.PutIfAbsent(ctx, key, bytes.NewReader(c.body), c.size, c.sha)
			if !errors.Is(err, ErrDigestMismatch) || created {
				t.Fatalf("%s: created=%v err=%v, want ErrDigestMismatch", c.name, created, err)
			}
		}
		if _, err := store.Head(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Head after mismatches: %v, want ErrNotFound", err)
		}
	})

	t.Run("invalid arguments", func(t *testing.T) {
		data, sha := randomBlob(t, 8)
		for _, key := range invalidKeys {
			if _, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), 8, sha); !errors.Is(err, ErrInvalid) {
				t.Errorf("PutIfAbsent(%q): %v, want ErrInvalid", key, err)
			}
			if _, err := store.Head(ctx, key); !errors.Is(err, ErrInvalid) {
				t.Errorf("Head(%q): %v, want ErrInvalid", key, err)
			}
			if _, _, err := store.Get(ctx, key); !errors.Is(err, ErrInvalid) {
				t.Errorf("Get(%q): %v, want ErrInvalid", key, err)
			}
		}
		key, _ := ReleaseKey(sha)
		for _, bad := range []string{"", strings.ToUpper(sha), sha[:63], sha + "0"} {
			if _, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), 8, bad); !errors.Is(err, ErrInvalid) {
				t.Errorf("sha %q: %v, want ErrInvalid", bad, err)
			}
		}
		if _, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), -1, sha); !errors.Is(err, ErrInvalid) {
			t.Errorf("negative size: %v", err)
		}
		if _, err := store.PutIfAbsent(ctx, key, nil, 8, sha); !errors.Is(err, ErrInvalid) {
			t.Errorf("nil body: %v", err)
		}
	})

	if opts.concurrency {
		t.Run("concurrent puts create once", func(t *testing.T) {
			data, sha := randomBlob(t, 4096)
			key, _ := ReleaseKey(sha)
			const n = 16
			var wg sync.WaitGroup
			var mu sync.Mutex
			createdCount := 0
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					created, err := store.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), sha)
					if err != nil {
						t.Error(err)
						return
					}
					if created {
						mu.Lock()
						createdCount++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if createdCount != 1 {
				t.Fatalf("created %d times, want 1", createdCount)
			}
		})
	}
}

var invalidKeys = []string{
	"", "/releases/x", "releases/", "releases//x", "../x", "releases/../x",
	"releases/./x", ".hidden", "releases/.tmp-1", "releases/a b", "a/b/c/d/e/f/g/h/i",
	"releases\\x", strings.Repeat("a", 513), "releases/\x00",
}

type secretSuiteOptions struct {
	concurrency bool
}

// runSecretConformance uses parameters under base (for example
// /drop/apps/<id>), which must be unused, and deletes them afterwards.
func runSecretConformance(t *testing.T, store SecretStore, base string, opts secretSuiteOptions) {
	ctx := context.Background()
	a, b := base+"/ALPHA", base+"/BETA"
	t.Cleanup(func() {
		_ = store.Delete(context.Background(), a)
		_ = store.Delete(context.Background(), b)
	})

	t.Run("versions", func(t *testing.T) {
		if _, err := store.Get(ctx, a, 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get missing: %v, want ErrNotFound", err)
		}
		v1, err := store.Put(ctx, a, "first value")
		if err != nil || v1 != 1 {
			t.Fatalf("Put 1: %d %v", v1, err)
		}
		v2, err := store.Put(ctx, a, "second\nvalue ✓")
		if err != nil || v2 != 2 {
			t.Fatalf("Put 2: %d %v", v2, err)
		}
		for v, want := range map[int64]string{1: "first value", 2: "second\nvalue ✓"} {
			got, err := store.Get(ctx, a, v)
			if err != nil || got != want {
				t.Fatalf("Get %d = %q, %v", v, got, err)
			}
		}
		if _, err := store.Get(ctx, a, 3); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get future version: %v, want ErrNotFound", err)
		}
		// Another name is independent.
		vb, err := store.Put(ctx, b, "beta")
		if err != nil || vb != 1 {
			t.Fatalf("Put other name: %d %v", vb, err)
		}
	})

	t.Run("delete resets", func(t *testing.T) {
		if err := store.Delete(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, a, 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after delete: %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, a); err != nil {
			t.Fatalf("Delete missing: %v, want nil", err)
		}
		v, err := store.Put(ctx, a, "again")
		if err != nil || v != 1 {
			t.Fatalf("Put after delete: %d %v, want version 1", v, err)
		}
		if got, err := store.Get(ctx, b, 1); err != nil || got != "beta" {
			t.Fatalf("other name after delete: %q %v", got, err)
		}
	})

	t.Run("limits", func(t *testing.T) {
		if _, err := store.Put(ctx, a, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("empty value: %v", err)
		}
		if _, err := store.Put(ctx, a, strings.Repeat("x", MaxSecretBytes+1)); !errors.Is(err, ErrInvalid) {
			t.Errorf("oversized value: %v", err)
		}
		big := strings.Repeat("y", MaxSecretBytes)
		v, err := store.Put(ctx, a, big)
		if err != nil {
			t.Fatalf("max value: %v", err)
		}
		if got, err := store.Get(ctx, a, v); err != nil || got != big {
			t.Fatalf("max value round trip: %v", err)
		}
		for _, version := range []int64{0, -1} {
			if _, err := store.Get(ctx, a, version); !errors.Is(err, ErrInvalid) {
				t.Errorf("version %d: %v", version, err)
			}
		}
		for _, name := range invalidParameterNames {
			if _, err := store.Put(ctx, name, "v"); !errors.Is(err, ErrInvalid) {
				t.Errorf("Put(%q): %v, want ErrInvalid", name, err)
			}
			if err := store.Delete(ctx, name); !errors.Is(err, ErrInvalid) {
				t.Errorf("Delete(%q): %v, want ErrInvalid", name, err)
			}
			if _, err := store.Get(ctx, name, 1); !errors.Is(err, ErrInvalid) {
				t.Errorf("Get(%q): %v, want ErrInvalid", name, err)
			}
		}
	})

	if opts.concurrency {
		t.Run("concurrent puts get distinct versions", func(t *testing.T) {
			if err := store.Delete(ctx, b); err != nil {
				t.Fatal(err)
			}
			const n = 20
			var wg sync.WaitGroup
			var mu sync.Mutex
			seen := map[int64]string{}
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					val := "value-" + string(rune('a'+i))
					v, err := store.Put(ctx, b, val)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					defer mu.Unlock()
					if _, dup := seen[v]; dup {
						t.Errorf("version %d returned twice", v)
					}
					seen[v] = val
				}()
			}
			wg.Wait()
			for v := int64(1); v <= n; v++ {
				got, err := store.Get(ctx, b, v)
				if err != nil || got != seen[v] {
					t.Fatalf("version %d = %q %v, want %q", v, got, err, seen[v])
				}
			}
		})
	}
}

var invalidParameterNames = []string{
	"", "/", "drop/apps/x", "/drop//x", "/drop/../x", "/drop/./x", "/drop/x/", "/drop/a b",
	"/aws/x", "/ssm/x", "/drop/x\x00", "/" + strings.Repeat("a/", 15) + "b", "/" + strings.Repeat("a", 1011),
}
