package dropserver_test

import (
	"errors"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestCommitSubmission(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, dev := h.User("dev@example.test", "developer")
	_, user := h.User("user@example.test", "user")
	const url = "https://github.com/example/product/commit/0123456789abcdef0123456789abcdef01234567"
	input := map[string]string{"commit_url": url}
	wantError(t, h.Op(user, "developer.submit", input), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(dev, "catalog.submissions", nil), 403, dropapi.CodeForbidden)
	for _, invalid := range []string{"https://github.com/example/product/tree/main", url + "?secret=x", "https://github.com.example.org/example/product/commit/" + url[len(url)-40:]} {
		wantError(t, h.Op(dev, "developer.submit", map[string]string{"commit_url": invalid}), 400, dropapi.CodeInvalidInput)
	}
	h.MustOp(dev, "developer.submit", input)
	h.MustOp(dev, "developer.submit", input)
	if rows := h.MustOp(admin, "catalog.submissions", nil)["submissions"].([]any); len(rows) != 1 || rows[0].(map[string]any)["commit_url"] != url {
		t.Fatal(rows)
	}
	wantError(t, h.Op(dev, "catalog.upload", map[string]string{"commit_url": url, "product": "sample", "version": "1"}), 403, dropapi.CodeForbidden)
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
	if _, err := h.Releases.Head(t.Context(), "releases/"+rel.SHA256); !errors.Is(err, awsx.ErrNotFound) {
		t.Fatal("private upload reached official storage", err)
	}
}
