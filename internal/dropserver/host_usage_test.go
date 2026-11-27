package dropserver_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func usageOf(seq int64, dep string, write, in int64) map[string]any {
	return map[string]any{"deploymentId": dep, "seq": seq, "counters": map[string]any{
		"readRequests": 0, "writeRequests": write, "listRequests": 0, "deleteRequests": 0, "rejectedRequests": write, "budgetRefusals": write, "bytesIn": in, "bytesOut": 0}}
}

// Host services usage: taken once per sequence, differences added to the
// running total, usage alone written at most every five minutes, a node
// cannot report usage for another node's app, and owners read only their
// own apps' rows.
func TestHostUsageReports(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, tokenA := h.User("a@example.test", "user")
	_, tokenB := h.User("b@example.test", "user")
	n1, _, t1 := h.Node("node-one", hostA)
	n2, _, t2 := h.Node("node-two", hostB)
	app := deployedApp(t, h, tokenA, "app-one", 1)
	h.MustOp(owner, "placement.set", map[string]any{"app": app, "node": n1})
	appB := deployedApp(t, h, tokenB, "app-two", 2)
	h.MustOp(owner, "placement.set", map[string]any{"app": appB, "node": n2})
	nib := appRecord(t, h, app).GetString("nib_app_id")
	dep := nib + "-dep-" + appRecord(t, h, app).GetString("deployment_seq")

	var mu sync.Mutex
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	dropserver.SetAgentClock(h.App, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	send := func(token, host string, u map[string]any) int {
		t.Helper()
		r := appReport(nib, dep, "running")
		if u != nil {
			r["usage"] = u
		}
		status, _ := report(t, h, token, reportOf(host, r))
		return status
	}
	row := func() map[string]any {
		t.Helper()
		rows, err := h.App.FindAllRecords("node_reports")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.GetString("nib_app_id") == nib {
				var total dropapi.UsageCounters
				_ = r.UnmarshalJSONField("host_usage_total", &total)
				return map[string]any{"seq": r.GetInt("host_usage_seq"), "dep": r.GetString("host_usage_deployment"), "total": total}
			}
		}
		return nil
	}
	if s := send(t1, hostA, usageOf(1, dep, 3, 100)); s != 200 {
		t.Fatalf("first usage %d", s)
	}
	if r := row(); r["seq"] != 1 || r["total"].(dropapi.UsageCounters).WriteRequests != 3 {
		t.Fatalf("first %v", r)
	}
	if n := num(h.MustOp(owner, "usage.get", map[string]any{"app": app})["budget_refusals"]); n != 3 {
		t.Fatalf("operator sees %d funding denials", n)
	}
	// The same or an older sequence never counts twice; a newer one within
	// five minutes waits (the counters are cumulative).
	writes := countWrites(h)
	advance(30 * time.Second)
	send(t1, hostA, usageOf(1, dep, 3, 100))
	send(t1, hostA, usageOf(2, dep, 5, 150))
	if w := writes.take(); w["node_reports"] != 0 {
		t.Fatalf("usage within five minutes wrote %v", w)
	}
	advance(5 * time.Minute)
	send(t1, hostA, usageOf(3, dep, 9, 400))
	if r := row(); r["seq"] != 3 || r["total"].(dropapi.UsageCounters).WriteRequests != 9 || r["total"].(dropapi.UsageCounters).BytesIn != 400 {
		t.Fatalf("delta %v", r)
	}
	advance(5 * time.Minute)
	send(t1, hostA, usageOf(2, dep, 99, 9999))
	if r := row(); r["seq"] != 3 || r["total"].(dropapi.UsageCounters).WriteRequests != 9 {
		t.Fatalf("an older sequence counted %v", r)
	}
	// Counters that went down (a node that lost its file) add nothing.
	send(t1, hostA, usageOf(4, dep, 1, 1))
	if r := row(); r["seq"] != 4 || r["total"].(dropapi.UsageCounters).WriteRequests != 9 {
		t.Fatalf("reset counters %v", r)
	}
	// Invalid usage refuses the report.
	for _, bad := range []map[string]any{usageOf(0, dep, 1, 1), usageOf(5, nib+"-dep-99", 1, 1), usageOf(5, dep, -1, 1)} {
		if s := send(t1, hostA, bad); s != 400 {
			t.Fatalf("invalid usage %v answered %d", bad, s)
		}
	}
	// N2 reporting usage about A's app is ignored.
	send(t2, hostB, usageOf(50, dep, 1000, 1000))
	if r := row(); r["seq"] != 4 || r["total"].(dropapi.UsageCounters).WriteRequests != 9 {
		t.Fatalf("another node's report counted %v", r)
	}
	// Owners read their own rows only (node_reports stays off the public
	// route table; on the unfiltered router the collection rules decide).
	w := routerGet(h, "/api/collections/node_reports/records", tokenB)
	if strings.Contains(w.Body.String(), nib) || strings.Contains(w.Body.String(), "host_usage_total\":{\"readRequests\":0,\"writeRequests\":9") {
		t.Fatalf("B read A's usage: %s", w.Body)
	}
	w = routerGet(h, "/api/collections/node_reports/records", tokenA)
	if !strings.Contains(w.Body.String(), "host_usage_total") {
		t.Fatalf("A cannot read its usage: %d %s", w.Code, w.Body)
	}
}

// The desired document carries a managed Crew app's effective hard limits
// for the node's S3 proxy, and an override bumps the node's desired_seq.
func TestHostLimitsInDesired(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	const alice = "alice@example.test"
	h.Invite(alice, "user")
	a := h.SignIn(alice).Token
	crew := appOf(t, h.MustOp(a, "app.create", map[string]any{"name": "Crew", "slug": "crew-app", "kind": "crew"}))
	plain := deployedApp(t, h, a, "plain-app", 2)
	id := crew["id"].(string)
	n1, _, token := h.Node("node-one", hostA)
	path := uploadLink(t, h, a, id, map[string]any{"activate": true})
	if up := upload(t, h, path, droptest.ELF(4096, 1)); up.Status != 200 {
		t.Fatal(up.Raw)
	}
	h.MustOp(owner, "placement.set", map[string]any{"app": id, "node": n1})
	h.MustOp(owner, "placement.set", map[string]any{"app": plain, "node": n1})
	d, _ := desired(t, h, token)
	limitsOf := func(d dropapi.Desired) map[string]*dropapi.HostLimits {
		out := map[string]*dropapi.HostLimits{}
		for _, app := range d.Apps {
			out[app.AppID] = app.Limits
		}
		return out
	}
	nib := crew["nib_app_id"].(string)
	got := limitsOf(d)
	if l := got[nib]; l == nil || l.StoredBytesQuota != 10<<30 || l.MonthlyBytesOut != 20<<30 || l.RPS != 0 || l.MaxObjectBytes != 0 {
		t.Fatalf("crew limits %+v", l)
	}
	if l := got[appRecord(t, h, plain).GetString("nib_app_id")]; l != nil {
		t.Fatalf("plain app limits %+v", l)
	}
	before := seqOf(t, h, n1)
	h.MustOp(owner, "usage.set_limits", map[string]any{"app": id, "stored_bytes": 2 << 30})
	if seqOf(t, h, n1) != before+1 {
		t.Fatal("a limits override left desired_seq alone")
	}
	d, _ = desired(t, h, token)
	if l := limitsOf(d)[nib]; l.StoredBytesQuota != 20<<30 || l.MonthlyBytesOut != 20<<30 {
		t.Fatalf("override limits %+v", l)
	}
	for _, bad := range []dropapi.HostLimits{{RPS: -1}, {RPS: 1001}, {MaxObjectBytes: 1}, {StoredBytesQuota: -1}, {Burst: 1 << 20}} {
		if bad.Valid() {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
