package dropserver_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/releasebundle"
)

const validCrewManifest = `{"schema":"drop.manifest/v1","app":"crew","version":"0.45.4-drop.4","http_port":8080,"hook_port":8081,"health":"tcp","hooks":["suspend","resume","terminate"],"requires":["persistent_volume","s3_replica","verified_owner_email","sign_in_email"],"minimum":{"memory_mib":512,"cpu_quarters":4,"disk_mib":1024}}`

func TestBetaProbeLeasesAndDeployment(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{BetaNodes: []string{"beta-node"}, BetaOwnerNodes: []string{"owner-node"}})
	admin := h.Token(h.Owner())
	_, alice := h.User("alice@example.test", "user")
	_, bob := h.User("bob@example.test", "user")
	body := droptest.ELF(4096, 7)
	digest := sha(body)
	ref := map[string]any{"sha256": digest}
	for _, op := range []string{"beta.approve", "beta.revoke", "beta.claim", "beta.complete"} {
		wantError(t, h.Op(alice, op, ref), 403, dropapi.CodeForbidden)
	}
	wantError(t, h.Op(alice, "beta.inspect", ref), 409, "probe_pending")
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
	if r := h.MustOp(alice, "beta.inspect", ref); r["name"] != "PocketContext Crew" || len(r["multipliers"].([]any)) != 8 || num(r["suggested_multiplier"]) != 4 {
		t.Fatal(r)
	}
	for _, bad := range []any{-1, 0, 9, 12, 4.5} {
		wantError(t, h.Op(alice, "beta.prepare", map[string]any{"sha256": digest, "multiplier": bad}), 400, dropapi.CodeInvalidInput)
	}
	wantError(t, h.Op(alice, "beta.prepare", map[string]any{"sha256": digest, "multiplier": 1}), 409, "storage_credits_required")
	for i, user := range []string{alice, bob} {
		result := h.Op(user, "credits.buy", map[string]any{}, "storage-pack-"+string(rune('a'+i)))
		if result.Status != 200 {
			t.Fatal(result.Raw)
		}
	}
	small := appOf(t, h.MustOp(bob, "beta.prepare", map[string]any{"sha256": digest, "multiplier": 1}))
	if num(small["memory_mib"]) != 256 || num(small["disk_mib"]) != 2048 || num(small["vcpu"]) != 1 {
		t.Fatal(small)
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
	ownerNode := h.MustOp(admin, "node.create", map[string]any{"name": "owner-node", "host_id": "owner-node"})
	_, ownerNodeToken := h.NodeLogin("owner-node", ownerNode["password"].(string))
	ownerReport := reportOf("owner-node")
	ownerReport["nibrunner"].(map[string]any)["versions"].(map[string]any)["cpuQuarterQuota"] = true
	if code, r := report(t, h, ownerNodeToken, ownerReport); code != http.StatusOK {
		t.Fatal(code, r)
	}
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
	if result := h.MustOp(admin, "beta.inspect", ref); result["generic"] != true {
		t.Fatal(result)
	}
	_, user := h.User("generic@example.test", "user")
	args := map[string]any{"sha256": ref["sha256"], "multiplier": 1, "http_port": 4000}
	app := appOf(t, h.MustOp(user, "beta.prepare", args))
	if app["kind"] != "ordinary" || num(app["http_port"]) != 4000 || app["sign_in_email_enabled"] != false || app["crew_owner_email"] != nil {
		t.Fatal(app)
	}
	if app["env"].(map[string]any)["PORT"] != "4000" {
		t.Fatal(app)
	}
	args["http_port"] = 65536
	wantError(t, h.Op(user, "beta.prepare", args), 400, dropapi.CodeInvalidInput)
}

func TestBetaSuggestionUsesReleaseProfile(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	for i, profile := range []struct{ memory, cpu, disk, want int }{
		{1, 1, 1, 1}, {1025, 1, 1, 5}, {1, 2, 12289, 7}, {1, 20, 1, 8},
	} {
		digest := sha(droptest.ELF(4096, byte(i)))
		ref := map[string]any{"sha256": digest}
		h.MustOp(admin, "beta.approve", ref)
		attempt := h.MustOp(admin, "beta.claim", ref)["attempt"]
		var m dropapi.BetaManifest
		if err := json.Unmarshal([]byte(validCrewManifest), &m); err != nil {
			t.Fatal(err)
		}
		m.Minimum.MemoryMiB, m.Minimum.CPUQuarters, m.Minimum.DiskMiB = profile.memory, profile.cpu, profile.disk
		h.MustOp(admin, "beta.complete", map[string]any{"sha256": digest, "attempt": attempt, "outcome": "success", "manifest": m})
		r := h.MustOp(admin, "beta.inspect", ref)
		if num(r["suggested_multiplier"]) != profile.want || len(r["multipliers"].([]any)) != 8 {
			t.Fatal(r)
		}
	}
}

func TestBetaIndependentServicesAndStorageEntitlement(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		required                    []string
		optional                    []string
		storage, email, owner, paid bool
	}{
		{name: "paid-storage", required: []string{"s3_replica"}, storage: true, paid: true},
		{name: "trial-storage", optional: []string{"s3_replica"}, storage: true},
		{name: "mail-only", required: []string{"sign_in_email"}, email: true},
		{name: "owner-only", required: []string{"verified_owner_email"}, owner: true},
		{name: "no-services"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := droptest.NewHosted(t, droptest.Options{BetaNodes: []string{"manifest-node"}})
			admin := h.Token(h.Owner())
			_, user := h.User("manifest@example.test", "user")
			_, _, node := h.Node("manifest-node", hostA)
			heartbeat := reportOf(hostA)
			heartbeat["nibrunner"].(map[string]any)["versions"].(map[string]any)["cpuQuarterQuota"] = true
			if code, reply := report(t, h, node, heartbeat); code != 200 {
				t.Fatal(code, reply)
			}
			body := droptest.ELF(4096, 1)
			digest := sha(body)
			ref := map[string]any{"sha256": digest}
			m := map[string]any{"schema": "drop.manifest/v1", "app": "sample", "version": "1", "http_port": 8080, "hook_port": 0, "health": "tcp", "hooks": []string{}, "requires": tc.required, "optional": tc.optional, "minimum": map[string]any{"cpu_quarters": 1, "memory_mib": 256, "disk_mib": 2048}}
			attempt := h.MustOp(admin, "beta.claim", ref)["attempt"]
			h.MustOp(admin, "beta.complete", map[string]any{"sha256": digest, "attempt": attempt, "outcome": "success", "manifest": m})
			args := map[string]any{"sha256": digest, "multiplier": 1}
			if result := h.MustOp(user, "beta.inspect", ref); result["generic"] != false || result["needs_storage_credits"] != tc.paid {
				t.Fatal(result)
			}
			if tc.paid {
				wantError(t, h.Op(user, "beta.prepare", args), 409, "storage_credits_required")
				if r := h.Op(user, "credits.buy", nil, "storage"); r.Status != 200 {
					t.Fatal(r.Raw)
				}
			}
			app := appOf(t, h.MustOp(user, "beta.prepare", args))["id"].(string)
			path := uploadLink(t, h, user, app, nil)
			uploaded := upload(t, h, path, body)
			if uploaded.Status != 200 {
				t.Fatal(uploaded.Raw)
			}
			deploy := map[string]any{"app": app, "release": uploaded.Result.Release.ID}
			h.MustOp(user, "beta.deploy", deploy)
			d, raw := desired(t, h, node)
			if len(d.Apps) != 1 {
				t.Fatal(raw)
			}
			got := d.Apps[0]
			if got.ManagedCrew != tc.storage || got.SignInEmailEnabled != tc.email || (got.CrewOwnerEmail != "" && !got.PrivateOwnerEmail) != tc.owner || got.ProxyOnly != tc.paid || got.LocalOnly != (tc.storage && !tc.paid) {
				t.Fatal(raw)
			}
			if tc.storage && !tc.paid {
				if r := h.Op(user, "credits.buy", nil, "later"); r.Status != 200 {
					t.Fatal(r.Raw)
				}
				before, _ := desired(t, h, node)
				if before.Apps[0].LocalOnly || !before.Apps[0].ProxyOnly || before.Apps[0].DeploymentID == got.DeploymentID {
					t.Fatal("top-up did not switch this workspace to funded storage")
				}
				h.MustOp(user, "beta.deploy", deploy)
				after, _ := desired(t, h, node)
				if !after.Apps[0].ProxyOnly || after.Apps[0].LocalOnly {
					t.Fatal("redeployment did not grant proxy-only storage")
				}
			}
		})
	}
}

func TestGenericArchiveUpdateKeepsPlacementAndReleaseProvenance(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{BetaNodes: []string{"beta-node"}})
	admin := h.Token(h.Owner())
	_, owner := h.User("archive@example.test", "user")
	_, other := h.User("other@example.test", "user")
	n := h.MustOp(admin, "node.create", map[string]any{"name": "beta-node", "host_id": hostA})
	_, agent := h.NodeLogin("beta-node", n["password"].(string))
	if code, r := report(t, h, agent, reportOf(hostA)); code != 200 {
		t.Fatal(code, r)
	}
	var app string
	var volume string
	for i := range 2 {
		executable := droptest.ELF(512, byte(i))
		archiveSHA := sha([]byte{byte(i)})
		m := releasebundle.Manifest{ArchiveSHA256: archiveSHA, ExecutableSHA256: sha(executable), Executable: "demo", Files: []releasebundle.Member{{Path: "demo", Size: int64(len(executable))}}}
		meta, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		body.Write(droptest.ELF(512, 10))
		start := body.Len()
		gz := gzip.NewWriter(&body)
		if err = binary.Write(gz, binary.LittleEndian, uint32(len(meta))); err != nil {
			t.Fatal(err)
		}
		if _, err = gz.Write(meta); err != nil {
			t.Fatal(err)
		}
		if _, err = gz.Write(executable); err != nil {
			t.Fatal(err)
		}
		if err = gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err = binary.Write(&body, binary.LittleEndian, uint32(body.Len()-start)); err != nil {
			t.Fatal(err)
		}
		body.WriteString("DRP1")
		digest := sha(body.Bytes())
		ref := map[string]any{"sha256": digest}
		claim := h.MustOp(admin, "beta.claim", ref)
		h.MustOp(admin, "beta.complete", map[string]any{"sha256": digest, "attempt": claim["attempt"], "outcome": "invalid"})
		if app == "" {
			created := appOf(t, h.MustOp(owner, "beta.prepare", map[string]any{"sha256": digest, "multiplier": 4, "http_port": 8090}))
			app = created["id"].(string)
			volume = created["volume_id"].(string)
		}
		up := upload(t, h, uploadLink(t, h, owner, app, nil), body.Bytes())
		if up.Status != 200 || up.Result.Release.ArchiveSHA256 != archiveSHA || up.Result.Release.ExecutableSHA256 != sha(executable) || up.Result.Release.ExecutablePath != "demo" {
			t.Fatal(up.Raw)
		}
		deploy := map[string]any{"app": app, "release": up.Result.Release.ID}
		wantError(t, h.Op(other, "beta.deploy", deploy), 404, dropapi.CodeNotFound)
		updated := appOf(t, h.MustOp(owner, "beta.deploy", deploy))
		if updated["volume_id"] != volume || updated["beta_sha256"] != digest || updated["current_release"] != up.Result.Release.ID {
			t.Fatal(updated)
		}
		wantError(t, h.Op(other, "release.list", map[string]any{"app": app}), 404, dropapi.CodeNotFound)
	}
}
