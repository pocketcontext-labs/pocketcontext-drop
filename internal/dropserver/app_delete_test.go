package dropserver_test

import (
	"bytes"
	"errors"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"

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
	if _, err := h.App.DB().NewQuery("INSERT INTO drop_credit_holds VALUES ({:app},'trial','on-request')").Bind(dbx.Params{"app": app}).Execute(); err != nil {
		t.Fatal(err)
	}
	h.MustOp(owner, "app.delete", args)
	var held int
	if err := h.App.DB().NewQuery("SELECT COUNT(*) FROM drop_credit_holds WHERE app={:app}").Bind(dbx.Params{"app": app}).Row(&held); err != nil || held != 0 {
		t.Fatal("top-up could revive deletion", err)
	}
	if r := h.Op(owner, "app.restore", map[string]any{"app": app}); r.Status != 404 {
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

func TestDeletionReceiptAfterNodeConfirmationAndRetry(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, owner := h.User("receipt-owner@example.test", "user")
	_, other := h.User("other@example.test", "user")
	_, _, node := h.Node("node-one", hostA)
	app := deployedApp(t, h, owner, "receipt-app", 1)
	h.MustOp(admin, "placement.set", map[string]any{"app": app, "node": "node-one"})
	ref := map[string]any{"app": app}
	preview := h.MustOp(owner, "app.get", map[string]any{"app": app, "deletion_inventory": true})["deletion_inventory"].(map[string]any)
	if num(preview["release_count"]) != 1 || preview["replica_mode"] != "none" {
		t.Fatal(preview)
	}
	h.MustOp(owner, "app.delete", map[string]any{"app": app, "confirm_slug": "receipt-app"})
	if detail := h.Op(owner, "app.get", ref); detail.Status != 404 {
		t.Fatal(detail)
	}
	for _, filter := range []map[string]any{nil, {"archived": true}, {"include_archived": true}} {
		if apps := h.MustOp(owner, "app.list", filter)["apps"].([]any); len(apps) != 0 {
			t.Fatalf("pending deletion visible: %v", apps)
		}
	}
	receipt := func(completed bool, nodes int) {
		t.Helper()
		for _, token := range []string{owner, admin} {
			items := h.MustOp(token, "app.deletions", ref)["deletions"].([]any)
			if len(items) != 1 {
				t.Fatal(items)
			}
			r := items[0].(map[string]any)
			if r["app"] != app || r["slug"] != "receipt-app" || r["completed"] != completed || num(r["waiting_nodes"]) != nodes {
				t.Fatal(r)
			}
		}
	}
	receipt(false, 1)
	for _, query := range []map[string]any{nil, ref} {
		if items := h.MustOp(other, "app.deletions", query)["deletions"].([]any); len(items) != 0 {
			t.Fatal(items)
		}
	}
	if err := dropserver.FinishAppDeletionForTest(t.Context(), h.App, app); err != nil {
		t.Fatal(err)
	}
	receipt(false, 1)
	d, _ := desired(t, h, node)
	ack := reportOf(hostA)
	ack["desiredSeq"] = d.DesiredSeq
	ack["deleted"] = d.Deleted
	if status, result := report(t, h, node, ack); status != 200 {
		t.Fatal(result)
	}
	receipt(false, 0)
	dropserver.SetServices(h.App, dropserver.Services{})
	if err := dropserver.FinishAppDeletionForTest(t.Context(), h.App, app); err == nil {
		t.Fatal("expected unavailable release store")
	}
	receipt(false, 0)
	dropserver.SetServices(h.App, dropserver.Services{Releases: h.Releases, Secrets: h.Secrets})
	if err := dropserver.FinishAppDeletionForTest(t.Context(), h.App, app); err != nil {
		t.Fatal(err)
	}
	newApp(t, h, other, "receipt-app")
	receipt(true, 0)
}
