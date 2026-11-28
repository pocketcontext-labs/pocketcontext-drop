package dropserver_test

import (
	"context"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// The archive survives deletion of its source app. Bill its original owner,
// exactly once, across the January/February boundary using actual month lengths.
func TestRetainedBackupBilling(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner, token := h.User("portable-billing@example.test", "user")
	if r := h.Op(token, "credits.buy", nil, "backup-billing-pack"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	start := time.Date(2030, 1, 31, 12, 0, 0, 0, time.UTC)
	p := dbx.Params{"o": owner.Id, "t": start.Unix(), "n": int64(1 << 30)}
	exec := func(query string) {
		t.Helper()
		if _, err := h.App.DB().NewQuery(query).Bind(p).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO drop_backups(id,owner,app,source_app,name,release_sha,multiplier,state,created) VALUES('archive',{:o},'deleted-app','source','Crew','sha',1,'pending',{:t})")
	exec("UPDATE drop_backups SET state='ready',bytes={:n} WHERE id='archive'")
	exec("UPDATE drop_backup_meter SET through={:t} WHERE backup='archive'")
	bill := func(at time.Time) {
		t.Helper()
		if err := dropserver.MaintainBackupBillingForTest(context.Background(), h.App, at); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE drop_credit_settings SET markup_ppm=2500000 WHERE id=1")
	listed := h.MustOp(token, "backup.list", nil)
	quote := listed["pricing"].(map[string]any)["microcredits_per_gib_month"]
	pinned := listed["backups"].([]any)[0].(map[string]any)["microcredits_per_gib_month"]
	if num(quote) != 5750000 || num(pinned) != 2875000 {
		t.Fatal("backup storage rate was not pinned", quote, pinned)
	}
	bill(start.Add(23 * time.Hour))
	end := start.Add(24 * time.Hour)
	bill(end)
	bill(end)
	view := h.MustOp(token, "credits.get", map[string]any{"kind": "backup_storage"})
	entries := view["entries"].([]any)
	// 2,875,000 microcredits/GiB-month, split evenly between 31 and 28 days.
	if len(entries) != 1 || num(entries[0].(map[string]any)["delta"]) != -97710 {
		t.Fatal("unexpected cross-month debit", entries)
	}
	// A low balance caps the debit. An unfunded interval advances the
	// cursor without an overdraft or retroactive charge at top-up.
	admin := h.Token(h.Owner())
	if r := h.Op(admin, "credits.adjust", map[string]any{"account": owner.Id, "target_microcredits": 3}, "backup-low-balance"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	bill(start.Add(48 * time.Hour))
	bill(start.Add(72 * time.Hour))
	view = h.MustOp(token, "credits.get", map[string]any{"kind": "backup_storage"})
	entries = view["entries"].([]any)
	if len(entries) != 2 || num(entries[0].(map[string]any)["delta"]) != -3 || num(view["state"].(map[string]any)["balance"]) != 0 {
		t.Fatal("grace billing", view)
	}
	// Simulate the existing wallet's zero-to-funded timestamp midway
	// through the next interval, then check only the funded half-day.
	topup := start.Add(84 * time.Hour)
	dropserver.SetAgentClock(h.App, func() time.Time { return topup })
	if r := h.Op(token, "credits.buy", nil, "backup-new-pack"); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	bill(start.Add(96 * time.Hour))
	view = h.MustOp(token, "credits.get", map[string]any{"kind": "backup_storage"})
	entries = view["entries"].([]any)
	if len(entries) != 3 || num(entries[0].(map[string]any)["delta"]) != -51339 {
		t.Fatal("retroactive grace charge", entries)
	}
	bill(start.Add(30 * 24 * time.Hour))
	var through int64
	if err := h.App.DB().NewQuery("SELECT through FROM drop_backup_meter WHERE backup='archive'").Row(&through); err != nil || through != start.Add(7*24*time.Hour).Unix() {
		t.Fatal("charged beyond expiry", through, err)
	}
}
