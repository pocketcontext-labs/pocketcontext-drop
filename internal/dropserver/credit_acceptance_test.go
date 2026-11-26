package dropserver_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestCreditPurchases(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	first, token := h.User("balance@example.test", "user")
	_, other := h.User("separate@example.test", "user")
	owner := h.Token(h.Owner())
	wantError(t, h.Op(other, "credits.get", map[string]any{"account": first.Id}), 404, "not_found")
	wantError(t, h.Op(other, "credits.accounts", nil), 403, "forbidden")
	cfg := map[string]any{"pack": 500, "cap": 500, "starting": 0, "euro_micros_per_credit": 10000, "markup_ppm": 1250000, "eur_per_usd_ppm": 1000000}
	h.MustOp(owner, "credits.configure", cfg)
	var wg sync.WaitGroup
	results := make([]droptest.OpResult, 2)
	for i := range results {
		wg.Go(func() { results[i] = h.Op(token, "credits.buy", nil, fmt.Sprintf("pack-%d", i)) })
	}
	wg.Wait()
	winner := 0
	if results[1].Status == 200 {
		winner = 1
	}
	if results[winner].Status != 200 || results[1-winner].Error["code"] != "credit_cap" {
		t.Fatalf("simultaneous purchases: %+v", results)
	}
	replay := h.Op(token, "credits.buy", nil, fmt.Sprintf("pack-%d", winner))
	if !replay.Replayed || num(replay.Result["balance"]) != 500_000_000 {
		t.Fatalf("replay: %+v", replay)
	}
	view := h.MustOp(token, "credits.get", nil)
	if len(view["entries"].([]any)) != 1 || len(h.MustOp(other, "credits.get", nil)["entries"].([]any)) != 0 {
		t.Fatal("ledger isolation or replay")
	}
}

func TestSharedTrialAndPaidSuspension(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	account, token := h.User("trial@example.test", "user")
	admin := h.Token(h.Owner())
	node, _, agent := h.Node("trial-node", hostA)
	ids := []string{deployedApp(t, h, token, "trial-one", 1), deployedApp(t, h, token, "trial-two", 2)}
	for _, id := range ids {
		h.MustOp(admin, "placement.set", map[string]any{"app": id, "node": node})
	}
	now := time.Now().UTC().Truncate(time.Second)
	dropserver.SetAgentClock(h.App, func() time.Time { return now })
	send := func(phases ...string) {
		t.Helper()
		rows := []map[string]any{}
		for i, p := range phases {
			r := appRecord(t, h, ids[i])
			nib := r.GetString("nib_app_id")
			rows = append(rows, appReport(nib, nib+"-dep-"+r.GetString("deployment_seq"), p))
		}
		body := reportOf(hostA, rows...)
		body["nibrunner"].(map[string]any)["reportedAt"] = now.Format(time.RFC3339)
		if status, res := report(t, h, agent, body); status != 200 {
			t.Fatalf("report %d %v", status, res)
		}
	}
	send("running", "starting")
	now = now.Add(29 * time.Minute)
	send("running", "running")
	send("running", "running")
	view := h.MustOp(token, "credits.get", nil)
	if got := num(view["state"].(map[string]any)["trial_seconds"]); got != 3480 {
		t.Fatalf("trial after reconnect and replay: %d", got)
	}
	now = now.Add(time.Minute)
	send("sleeping", "running")
	for _, id := range ids {
		if !appRecord(t, h, id).GetBool("archived") {
			t.Fatal("expired trial did not archive", id)
		}
	}
	wantError(t, h.Op(token, "app.restore", map[string]any{"app": ids[0]}), 409, "credits_required")
	if r := h.Op(token, "credits.buy", nil, "trial-top-up"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	for _, id := range ids {
		r := appRecord(t, h, id)
		if r.GetBool("archived") || r.GetString("desired_state") == "stopped" {
			t.Fatal("top-up did not restore", id)
		}
	}
	if r := h.Op(admin, "credits.adjust", map[string]any{"account": account.Id, "microcredits": 1 - 500_000_000}, "small-balance"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	now = now.Add(time.Second)
	send("running", "sleeping")
	now = now.Add(time.Minute)
	send("running", "sleeping")
	for _, id := range ids {
		r := appRecord(t, h, id)
		if r.GetBool("archived") || r.GetString("desired_state") != "stopped" {
			t.Fatal("balance depletion did not suspend", id)
		}
	}
	if r := h.Op(token, "credits.buy", nil, "paid-top-up"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	for _, id := range ids {
		if appRecord(t, h, id).GetString("desired_state") == "stopped" {
			t.Fatal("paid app did not resume")
		}
	}
}

func TestCreditS3Collection(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{Usage: true})
	user, token := h.User("s3-billing@example.test", "user")
	id := createApp(t, h, user.Id, "s3-billing")
	nib := appRecord(t, h, id).GetString("nib_app_id")
	if r := h.Op(token, "credits.buy", nil, "pack"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	now := time.Now().UTC()
	h.Usage.PutObject(logKey(now, "charge"), []byte(gets(now, nib, 2, 0)))
	inv := droptest.UsageConfig.InventoryPrefix
	day := now.Format(time.DateOnly)
	data := inv + "data/00000000-0000-4000-8000-000000000001.csv.gz"
	manifest := fmt.Sprintf(`{"sourceBucket":"crew-replica-fixture","fileFormat":"CSV","fileSchema":"Bucket, Key, VersionId, IsLatest, IsDeleteMarker, Size, LastModifiedDate, StorageClass, IsMultipartUploaded","files":[{"key":%q,"size":1}]}`, data)
	h.Usage.PutObject(inv+day+"T01-00Z/manifest.json", []byte(manifest))
	h.Usage.PutObject(data, gz(t, fmt.Sprintf("\"b\",\"%s/db\",\"v1\",\"true\",\"false\",\"1073741824\",\"t\",\"STANDARD\",\"false\"\n", nib)))
	collect(t, h)
	collect(t, h)
	days := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	// 2 GETs cost 100 microcredits; a GiB-day costs 2,875,000 / days.
	expected := 500_000_000 - 100 - 2_875_000/days
	view := h.MustOp(token, "credits.get", nil)
	if n := num(view["state"].(map[string]any)["balance"]); n != expected {
		t.Fatalf("balance %d, want %d: %+v", n, expected, view)
	}
	if len(view["entries"].([]any)) != 3 {
		t.Fatal("duplicate or missing charges", view)
	}
}

func TestCreditHostCounterDelta(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("counter@example.test", "user")
	node, _, agent := h.Node("counter-node", hostA)
	app := deployedApp(t, h, token, "counter-app", 1)
	h.MustOp(h.Token(h.Owner()), "placement.set", map[string]any{"app": app, "node": node})
	rec := appRecord(t, h, app)
	nib := rec.GetString("nib_app_id")
	at := time.Now().UTC().Truncate(time.Second)
	dropserver.SetAgentClock(h.App, func() time.Time { return at })
	send := func(awake, billable int) {
		row := appReport(nib, nib+"-dep-"+rec.GetString("deployment_seq"), "sleeping")
		row["awakeCounter"] = map[string]any{"measuredSeconds": awake, "billableSeconds": billable}
		body := reportOf(hostA, row)
		body["nibrunner"].(map[string]any)["reportedAt"] = at.Format(time.RFC3339)
		if status, res := report(t, h, agent, body); status != 200 {
			t.Fatalf("report %d %v", status, res)
		}
	}
	send(20, 10)
	at = at.Add(time.Hour)
	send(300, 200)
	send(300, 200)
	view := h.MustOp(token, "credits.get", nil)
	if n := num(view["state"].(map[string]any)["trial_seconds"]); n != 190 {
		t.Fatalf("trial billed %d, want 190", n)
	}
	var measured, billable, sampled int
	err := h.App.DB().NewQuery("SELECT sum(measured),sum(billable),sum(sampled) FROM drop_credit_comparison").Row(&measured, &billable, &sampled)
	if err != nil || measured != 280 || billable != 190 || sampled != 0 {
		t.Fatalf("overlap: %d %d %d %v", measured, billable, sampled, err)
	}
}
