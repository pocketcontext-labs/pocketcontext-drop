package dropserver_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestConfirmedAppDeletion(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, owner := h.User("owner-a@example.test", "user")
	_, other := h.User("owner-b@example.test", "user")
	app := newApp(t, h, owner, "delete-me")
	shared := newApp(t, h, other, "keep-me")
	unique, common := droptest.ELF(4096, 1), droptest.ELF(4096, 2)
	for _, f := range []struct {
		app, token string
		body       []byte
	}{{app, owner, unique}, {app, owner, common}, {shared, other, common}} {
		if r := upload(t, h, uploadLink(t, h, f.token, f.app, nil), f.body); r.Status != 200 {
			t.Fatal(r.Raw)
		}
	}
	for _, id := range []string{app, shared} {
		if _, err := h.Releases.PutIfAbsent(t.Context(), "d-"+id+"/file", bytes.NewReader(common), int64(len(common)), sha(common)); err != nil {
			t.Fatal(err)
		}
	}
	h.MustOp(owner, "secret.set", map[string]any{"app": app, "name": "TEST", "value": "synthetic"})
	args := map[string]any{"app": app, "confirm_slug": "delete-me"}
	if r := h.Op(other, "app.delete", args); r.Status != 404 {
		t.Fatal(r.Raw)
	}
	if r := h.Op(owner, "app.delete", map[string]any{"app": app, "confirm_slug": "keep-me"}); r.Status != 400 {
		t.Fatal(r.Raw)
	}
	h.MustOp(owner, "app.delete", args)
	if r := h.Op(owner, "app.restore", map[string]any{"app": app}); r.Status != 409 {
		t.Fatal(r.Raw)
	}
	if err := dropserver.FinishAppDeletionForTest(t.Context(), h.App, app); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"releases/" + sha(unique), "d-" + app + "/file"} {
		if _, err := h.Releases.Head(t.Context(), key); !errors.Is(err, awsx.ErrNotFound) {
			t.Fatalf("key %s: %v", key, err)
		}
	}
	for _, key := range []string{"releases/" + sha(common), "d-" + shared + "/file"} {
		if _, err := h.Releases.Head(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	parameter, err := awsx.SecretParameter(awsx.DefaultSSMPrefix, "d-"+app, "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secrets.Get(t.Context(), parameter, 1); !errors.Is(err, awsx.ErrNotFound) {
		t.Fatal("secret remains", err)
	}
	newApp(t, h, other, "delete-me")
	h.MustOp(owner, "app.delete", args)
	if len(events(t, h, "app.delete")) < 1 {
		t.Fatal("audit missing")
	}
}
