package dropserver_test

import (
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestOfficialDiskResizeKeepsSourceUntilMatchingTerminalReceipt(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, user := h.User("resize@example.test", "developer")
	id := deployedApp(t, h, user, "resizing", 1)
	other := deployedApp(t, h, user, "waiting", 2)
	dropserver.SetServices(h.App, dropserver.Services{Releases: h.Releases, Secrets: h.Secrets, BetaNodes: []string{"resize-node"}, AppDomain: droptest.AppDomain})
	a := appRecord(t, h, id)
	a.Set("official_product", "atlas")
	a.Set("memory_mib", 256)
	a.Set("disk_mib", 2048)
	if err := h.App.Save(a); err != nil {
		t.Fatal(err)
	}
	n := h.MustOp(admin, "node.create", map[string]any{"name": "resize-node", "host_id": hostA})
	_, token := h.NodeLogin("resize-node", n["password"].(string))
	body := reportOf(hostA)
	host := body["nibrunner"].(map[string]any)
	host["reportedAt"] = time.Now().UTC().Format(time.RFC3339)
	host["versions"].(map[string]any)["localVolumeResize"] = true
	host["versions"].(map[string]any)["cpuQuarterQuota"] = true
	host["allocatable"].(map[string]any)["memoryMib"] = 1024
	host["capacity"].(map[string]any)["memoryMib"] = 1024
	submit := func() {
		t.Helper()
		if code, result := report(t, h, token, body); code != 200 {
			t.Fatal(result)
		}
	}
	submit()
	h.MustOp(admin, "placement.set", map[string]any{"node": "resize-node", "app": id})
	resize := func(n int) map[string]any {
		t.Helper()
		return appOf(t, h.MustOp(user, "workspace.resize", map[string]any{"app": id, "multiplier": n}))
	}
	out := resize(4)
	if out["memory_mib"] != float64(256) || out["disk_mib"] != float64(2048) || out["disk_resize"].(map[string]any)["outcome"] != "pending" {
		t.Fatal(out)
	}
	sourceSeq := out["deployment_seq"]
	d, _ := desired(t, h, token)
	vol := d.Apps[0].Volume
	if vol.LocalResize == nil || vol.SizeBytes != 8192<<20 || vol.LocalResize.FromBytes != 2048<<20 || d.Apps[0].Resources.MemoryMib != 256 {
		t.Fatal(d)
	}
	requestID := vol.LocalResize.RequestID
	resize(4)
	d, _ = desired(t, h, token)
	if d.Apps[0].Volume.LocalResize.RequestID != requestID {
		t.Fatal("retry replaced request")
	}
	wantError(t, h.Op(admin, "placement.clear", map[string]any{"app": id}), 409, dropapi.CodeDiskLocked)
	// The pending 4× reservation already consumes the available 1024 MiB.
	wantError(t, h.Op(admin, "placement.set", map[string]any{"app": other, "node": "resize-node"}), 409, dropapi.CodeNodeTooSmall)
	receipt := func(rid, outcome string, mib int) {
		t.Helper()
		r := appReport(d.Apps[0].AppID, d.Apps[0].DeploymentID, "running")
		r["localResize"] = map[string]any{"outcome": outcome, "requestId": rid, "sizeBytes": int64(mib) << 20, "deploymentId": d.Apps[0].DeploymentID}
		body["apps"] = []map[string]any{r}
		submit()
	}
	receipt("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "applied", 8192)
	receipt(requestID, "applied", 2048)
	if appRecord(t, h, id).GetInt("disk_mib") != 2048 {
		t.Fatal("unmatched receipt committed")
	}
	receipt(requestID, "refused", 2048)
	if result := h.MustOp(user, "app.get", map[string]any{"app": id})["app"].(map[string]any); result["disk_resize"].(map[string]any)["outcome"] != "refused" || result["deployment_seq"] != sourceSeq {
		t.Fatal(result)
	}
	resize(4)
	d, _ = desired(t, h, token)
	nextID := d.Apps[0].Volume.LocalResize.RequestID
	if nextID == requestID {
		t.Fatal("new attempt reused refused ID")
	}
	receipt(nextID, "applied", 8192)
	receipt(nextID, "applied", 8192)
	d, _ = desired(t, h, token)
	if d.Apps[0].Volume.LocalResize != nil || d.Apps[0].Volume.SizeBytes != 8192<<20 || d.Apps[0].Resources.MemoryMib != 1024 {
		t.Fatal(d)
	}
	if got := appRecord(t, h, id).GetInt("deployment_seq"); got != int(sourceSeq.(float64))+1 {
		t.Fatalf("duplicate receipt bumped sequence: %d", got)
	}
}
