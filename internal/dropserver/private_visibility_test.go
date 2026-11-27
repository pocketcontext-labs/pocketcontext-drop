package dropserver_test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestPrivateAppMetadataOnlyVisibleToOwner(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	operator := h.Token(h.Owner())
	account, developer := h.User("owner-preview@example.test", "user")
	h.MustOp(operator, "user.set", map[string]any{"user": account.Id, "role": "developer"})
	public := newApp(t, h, developer, "ordinary-app")
	const slug = "private-preview"
	id := appOf(t, h.MustOp(developer, "developer.create", map[string]any{"name": slug, "slug": slug}))["id"].(string)
	ref := map[string]any{"app": id}
	// A released file in the owner's private app.
	collection, err := h.App.FindCollectionByNameOrId("releases")
	if err != nil {
		t.Fatal(err)
	}
	release := core.NewRecord(collection)
	release.Set("app", id)
	release.Set("sha256", strings.Repeat("a", 64))
	release.Set("s3_key", "releases/"+strings.Repeat("b", 64))
	release.Set("size_bytes", 200)
	release.Set("created_by", account.Id)
	if err := h.App.Save(release); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"app.get", "release.list", "secret.list", "status", "usage.get", "event.list", "placement.clear"} {
		wantError(t, h.Op(operator, op, ref), 404, "not_found")
	}
	all := h.MustOp(operator, "app.list", map[string]any{"all": true})["apps"].([]any)
	if len(all) != 1 || all[0].(map[string]any)["id"] != public {
		t.Fatalf("admin app list: %v", all)
	}
	for _, pair := range []struct{ collection, id string }{{"apps", id}, {"releases", release.Id}} {
		path := "/api/collections/" + pair.collection + "/records/" + pair.id
		if w := routerGet(h, path, operator); w.Code != 404 {
			t.Fatalf("%s returned %d for admin", pair.collection, w.Code)
		}
		if w := routerGet(h, path, developer); w.Code != 200 {
			t.Fatalf("%s returned %d for owner", pair.collection, w.Code)
		}
	}

	// Known immutable IDs and slugs must not confirm or expose deleted private
	// apps through shared audit, admin billing and deletion receipt interfaces.
	if _, err := h.App.DB().NewQuery("INSERT INTO drop_credit_ledger(owner,app,kind,delta,source) VALUES ({:owner},{:app},'adjustment',1,'private-test')").Bind(dbx.Params{"owner": account.Id, "app": id}).Execute(); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{operator, developer} {
		input := map[string]any{"account": account.Id, "query": slug, "kind": "adjustment", "limit": 1}
		result := h.MustOp(token, "credits.get", input)
		count := len(result["entries"].([]any))
		if (token == operator && count != 0) || (token == developer && count != 1) {
			t.Fatalf("search visibility returned %d entries", count)
		}
	}
	for i := range 2 {
		if r := h.Op(operator, "credits.adjust", map[string]any{"account": account.Id, "microcredits": i + 1}, "adjust-"+strconv.Itoa(i)); r.Status != 200 {
			t.Fatal(r.Raw)
		}
	}
	first := h.MustOp(developer, "credits.get", map[string]any{"kind": "adjustment", "limit": 1})
	firstID := first["entries"].([]any)[0].(map[string]any)["id"].(float64)
	if !first["has_more"].(bool) {
		t.Fatal("missing more entries")
	}
	second := h.MustOp(developer, "credits.get", map[string]any{"kind": "adjustment", "limit": 1, "before": firstID})
	if second["entries"].([]any)[0].(map[string]any)["id"].(float64) >= firstID {
		t.Fatal("repeated cursor")
	}
	assertPrivate := func() {
		t.Helper()
		for _, q := range []struct {
			op    string
			input map[string]any
		}{{"event.list", map[string]any{"all": true}}, {"credits.get", map[string]any{"account": account.Id}}, {"app.deletions", ref}} {
			res := h.Op(operator, q.op, q.input)
			if res.Status != 200 || strings.Contains(res.Raw, id) || strings.Contains(res.Raw, slug) {
				t.Fatalf("private metadata in %s: %s", q.op, res.Raw)
			}
		}
		path := "/api/collections/events/records?filter=" + url.QueryEscape("action='developer.create'")
		if n := listCount(t, h, path, operator); n != 0 {
			t.Fatalf("admin sees %d private creation events", n)
		}
	}
	assertPrivate()
	if n := listCount(t, h, "/api/collections/events/records?filter="+url.QueryEscape("action='developer.create'"), developer); n != 1 {
		t.Fatalf("owner sees %d private creation events", n)
	}
	del := map[string]any{"app": id, "confirm_slug": slug}
	wantError(t, h.Op(operator, "app.delete", del), 404, "not_found")
	h.MustOp(developer, "app.delete", del)
	assertPrivate()
	if err := dropserver.FinishAppDeletionForTest(t.Context(), h.App, id); err != nil {
		t.Fatal(err)
	}
	assertPrivate()
	wantError(t, h.Op(operator, "app.delete", del), 404, "not_found")
	if r := h.MustOp(developer, "app.deletions", ref)["deletions"].([]any); len(r) != 1 || !r[0].(map[string]any)["completed"].(bool) {
		t.Fatalf("owner receipt: %v", r)
	}
}
