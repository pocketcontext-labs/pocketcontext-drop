package dropserver_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

const validCrewManifest = `{"schema":"drop.manifest/v1","app":"crew","version":"0.45.4-drop.4","http_port":8080,"hook_port":8081,"health":"tcp","hooks":["suspend","resume","terminate"],"requires":["persistent_volume","s3_replica","verified_owner_email","sign_in_email"],"minimum":{"memory_mib":512,"cpu_quarters":4,"disk_mib":1024}}`

func TestBetaProbeLeasesAndDeployment(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{BetaNodes: []string{"beta-node"}})
	admin := h.Token(h.Owner())
	_, alice := h.User("alice@example.test", "user")
	_, bob := h.User("bob@example.test", "user")
	body := droptest.ELF(4096, 7)
	digest := sha(body)
	ref := map[string]any{"sha256": digest}
	for _, op := range []string{"beta.approve", "beta.revoke", "beta.claim", "beta.complete"} {
		wantError(t, h.Op(alice, op, ref), 403, dropapi.CodeForbidden)
	}
	wantError(t, h.Op(alice, "beta.inspect", ref), 409, "unsupported_binary")
	h.MustOp(admin, "beta.approve", ref)
	wantError(t, h.Op(alice, "app.create", map[string]any{"name": "Alternate", "slug": "alternate"}), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(alice, "beta.inspect", ref), 409, "probe_pending")
	attempt := h.MustOp(admin, "beta.claim", ref)["attempt"]
	wantError(t, h.Op(admin, "beta.claim", ref), 409, "probe_busy")
	if _, err := h.App.DB().NewQuery("UPDATE drop_beta_probes SET expires_at=0").Execute(); err != nil {
		t.Fatal(err)
	}
	current := h.MustOp(admin, "beta.claim", ref)["attempt"]
	complete := map[string]any{"sha256": digest, "outcome": "success", "manifest": json.RawMessage(validCrewManifest), "attempt": attempt}
	wantError(t, h.Op(admin, "beta.complete", complete), 409, "probe_expired")
	complete["attempt"] = current
	h.MustOp(admin, "beta.complete", complete)
	if r := h.MustOp(admin, "beta.claim", ref); r["cached"] != true {
		t.Fatal(r)
	}
	if r := h.MustOp(alice, "beta.inspect", ref); r["name"] != "PocketContext Crew" || len(r["multipliers"].([]any)) != 5 {
		t.Fatal(r)
	}
	for _, bad := range []any{1, 3, 12, 4.5} {
		wantError(t, h.Op(alice, "beta.prepare", map[string]any{"sha256": digest, "multiplier": bad}), 400, dropapi.CodeInvalidInput)
	}
	args := map[string]any{"sha256": digest, "multiplier": 5}
	prepared := h.Op(alice, "beta.prepare", args, "prepare-1")
	if prepared.Status != 200 {
		t.Fatal(prepared.Raw)
	}
	a := appOf(t, prepared.Result)
	if a["beta_managed"] != true || num(a["vcpu"]) != 2 || num(a["memory_mib"]) != 1280 || num(a["disk_mib"]) != 10240 {
		t.Fatal(a)
	}
	app := a["id"].(string)
	if other := h.Op(alice, "beta.prepare", args, "prepare-1"); other.Status != 200 || appOf(t, other.Result)["id"] != app {
		t.Fatal(other.Raw)
	}
	wantError(t, h.Op(alice, "app.set", map[string]any{"app": app, "vcpu": 2}), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(alice, "release.upload_link", map[string]any{"app": app, "activate": true}), 403, dropapi.CodeForbidden)
	path := uploadLink(t, h, alice, app, nil)
	wantUploadError(t, upload(t, h, path, droptest.ELF(4096, 8)), 409, "unsupported_binary")
	up := upload(t, h, path, body)
	if up.Status != 200 || up.Result.Deployed {
		t.Fatal(up.Raw)
	}
	deploy := map[string]any{"app": app, "release": up.Result.Release.ID}
	wantError(t, h.Op(bob, "beta.deploy", deploy), 404, dropapi.CodeNotFound)
	wantError(t, h.Op(alice, "release.deploy", deploy), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(alice, "beta.deploy", deploy), 409, "beta_no_capacity")
	node := h.MustOp(admin, "node.create", map[string]any{"name": "beta-node", "host_id": hostA})
	_, token := h.NodeLogin("beta-node", node["password"].(string))
	if code, r := report(t, h, token, reportOf(hostA)); code != http.StatusOK {
		t.Fatal(code, r)
	}
	wantError(t, h.Op(alice, "beta.deploy", deploy), 409, "beta_no_capacity")
	ready := reportOf(hostA)
	ready["nibrunner"].(map[string]any)["versions"].(map[string]any)["cpuQuarterQuota"] = true
	if code, r := report(t, h, token, ready); code != http.StatusOK {
		t.Fatal(code, r)
	}
	if r := h.MustOp(alice, "beta.deploy", deploy); r["deployment_bumped"] != true {
		t.Fatal(r)
	}
	if r := h.MustOp(alice, "beta.deploy", deploy); r["deployment_bumped"] != false {
		t.Fatal(r)
	}
	if appRecord(t, h, app).GetDateTime("first_placed_at").IsZero() {
		t.Fatal("placement did not lock disk")
	}
	h.MustOp(admin, "beta.revoke", ref)
	wantError(t, h.Op(alice, "beta.deploy", deploy), 409, "unsupported_binary")
	wantError(t, h.Op(alice, "app.start", map[string]any{"app": app}), 409, "unsupported_binary")
	if appRecord(t, h, app).GetString("desired_state") != "stopped" {
		t.Fatal("revocation did not stop app")
	}
	h.MustOp(admin, "beta.approve", ref)
	if appRecord(t, h, app).GetString("desired_state") != "stopped" {
		t.Fatal("approval restarted the app")
	}
	h.MustOp(alice, "beta.deploy", deploy)
	if appRecord(t, h, app).GetString("desired_state") == "stopped" {
		t.Fatal("explicit deployment did not restart approved app")
	}
}

func TestBetaConcurrentInspectionAndNegativeCache(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	ref := map[string]any{"sha256": sha(droptest.ELF(4096, 13))}
	h.MustOp(admin, "beta.approve", ref)
	results := make(chan droptest.OpResult, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			results <- h.Op(admin, "beta.claim", ref)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if second.Status == http.StatusOK {
		first, second = second, first
	}
	if first.Status != http.StatusOK {
		t.Fatal(first.Raw, second.Raw)
	}
	wantError(t, second, 409, "probe_busy")
	reply := map[string]any{"sha256": ref["sha256"], "attempt": first.Result["attempt"], "outcome": "success", "manifest": map[string]any{"schema": "other"}}
	reply["outcome"] = "retry"
	delete(reply, "manifest")
	h.MustOp(admin, "beta.complete", reply)
	claim := h.MustOp(admin, "beta.claim", ref)
	reply["attempt"] = claim["attempt"]
	reply["outcome"] = "success"
	reply["manifest"] = map[string]any{"schema": "incompatible"}
	h.MustOp(admin, "beta.complete", reply)
	if r := h.MustOp(admin, "beta.claim", ref); r["cached"] != true || r["state"] != "invalid" {
		t.Fatal(r)
	}
	wantError(t, h.Op(admin, "beta.inspect", ref), 409, "unsupported_binary")
}
