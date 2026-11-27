package dropserver_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestVolumeDowngradeReasons(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, user := h.User("volume@example.test", "developer")
	a := appOf(t, h.MustOp(user, "app.create", map[string]any{"name": "volume", "slug": "volume", "disk_mib": 4096, "memory_mib": 512}))
	n := h.MustOp(admin, "node.create", map[string]any{"name": "volume-node", "host_id": hostA})
	_, token := h.NodeLogin("volume-node", n["password"].(string))
	h.MustOp(admin, "placement.set", map[string]any{"node": "volume-node", "app": a["id"]})
	app := appRecord(t, h, a["id"].(string))
	app.Set("official_product", "crew")
	if err := h.App.Save(app); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	dropserver.SetAgentClock(h.App, func() time.Time { return now })
	check := func(reason string) {
		t.Helper()
		r := h.Op(user, "workspace.resize", map[string]any{"app": app.Id, "multiplier": 1})
		wantError(t, r, 409, dropapi.CodeDiskLocked)
		if !strings.Contains(r.Error["message"].(string), reason) {
			t.Fatal(r.Raw)
		}
	}
	check("unavailable")
	submit := func(used int64) {
		t.Helper()
		nib := app.GetString("nib_app_id")
		r := appReport(nib, fmt.Sprintf("%s-dep-%d", nib, app.GetInt("deployment_seq")), "running")
		r["volumeUsage"] = map[string]any{"usedBytes": used, "totalBytes": int64(4000) << 20, "measuredAt": now.Format(time.RFC3339)}
		body := reportOf(hostA, r)
		body["nibrunner"].(map[string]any)["reportedAt"] = now.Format(time.RFC3339)
		if code, result := report(t, h, token, body); code != 200 {
			t.Fatal(result)
		}
	}
	submit(int64(2048)<<20 + 1)
	check("2049 MiB")
	now = now.Add(time.Minute)
	submit(int64(1200) << 20)
	check("within this tier")
	now = now.Add(6 * time.Minute)
	check("unavailable")
	if fresh := appRecord(t, h, app.Id); fresh.GetInt("disk_mib") != 4096 || fresh.GetInt("memory_mib") != 512 {
		t.Fatal("refused downgrade changed app")
	}
}
