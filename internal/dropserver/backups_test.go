package dropserver_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// Exercise owner isolation and node transfer via the normal hosted route allowlist.
func checkBackupRoundTrip(t *testing.T, h *droptest.Hosted, alice, bob, node string, app map[string]any) {
	args := map[string]any{"app": app["id"]}
	wantError(t, h.Op(bob, "backup.create", args), 404, dropapi.CodeNotFound)
	wantError(t, h.Op(alice, "backup.create", args), 409, "backups_disabled")
	wantError(t, h.Op(alice, "backup.configure", map[string]any{"app": app["id"], "enabled": true}), 409, "backup_credits_required")
	if r := h.Op(alice, "credits.buy", nil, "fund-backups"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	h.MustOp(alice, "backup.configure", map[string]any{"app": app["id"], "enabled": true})
	capture := h.MustOp(alice, "backup.create", args)
	wantError(t, h.Op(alice, "backup.create", args), 409, "backup_busy")
	id := capture["backup"].(string)
	jobs := dropapi.AgentBackupJobsRoute
	upload := jobs + "/" + capture["job"].(string) + "/archive"
	const archive = "archive fixture with content"
	foreign := h.MustOp(h.Token(h.Owner()), "node.create", map[string]any{"name": "other-backup-node", "host_id": hostB})
	_, foreignToken := h.NodeLogin("other-backup-node", foreign["password"].(string))
	if w := h.Request("PUT", upload, foreignToken, archive); w.Code != 404 {
		t.Fatalf("foreign node write %d %s", w.Code, w.Body.String())
	}
	for _, token := range []string{"", bob} {
		w := h.Request("PUT", upload, token, archive)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("capture denied", w.Code, w.Body.String())
		}
	}
	// Queue time does not consume days from the downloadable retention window.
	if _, err := h.App.DB().NewQuery("UPDATE drop_backups SET created=unixepoch()-86400 WHERE id={:id}").Bind(dbx.Params{"id": id}).Execute(); err != nil {
		t.Fatal(err)
	}
	if w := h.Request("PUT", upload, node, archive); w.Code != 204 {
		t.Fatal("capture", w.Code, w.Body.String())
	}
	var archiveAge int64
	if err := h.App.DB().NewQuery("SELECT unixepoch()-created FROM drop_backups WHERE id={:id}").Bind(dbx.Params{"id": id}).Row(&archiveAge); err != nil || archiveAge >= 86400 {
		t.Fatal("queue time counted against retention", archiveAge, err)
	}
	download := dropapi.BackupDownloadPrefix + id + "/download"
	if w := h.Request("GET", download, bob, ""); w.Code != 404 {
		t.Fatal("foreign download", w.Code)
	}
	if w := h.Request("GET", download, alice, ""); w.Code != 200 || w.Body.String() != archive {
		t.Fatal("download", w.Code, w.Body.String())
	}
	wantError(t, h.Op(bob, "backup.restore", map[string]any{"backup": id, "new_name": "clone"}), 404, dropapi.CodeNotFound)
	wantError(t, h.Op(alice, "backup.restore", map[string]any{"backup": id, "same_app": "wrong"}), 400, dropapi.CodeInvalidInput)
	for _, dest := range []map[string]any{{"same_app": app["slug"]}, {"new_name": "Restored Crew"}} {
		dest["backup"] = id
		out := h.MustOp(alice, "backup.restore", dest)
		if _, clone := dest["new_name"]; clone == (out["app"] == app["id"]) {
			t.Fatal("restore target", out)
		}
		path := jobs + "/" + out["job"].(string)
		if w := h.Request("GET", path+"/archive", node, ""); w.Code != 200 || w.Body.String() != archive {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := h.Request("POST", path+"/result", node, `{"success":true}`); w.Code != http.StatusNoContent {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := h.Request("GET", path+"/archive", node, ""); w.Code != 404 {
			t.Fatal("consumed job", w.Code)
		}
	}
	if got := h.MustOp(bob, "backup.list", nil)["backups"].([]any); len(got) != 0 {
		t.Fatal("foreign list", got)
	}
	// A due schedule runs once, and expiration removes both metadata and the owner's download.
	now := time.Now()
	if _, err := h.App.DB().NewQuery("UPDATE drop_backup_policies SET next_at={:t} WHERE app={:a}").Bind(dbx.Params{"t": now.Unix(), "a": app["id"]}).Execute(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := dropserver.MaintainBackupsForTest(context.Background(), h.App, now); err != nil {
			t.Fatal(err)
		}
	}
	list := h.MustOp(alice, "backup.list", nil)
	if len(list["backups"].([]any)) != 2 {
		t.Fatal("duplicate or missing scheduled capture", list)
	}
	h.MustOp(alice, "backup.configure", map[string]any{"app": app["id"], "enabled": false})
	if err := dropserver.MaintainBackupsForTest(context.Background(), h.App, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w := h.Request("GET", download, alice, ""); w.Code != 404 {
		t.Fatal("expired backup", w.Code)
	}
	if list := h.MustOp(alice, "backup.list", nil); len(list["backups"].([]any)) != 0 {
		t.Fatal("expired metadata", list)
	}

}
