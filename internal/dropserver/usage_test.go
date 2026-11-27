package dropserver_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/leakscan"
)

// Synthetic access-log records in S3's format. The requester, address and
// request id are sentinels: none of them may reach the database.
const (
	logIP        = "198.51.100.77"
	logRequester = "arn:aws:sts::111122223333:assumed-role/workloads/synthetic-session"
	logRequestID = "SYNTHREQUESTID0001"
)

func logLine(at time.Time, op, key, uri string, status int, sent, size string) string {
	return fmt.Sprintf(`0000 crew-replica-fixture [%s] %s %s %s %s %s "%s" %d - %s %s 10 9 "-" "synthetic-agent" - SYNTHHOSTID= SigV4 TLS_AES_128_GCM_SHA256 AuthHeader crew-replica-fixture.s3.eu-north-1.amazonaws.com TLSv1.3 - -`,
		at.UTC().Format("02/Jan/2006:15:04:05 -0700"), logIP, logRequester, logRequestID, op, key, uri, status, sent, size)
}

// gets returns n GET records of one object of an app.
func gets(at time.Time, nib string, n int, sent int) string {
	var b strings.Builder
	for range n {
		b.WriteString(logLine(at, "REST.GET.OBJECT", nib+"/db", "GET /"+nib+"/db HTTP/1.1", 200, fmt.Sprint(sent), "100") + "\n")
	}
	return b.String()
}

func logKey(at time.Time, name string) string {
	return droptest.UsageConfig.LogPrefix + at.UTC().Format("2006/01/02/") + at.UTC().Format("2006-01-02") + "-00-00-00-" + name
}

func collect(t *testing.T, h *droptest.Hosted) map[string]any {
	t.Helper()
	r, err := dropserver.CollectUsage(context.Background(), h.App, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"processed": r.ObjectsProcessed, "malformed": r.Malformed, "alerts": r.Alerts, "stopped": len(r.Stopped),
		"inventory": r.InventoryDay, "created": r.FiltersCreated, "deleted": r.FiltersDeleted, "failed": r.ObjectsFailed}
}

func hourlySum(t *testing.T, h *droptest.Hosted, app, field string) int {
	t.Helper()
	var n int
	if err := h.App.DB().NewQuery("SELECT COALESCE(SUM(" + field + "), 0) FROM app_usage_hourly WHERE app = {:app}").Bind(dbx.Params{"app": app}).Row(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func usageEvents(t *testing.T, h *droptest.Hosted, app, action string) int {
	t.Helper()
	n, err := h.App.CountRecords("events", dbx.HashExp{"app": app, "action": action})
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

// TestUsageCollectEndToEnd: fake AWS logs -> hourly rows -> soft and hard
// limit events -> the app stops through the stop path, once; the cursor
// never counts an object twice, even with two runs at once.
func TestUsageCollectEndToEnd(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{Usage: true})
	owner := h.Token(h.Owner())
	_, a := h.User("a@example.test", "developer")
	_, b := h.User("b@example.test", "developer")
	n1, _, _ := h.Node("node-one", hostA)
	appA := deployedApp(t, h, a, "app-of-a", 1)
	appB := deployedApp(t, h, b, "app-of-b", 2)
	h.MustOp(owner, "placement.set", map[string]any{"app": appA, "node": n1})
	h.MustOp(a, "app.set", map[string]any{"app": appA, "desired_state": "running"})
	nibA, nibB := appRecord(t, h, appA).GetString("nib_app_id"), appRecord(t, h, appB).GetString("nib_app_id")
	now := time.Now().UTC()

	h.Usage.PutObject(logKey(now, "0001"), []byte(gets(now, nibA, 2, 1000)+gets(now, nibB, 1, 10)+"garbage line\n"+
		logLine(now, "REST.PUT.OBJECT", nibA+"/db", "PUT /"+nibA+"/db HTTP/1.1", 200, "-", "4096")+"\n"+
		logLine(now, "REST.GET.OBJECT", nibB+"/x", "GET /"+nibB+"/x HTTP/1.1", 403, "243", "-")+"\n"))
	if r := collect(t, h); r["processed"] != 1 || r["malformed"] != 1 {
		t.Fatalf("first run %v", r)
	}
	if hourlySum(t, h, appA, "class_b") != 2 || hourlySum(t, h, appA, "class_a") != 1 || hourlySum(t, h, appA, "bytes_in") != 4096 || hourlySum(t, h, appA, "bytes_out") != 2000 {
		t.Fatal("app A hourly counts")
	}
	if hourlySum(t, h, appB, "class_b") != 1 || hourlySum(t, h, appB, "denied") != 1 {
		t.Fatal("app B hourly counts")
	}
	// Replays: the same objects again, and two runs at once, count nothing.
	if r := collect(t, h); r["processed"] != 0 {
		t.Fatalf("replay %v", r)
	}
	h.Usage.PutObject(logKey(now, "0002"), []byte(gets(now, nibA, 3, 0)))
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { collect(t, h) })
	}
	wg.Wait()
	if hourlySum(t, h, appA, "class_b") != 5 {
		t.Fatalf("after concurrent runs: %d", hourlySum(t, h, appA, "class_b"))
	}
	// An object of yesterday's partition is read too (late delivery).
	yesterday := now.Add(-24 * time.Hour)
	h.Usage.PutObject(logKey(yesterday, "0003"), []byte(gets(yesterday, nibB, 1, 0)))
	collect(t, h)
	if hourlySum(t, h, appB, "class_b") != 2 {
		t.Fatal("yesterday's object")
	}

	// Limits: an admin lowers A's Class B soft limit to 6 (hard 60).
	wantError(t, h.Op(a, "usage.set_limits", map[string]any{"app": appA, "requests_b": 6}), 403, "forbidden")
	wantError(t, h.Op(owner, "usage.set_limits", map[string]any{"app": appA, "requests_b": 0}), 400, "invalid_input")
	wantError(t, h.Op(owner, "usage.set_limits", map[string]any{"app": appA}), 400, "invalid_input")
	set := h.MustOp(owner, "usage.set_limits", map[string]any{"app": appA, "requests_b": 6})
	if num(set["limits"].(map[string]any)["requests_b"]) != 6 || num(set["limits"].(map[string]any)["requests_a"]) != 100000 {
		t.Fatalf("set_limits %v", set)
	}
	h.Usage.PutObject(logKey(now, "0004"), []byte(gets(now, nibA, 1, 0)))
	seq := seqOf(t, h, n1)
	if r := collect(t, h); r["alerts"] != 1 || r["stopped"] != 0 {
		t.Fatalf("soft %v", r)
	}
	if usageEvents(t, h, appA, "app.usage_exceeded") != 1 || appRecord(t, h, appA).GetString("desired_state") != "running" {
		t.Fatal("soft limit")
	}
	collect(t, h)
	if usageEvents(t, h, appA, "app.usage_exceeded") != 1 {
		t.Fatal("soft alert raised twice")
	}
	if seqOf(t, h, n1) != seq {
		t.Fatal("a soft alert changed the desired document")
	}
	h.Usage.PutObject(logKey(now, "0005"), []byte(gets(now, nibA, 60, 0)))
	if r := collect(t, h); r["alerts"] != 1 || r["stopped"] != 1 {
		t.Fatalf("hard %v", r)
	}
	if appRecord(t, h, appA).GetString("desired_state") != "stopped" || seqOf(t, h, n1) != seq+1 {
		t.Fatal("hard limit did not stop the app through the stop path")
	}
	events, err := h.App.FindRecordsByFilter("events", "app = {:app} && action = 'app.usage_exceeded'", "created", 0, 0, dbx.Params{"app": appA})
	if err != nil || len(events) != 2 || !strings.Contains(events[1].GetString("detail"), `"stopped":true`) ||
		!strings.Contains(events[1].GetString("detail"), "stopped the app automatically") {
		t.Fatalf("hard event %v", events)
	}
	// Started again, the app is not stopped a second time this month.
	h.MustOp(a, "app.start", map[string]any{"app": appA})
	h.Usage.PutObject(logKey(now, "0006"), []byte(gets(now, nibA, 5, 0)))
	if r := collect(t, h); r["alerts"] != 0 || r["stopped"] != 0 || appRecord(t, h, appA).GetString("desired_state") != "running" {
		t.Fatalf("stopped twice: %v", r)
	}
	if usageEvents(t, h, appB, "app.usage_exceeded") != 0 {
		t.Fatal("B was alerted")
	}

	// usage.get: own app, admins any; another account's is 404.
	got := h.MustOp(a, "usage.get", map[string]any{"app": "app-of-a"})
	if num(got["month_to_date"].(map[string]any)["class_b"]) != 71 || got["metered"] != true || len(got["exceeded"].([]any)) != 2 {
		t.Fatalf("usage %v", got)
	}
	if hourly := got["hourly"].([]any); len(hourly) == 0 {
		t.Fatal("no hourly rows")
	}
	var level string
	for _, l := range got["limits"].([]any) {
		if l := l.(map[string]any); l["metric"] == "requests_b" {
			level = l["level"].(string)
		}
	}
	if level != "hard" {
		t.Fatalf("level %q", level)
	}
	wantError(t, h.Op(b, "usage.get", map[string]any{"app": appA}), 404, "not_found")
	wantError(t, h.Op(b, "usage.set_limits", map[string]any{"app": appA, "requests_b": 1}), 403, "forbidden")
	if got := h.MustOp(owner, "usage.get", map[string]any{"app": appB}); num(got["month_to_date"].(map[string]any)["class_b"]) != 2 {
		t.Fatalf("admin usage of B %v", got)
	}
	// usage.collect: admins only; one event with counts.
	wantError(t, h.Op(a, "usage.collect", nil), 403, "forbidden")
	if r := h.MustOp(owner, "usage.collect", nil); num(r["objects_processed"]) != 0 {
		t.Fatalf("collect op %v", r)
	}
	if n, _ := h.App.CountRecords("events", dbx.HashExp{"action": "usage.collect"}); n != 1 {
		t.Fatal("usage.collect event")
	}

	// Metadata only: no requester, address or request id is stored.
	leakscan.Require(t, []leakscan.Sentinel{{Label: "log address", Value: logIP}, {Label: "log requester", Value: logRequester}, {Label: "log request id", Value: logRequestID}},
		leakscan.DB("database", h.App.DB()))
}

func TestUsageCollectUnavailable(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	wantError(t, h.Op(h.Token(h.Owner()), "usage.collect", nil), 503, "usage_unavailable")
	_, a := h.User("a@example.test", "developer")
	app := createApp(t, h, mustUser(t, h, "a@example.test"), "plain-app")
	if got := h.MustOp(a, "usage.get", map[string]any{"app": app}); got["metered"] != false || got["stored"] != nil {
		t.Fatalf("usage %v", got)
	}
}

func gz(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte(text))
	w.Close()
	return buf.Bytes()
}

// TestUsageInventory: the newest daily report becomes app_usage_daily, once;
// stored bytes past the hard limit stop the app.
func TestUsageInventory(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{Usage: true})
	owner := h.Token(h.Owner())
	_, a := h.User("a@example.test", "developer")
	appA := createApp(t, h, mustUser(t, h, "a@example.test"), "app-of-a")
	nibA := appRecord(t, h, appA).GetString("nib_app_id")
	h.MustOp(a, "app.set", map[string]any{"app": appA, "desired_state": "running"})
	h.MustOp(owner, "usage.set_limits", map[string]any{"app": appA, "stored_bytes": 100})
	inv := droptest.UsageConfig.InventoryPrefix
	today := time.Now().UTC().Format("2006-01-02")
	old := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02")
	data := inv + "data/00000000-0000-4000-8000-000000000001.csv.gz"
	manifest := fmt.Sprintf(`{"sourceBucket":"crew-replica-fixture","fileFormat":"CSV","fileSchema":"Bucket, Key, VersionId, IsLatest, IsDeleteMarker, Size, LastModifiedDate, StorageClass, IsMultipartUploaded","files":[{"key":%q,"size":1}]}`, data)
	h.Usage.PutObject(inv+old+"T01-00Z/manifest.json", []byte(`{"fileFormat":"CSV","fileSchema":"Key, IsLatest, Size","files":[]}`))
	h.Usage.PutObject(inv+today+"T01-00Z/manifest.json", []byte(manifest))
	h.Usage.PutObject(inv+today+"T01-00Z/manifest.checksum", []byte("x"))
	h.Usage.PutObject(data, gz(t, fmt.Sprintf(`"b","%[1]s/db","v2","true","false","700","t","STANDARD","false"
"b","%[1]s/db","v1","false","false","400","t","STANDARD","false"
"b","%[1]s/gone","v1","true","true","","t","",""
"b","releases/x","v1","true","false","9","t","STANDARD","false"
`, nibA)))
	if r := collect(t, h); r["inventory"] != today || r["stopped"] != 1 {
		t.Fatalf("inventory %v", r)
	}
	row, err := h.App.FindFirstRecordByFilter("app_usage_daily", "app = {:app}", dbx.Params{"app": appA})
	if err != nil || row.GetString("day") != today || row.GetInt("current_bytes") != 700 || row.GetInt("noncurrent_bytes") != 400 || row.GetInt("objects") != 1 || row.GetInt("versions") != 2 {
		t.Fatalf("daily %v %v", row, err)
	}
	if r := collect(t, h); r["inventory"] != "" {
		t.Fatalf("the same report was read again: %v", r)
	}
	if appRecord(t, h, appA).GetString("desired_state") != "stopped" {
		t.Fatal("storage hard limit")
	}
	got := h.MustOp(a, "usage.get", map[string]any{"app": appA})
	if s := got["stored"].(map[string]any); num(s["current_bytes"]) != 700 || s["day"] != today {
		t.Fatalf("stored %v", s)
	}
}

// TestUsageMetricsFilters: a Crew app gets a request-metrics filter on the
// replica bucket when created and loses it when archived; failures never
// block the operation, are recorded, and the collector reconciles them.
func TestUsageMetricsFilters(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{Usage: true})
	const alice = "alice@example.test"
	h.Invite(alice, "developer")
	a := h.SignIn(alice).Token
	plain := appOf(t, h.MustOp(a, "app.create", map[string]any{"name": "Plain", "slug": "plain-app"}))
	crew := appOf(t, h.MustOp(a, "app.create", map[string]any{"name": "Crew", "slug": "crew-app", "kind": "crew"}))
	id, nib := crew["id"].(string), crew["nib_app_id"].(string)
	if f := h.Usage.Filters(); len(f) != 1 || f[nib] != nib+"/" {
		t.Fatalf("filters %v", f)
	}
	if usageEvents(t, h, id, "app.metrics_filter") != 1 || usageEvents(t, h, plain["id"].(string), "app.metrics_filter") != 0 {
		t.Fatal("filter events")
	}
	h.MustOp(a, "app.archive", map[string]any{"app": id})
	if len(h.Usage.Filters()) != 0 {
		t.Fatal("archived app kept its filter")
	}
	// A failing store: the restore still succeeds, the event says so, and the
	// next collector run creates the filter.
	h.Usage.FailMetrics = errors.New("synthetic outage")
	h.MustOp(a, "app.restore", map[string]any{"app": id})
	failed, _ := h.App.FindRecordsByFilter("events", "app = {:app} && action = 'app.metrics_filter'", "-created", 1, 0, dbx.Params{"app": id})
	if len(failed) != 1 || !strings.Contains(failed[0].GetString("detail"), `"ok":false`) || strings.Contains(failed[0].GetString("detail"), "synthetic outage") {
		t.Fatalf("failure event %v", failed)
	}
	h.Usage.FailMetrics = nil
	if r := collect(t, h); r["created"] != 1 {
		t.Fatalf("reconcile %v", r)
	}
	if f := h.Usage.Filters(); f[nib] != nib+"/" {
		t.Fatalf("filters %v", f)
	}
	if r := collect(t, h); r["created"] != 0 || r["deleted"] != 0 {
		t.Fatalf("reconcile is not idempotent: %v", r)
	}
	// Filters that are not Drop apps' are left alone.
	_ = h.Usage.PutPrefixFilter(context.Background(), "d-zzzzzzzzzzzzzzz")
	collect(t, h)
	if _, ok := h.Usage.Filters()["d-zzzzzzzzzzzzzzz"]; !ok {
		t.Fatal("a foreign filter was removed")
	}
}

// TestReplicaSyncIntervalSetting: Crew apps default to 10s, the owner changes
// it (a new deployment), the desired document carries it.
func TestReplicaSyncIntervalSetting(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	const alice = "alice@example.test"
	h.Invite(alice, "developer")
	a := h.SignIn(alice).Token
	_, b := h.User("b@example.test", "developer")
	crew := appOf(t, h.MustOp(a, "app.create", map[string]any{"name": "Crew", "slug": "crew-app", "kind": "crew"}))
	plain := appOf(t, h.MustOp(a, "app.create", map[string]any{"name": "Plain", "slug": "plain-app"}))
	if crew["replica_sync_interval"] != "10s" || plain["replica_sync_interval"] != nil {
		t.Fatalf("defaults %v / %v", crew["replica_sync_interval"], plain["replica_sync_interval"])
	}
	id := crew["id"].(string)
	for _, bad := range []string{"0s", "301s", "1.5s", "ten", "-5s"} {
		wantError(t, h.Op(a, "app.set", map[string]any{"app": id, "replica_sync_interval": bad}), 400, "invalid_input")
	}
	wantError(t, h.Op(a, "app.set", map[string]any{"app": plain["id"], "replica_sync_interval": "10s"}), 400, "invalid_input")
	wantError(t, h.Op(b, "app.set", map[string]any{"app": id, "replica_sync_interval": "30s"}), 404, "not_found")
	r := h.MustOp(a, "app.set", map[string]any{"app": id, "replica_sync_interval": "2m"})
	if r["deployment_bumped"] != true || appOf(t, r)["replica_sync_interval"] != "120s" {
		t.Fatalf("set %v", r)
	}
	n1, _, token := h.Node("node-one", hostA)
	path := uploadLink(t, h, a, id, map[string]any{"activate": true})
	if up := upload(t, h, path, droptest.ELF(4096, 1)); up.Status != 200 {
		t.Fatal(up.Raw)
	}
	h.MustOp(h.Token(h.Owner()), "placement.set", map[string]any{"app": id, "node": n1})
	d, _ := desired(t, h, token)
	if len(d.Apps) != 1 || d.Apps[0].ReplicaSyncSeconds != 120 {
		t.Fatalf("desired %+v", d.Apps)
	}
}
