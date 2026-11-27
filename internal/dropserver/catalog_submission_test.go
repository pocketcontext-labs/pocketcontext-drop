package dropserver_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"io"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// Called after the private runner staged the developer's ELF. Exercise all
// promotion requests through the authenticated operation route.
func testCatalogPromotion(t *testing.T, h *droptest.Hosted, store *promotionStore, dev, admin, user, app, release string, raw []byte) {
	t.Helper()
	digest := sha(raw)
	submit := map[string]any{"app": app, "release": release}
	wantError(t, h.Op(user, "developer.submit", submit), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(admin, "developer.submit", submit), 403, dropapi.CodeForbidden)
	_, foreign := h.User("second-dev@example.test", "developer")
	wantError(t, h.Op(foreign, "developer.submit", submit), 404, dropapi.CodeNotFound)
	if s := h.MustOp(dev, "developer.submit", submit); s["sha256"] != digest || s["product"] != "crew" || s["version"] != "0.45.4-drop.4" {
		t.Fatal("incorrect candidate", s)
	}
	h.MustOp(dev, "developer.submit", submit)
	wantError(t, h.Op(user, "catalog.submissions", nil), 403, dropapi.CodeForbidden)
	if rows := h.MustOp(admin, "catalog.submissions", nil)["submissions"].([]any); len(rows) != 1 {
		t.Fatal("submissions", rows)
	}
	ref := map[string]any{"sha256": digest}
	wantError(t, h.Op(admin, "catalog.publish", ref), 404, dropapi.CodeNotFound)
	accept := map[string]any{"app": app, "release": release, "sha256": sha([]byte("wrong digest"))}
	wantError(t, h.Op(admin, "catalog.accept", accept), 404, dropapi.CodeNotFound)
	accept["sha256"] = digest
	wantError(t, h.Op(dev, "catalog.accept", accept), 403, dropapi.CodeForbidden)
	// Demoting the reviewer during either external store call must succeed
	// promptly and prevent that call from making a catalog entry.
	reviewer, token := h.User("reviewer@example.test", "admin")
	for _, phase := range []string{"get", "put"} {
		entered, resume := make(chan struct{}), make(chan struct{})
		store.pause = func(kind string) error {
			if phase != kind {
				return nil
			}
			close(entered)
			select {
			case <-resume:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("a release transfer held the database writer")
			}
		}
		done := make(chan droptest.OpResult, 1)
		go func() { done <- h.Op(token, "catalog.accept", accept, "review-accept") }()
		select {
		case <-entered:
		case res := <-done:
			t.Fatalf("accept exited before %s: %s", phase, res.Raw)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", phase)
		}
		change := h.Op(admin, "user.set", map[string]any{"user": reviewer.Id, "role": "user"}, "revoke-"+phase)
		close(resume)
		result := <-done
		store.pause = nil
		if change.Status != 200 {
			t.Fatal(change.Raw)
		}
		wantError(t, result, 403, dropapi.CodeForbidden)
		if phase == "get" {
			if _, err := h.Releases.Head(t.Context(), "releases/"+digest); !errors.Is(err, awsx.ErrNotFound) {
				t.Fatal("revoked reviewer disclosed private bytes", err)
			}
		}
		wantError(t, h.Op(admin, "catalog.publish", ref), 404, dropapi.CodeNotFound)
		h.MustOp(admin, "user.set", map[string]any{"user": reviewer.Id, "role": "admin"})
	}
	if r := h.Op(admin, "catalog.accept", accept, "final-review"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	if r := h.Op(admin, "catalog.accept", accept, "final-review"); r.Status != 200 || !r.Replayed {
		t.Fatal("acceptance receipt was not replayed", r)
	}
	h.MustOp(admin, "catalog.accept", accept)
	if rows := h.MustOp(admin, "catalog.submissions", nil)["submissions"].([]any); len(rows) != 0 {
		t.Fatal("accepted submission is still pending", rows)
	}
	wantError(t, h.Op(admin, "catalog.publish", ref), 409, "rehearsal_required")
	if rows := h.MustOp(user, "catalog.list", nil)["releases"].([]any); len(rows) != 0 {
		t.Fatal("unpublished candidate visible", rows)
	}
	h.MustOp(admin, "catalog.rehearse", map[string]any{"sha256": digest, "baseline_sha256": "", "evidence_url": "https://evidence.example.test/rehearsal", "evidence_sha256": sha([]byte("fixture report")),
		"before_records_sha256": sha([]byte("record")), "after_records_sha256": sha([]byte("record")), "before_attachments_sha256": sha([]byte("file")), "after_attachments_sha256": sha([]byte("file")), "records": 1, "attachments": 1})
	h.MustOp(admin, "catalog.publish", ref)
	body, _, err := h.Releases.Get(t.Context(), "releases/"+digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatal("catalog binary differs from the tested private release", err)
	}
	// Use an activated catalog fixture, with the actual published candidate.
	if _, err = h.App.DB().NewQuery("UPDATE drop_official_config SET enabled=1").Execute(); err != nil {
		t.Fatal(err)
	}
	workspace := appOf(t, h.MustOp(user, "workspace.create", map[string]any{"product": "crew", "sha256": digest, "multiplier": 4, "name": "Reviewed release"}))
	if workspace["beta_sha256"] != digest {
		t.Fatal("wrong workspace release", workspace)
	}
}

func testCatalogSourceDeletion(t *testing.T, h *droptest.Hosted, owner, node, app string, rel dropapi.Release) {
	t.Helper()
	h.MustOp(owner, "app.delete", map[string]any{"app": app, "confirm_slug": "dev-private-build"})
	d, _ := desired(t, h, node)
	ack := reportOf(hostB)
	ack["desiredSeq"] = d.DesiredSeq
	ack["deleted"] = d.Deleted
	if status, body := report(t, h, node, ack); status != 200 {
		t.Fatal(body)
	}
	if err := dropserver.FinishAppDeletionForTest(t.Context(), h.App, app); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Releases.Head(t.Context(), rel.S3Key); !errors.Is(err, awsx.ErrNotFound) {
		t.Fatal("source ciphertext survived deletion", err)
	}
	if _, err := h.Releases.Head(t.Context(), "releases/"+rel.SHA256); err != nil {
		t.Fatal("catalog release lost after developer app deletion", err)
	}
	var n int
	if err := h.App.DB().NewQuery("SELECT COUNT(*) FROM drop_catalog_submissions").Row(&n); err != nil || n != 0 {
		t.Fatal("source submission survived deletion", n, err)
	}
}

type promotionStore struct {
	*awsx.MemBlobStore
	pause func(string) error
}

func (s *promotionStore) Get(ctx context.Context, key string) (io.ReadCloser, awsx.BlobInfo, error) {
	if s.pause != nil {
		if err := s.pause("get"); err != nil {
			return nil, awsx.BlobInfo{}, err
		}
	}
	return s.MemBlobStore.Get(ctx, key)
}
func (s *promotionStore) PutIfAbsent(ctx context.Context, key string, r io.ReadSeeker, size int64, digest string) (bool, error) {
	if s.pause != nil {
		if err := s.pause("put"); err != nil {
			return false, err
		}
	}
	return s.MemBlobStore.PutIfAbsent(ctx, key, r, size, digest)
}
