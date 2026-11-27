package dropserver_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"ganigeorgiev.com/pocketbase/dbx"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// The isolated probe is replaced with its wire contract; every customer and
// operator interaction below uses the hosted HTTP operations and upload routes.
func TestOfficialWorkspaceLifecycle(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "probe.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := droptest.NewHosted(t, droptest.Options{ProbeSocket: sock, BetaNodes: []string{"official-node"}})
	admin := h.Token(h.Owner())
	_, alice := h.User("alice@example.test", "user")
	_, bob := h.User("bob@example.test", "user")
	node := h.MustOp(admin, "node.create", map[string]any{"name": "official-node", "host_id": hostA})
	_, nodeToken := h.NodeLogin("official-node", node["password"].(string))
	ready := reportOf(hostA)
	ready["nibrunner"].(map[string]any)["versions"].(map[string]any)["cpuQuarterQuota"] = true
	if code, body := report(t, h, nodeToken, ready); code != 200 {
		t.Fatal(body)
	}
	stage := func(product, version string, seed byte) string {
		t.Helper()
		body := droptest.ELF(4096, seed)
		digest := sha(body)
		input := map[string]any{"product": product, "version": version}
		wantError(t, h.Op(alice, "catalog.upload", input), 403, dropapi.CodeForbidden)
		path := h.MustOp(admin, "catalog.upload", input)["path"].(string)
		ch := make(chan error, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				ch <- err
				return
			}
			defer c.Close()
			reader := bufio.NewReader(c)
			if _, err = reader.ReadString('\n'); err == nil {
				_, err = io.CopyN(io.Discard, reader, int64(len(body)))
			}
			if err == nil {
				m := strings.NewReplacer(`"app":"crew"`, fmt.Sprintf(`"app":%q`, product), `"version":"0.45.4-drop.4"`, fmt.Sprintf(`"version":%q`, version), `"s3_replica",`, ``, `"minimum":`, `"optional":["s3_replica"],"minimum":`).Replace(validCrewManifest)
				if product == "atlas" {
					m = strings.ReplaceAll(strings.ReplaceAll(m, `"memory_mib":512`, `"memory_mib":256`), `"cpu_quarters":4`, `"cpu_quarters":1`)
				}
				err = json.NewEncoder(c).Encode(map[string]any{"outcome": "success", "manifest": json.RawMessage(m)})
			}
			ch <- err
		}()
		if res := h.Upload(path, body); res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
		if res := h.Upload(path, body); res.Code != 404 {
			t.Fatal("reused upload", res.Code)
		}
		return digest
	}
	rehearse := func(d, b string) {
		t.Helper()
		proof := map[string]any{"sha256": d, "baseline_sha256": b, "evidence_url": "https://evidence.example.test/rehearsal", "evidence_sha256": sha([]byte("fixture report")),
			"before_records_sha256": sha([]byte("record")), "after_records_sha256": sha([]byte("record")), "before_attachments_sha256": sha([]byte("file")), "after_attachments_sha256": sha([]byte("file")), "records": 1, "attachments": 1}
		wantError(t, h.Op(bob, "catalog.rehearse", proof), 403, dropapi.CodeForbidden)
		h.MustOp(admin, "catalog.rehearse", proof)
	}
	publish := func(d, b string) {
		ref := map[string]any{"sha256": d}
		wantError(t, h.Op(admin, "catalog.publish", ref), 409, "rehearsal_required")
		rehearse(d, b)
		h.MustOp(admin, "catalog.publish", ref)
	}
	crew := stage("crew", "1.0.0", 1)
	if rows := h.MustOp(alice, "catalog.list", nil)["releases"].([]any); len(rows) != 0 {
		t.Fatal(rows)
	}
	publish(crew, "")
	wantError(t, h.Op(admin, "catalog.activate", nil), 409, "release_unavailable")
	atlas := stage("atlas", "1.0.0", 2)
	publish(atlas, "")
	h.MustOp(admin, "catalog.activate", nil)
	wantError(t, h.Op(alice, "app.create", map[string]any{"name": "Legacy", "slug": "legacy"}), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(alice, "beta.import_url", map[string]any{"url": "https://example.test/file"}), 403, dropapi.CodeForbidden)
	create := func(user, product, name string) map[string]any {
		t.Helper()
		return appOf(t, h.MustOp(user, "workspace.create", map[string]any{"product": product, "name": name, "multiplier": 1}))
	}
	wantError(t, h.Op(alice, "workspace.create", map[string]any{"product": "crew", "name": "Too small", "multiplier": 1}), 400, dropapi.CodeInvalidInput)
	old := appOf(t, h.MustOp(alice, "workspace.create", map[string]any{"product": "crew", "name": "Historical Crew", "multiplier": 4}))
	wantError(t, h.Op(alice, "workspace.resize", map[string]any{"app": old["id"], "multiplier": 1}), 400, dropapi.CodeInvalidInput)
	a, b := create(alice, "atlas", "Research"), create(bob, "atlas", "Library")
	if a["official_product"] != "atlas" || a["name"] != "Research" {
		t.Fatal(a)
	}
	wantError(t, h.Op(bob, "workspace.resize", map[string]any{"app": a["id"], "multiplier": 2}), 404, dropapi.CodeNotFound)
	a2 := appOf(t, h.MustOp(alice, "workspace.resize", map[string]any{"app": a["id"], "multiplier": 8}))
	if a2["volume_id"] != a["volume_id"] || a2["disk_mib"] != a["disk_mib"] || a2["memory_mib"] != float64(2048) {
		t.Fatal(a2)
	}
	reportApp := func(a map[string]any, phase string) {
		t.Helper()
		ready["apps"] = []map[string]any{appReport(a["nib_app_id"].(string), a["deployment_id"].(string), phase)}
		if code, r := report(t, h, nodeToken, ready); code != 200 {
			t.Fatal(r)
		}
	}
	var units int
	if err := h.App.DB().NewQuery("SELECT units FROM drop_credit_executions WHERE deployment={:deployment}").Bind(dbx.Params{"deployment": a2["deployment_id"]}).Row(&units); err != nil || units != 8 {
		t.Fatalf("resize billing units %d: %v", units, err)
	}
	reportApp(a2, "running")
	newVersion := stage("atlas", "1.1.0", 3)
	publish(newVersion, atlas)
	up := map[string]any{"app": a["id"], "sha256": newVersion}
	wantError(t, h.Op(bob, "workspace.upgrade", up), 404, dropapi.CodeNotFound)
	r := h.Op(alice, "workspace.upgrade", up, "upgrade-atlas")
	if r.Status != 200 {
		t.Fatal(r.Raw)
	}
	upgraded := appOf(t, r.Result)
	if got := h.Op(alice, "workspace.upgrade", up, "upgrade-atlas"); got.Status != 200 || !got.Replayed {
		t.Fatal(got.Raw)
	}
	if upgraded["volume_id"] != a["volume_id"] || upgraded["hostname"] != a["hostname"] || upgraded["installed_version"] != "1.0.0" {
		t.Fatal(upgraded)
	}
	reportApp(upgraded, "failed")
	wantError(t, h.Op(alice, "workspace.upgrade", map[string]any{"app": a["id"], "sha256": atlas}), 409, "release_changed")
	retry := appOf(t, h.MustOp(alice, "workspace.upgrade", up))
	reportApp(retry, "running")
	if actual := appOf(t, h.MustOp(alice, "app.get", map[string]any{"app": a["id"]})); actual["installed_version"] != "1.1.0" {
		t.Fatal(actual)
	}
	if appOf(t, h.MustOp(bob, "app.get", map[string]any{"app": b["id"]}))["beta_sha256"] != atlas {
		t.Fatal("other owner's selection changed")
	}
	if third := create(bob, "atlas", "New library"); third["beta_sha256"] != newVersion {
		t.Fatal(third)
	}
	wantError(t, h.Op(alice, "user.directory", nil), 403, dropapi.CodeForbidden)
	if users := h.MustOp(admin, "user.directory", map[string]any{"search": "alice", "status": "active", "workspaces": "yes", "credits": "no"})["users"].([]any); len(users) != 1 {
		t.Fatal(users)
	}
}
