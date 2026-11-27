package dropserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// Two concurrent app reservations compete for one account wallet. Provider
// collection may lag or redeliver records, independently of node completion.
func TestStorageBudgetIsolationAndSettlement(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{Usage: true})
	user, token := h.User("budget-owner@example.test", "developer")
	_, second := h.User("separate-owner@example.test", "developer")
	admin := h.Token(h.Owner())
	node, _, agent := h.Node("budget-node", hostA)
	_, _, otherNode := h.Node("other-budget-node", hostB)
	fund := func(amount int64) {
		t.Helper()
		r := h.Op(admin, "credits.adjust", map[string]any{"account": user.Id, "microcredits": amount}, "seed")
		if r.Status != 200 {
			t.Fatal(r.Raw)
		}
	}
	fund(100)
	var inputs []dropapi.StorageBudget
	for i := range 2 {
		slug := fmt.Sprintf("budget-%d", i)
		app := appOf(t, h.MustOp(token, "app.create", map[string]any{"name": slug, "slug": slug, "kind": "crew"}))
		id := app["id"].(string)
		if r := upload(t, h, uploadLink(t, h, token, id, map[string]any{"activate": true}), droptest.ELF(4096, byte(i+1))); r.Status != 200 {
			t.Fatal(r.Raw)
		}
		h.MustOp(admin, "placement.set", map[string]any{"app": id, "node": node})
		a := appRecord(t, h, id)
		inputs = append(inputs, dropapi.StorageBudget{Action: "reserve", Token: fmt.Sprintf("%032x", i), App: a.GetString("nib_app_id"), Deployment: a.GetString("nib_app_id") + "-dep-" + a.GetString("deployment_seq"), Class: "read", Bytes: 4096})
	}
	call := func(auth string, in dropapi.StorageBudget) int {
		in.Schema = dropapi.StorageBudgetSchema
		body, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", "https://"+droptest.Domain+dropapi.AgentReportRoute, strings.NewReader(string(body)))
		req.TLS = nil
		req.Host = droptest.Domain
		req.Header.Set("Content-Type", dropapi.StorageBudgetMedia)
		req.Header.Set("Authorization", auth)
		rec := httptest.NewRecorder()
		h.Server.Public().ServeHTTP(rec, req)
		return rec.Code
	}
	for _, auth := range []string{token, second, admin} {
		if n := call(auth, inputs[0]); n != 403 {
			t.Fatalf("account authorized %d", n)
		}
	}
	if n := call("", inputs[0]); n != 401 {
		t.Fatalf("anonymous status %d", n)
	}
	if n := call(otherNode, inputs[0]); n != 404 {
		t.Fatalf("foreign node status %d", n)
	}
	bad := inputs[0]
	bad.Deployment = bad.App + "-dep-99"
	if n := call(agent, bad); n != 404 {
		t.Fatalf("stale deployment status %d", n)
	}
	var wg sync.WaitGroup
	var statuses [2]int
	for i := range inputs {
		wg.Go(func() { statuses[i] = call(agent, inputs[i]) })
	}
	wg.Wait()
	win := 0
	if statuses[1] == 200 {
		win = 1
	}
	if statuses[win] != 200 || statuses[1-win] != 403 {
		t.Fatalf("concurrent reservations %v", statuses)
	}
	in := inputs[win]
	if n := call(agent, in); n != 409 {
		t.Fatalf("reserve replay allowed %d", n)
	}
	if r := h.Op(admin, "credits.adjust", map[string]any{"account": user.Id, "microcredits": -100}, "competing-debit"); r.Status == 200 {
		t.Fatal("admin adjustment spent encumbered funds")
	}
	in.Action = "observed"
	in.Bytes = 0
	for range 2 {
		if n := call(agent, in); n != http.StatusOK {
			t.Fatalf("observe returned %d", n)
		}
	}
	state := func() (int, int) {
		v := h.MustOp(token, "credits.get", nil)["state"].(map[string]any)
		return num(v["balance"]), num(v["reserved"])
	}
	if bal, res := state(); bal != 50 || res != 0 {
		t.Fatalf("wallet after observation %d, held %d", bal, res)
	}
	at := time.Now().UTC()
	record := strings.Replace(gets(at, in.App, 1, 0), "synthetic-agent", dropapi.StorageMarker+in.Token, 1)
	for i := range 2 {
		h.Usage.PutObject(logKey(at, fmt.Sprintf("budget-%d", i)), []byte(record))
		collect(t, h)
	}
	if bal, res := state(); bal != 50 || res != 0 {
		t.Fatalf("wallet after duplicate provider records %d, held %d", bal, res)
	}
	if n := call(agent, in); n != 200 {
		t.Fatalf("late node observation %d", n)
	}
	// A committed reserve whose reply is lost never gets sent to S3. Expiry
	// must free its funds and ignore any later duplicate, even after restart.
	abandoned := inputs[1-win]
	abandoned.Bytes = 0
	if n := call(agent, abandoned); n != 200 {
		t.Fatalf("reserve remaining funds %d", n)
	}
	if _, err := h.App.DB().NewQuery("UPDATE drop_storage_reservations SET created=unixepoch()-604801 WHERE state='pending'").Execute(); err != nil {
		t.Fatal(err)
	}
	late := strings.Replace(gets(at, abandoned.App, 1, 0), "synthetic-agent", dropapi.StorageMarker+abandoned.Token, 1)
	h.Usage.PutObject(logKey(at, "expired"), []byte(late))
	collect(t, h)
	if bal, res := state(); bal != 50 || res != 0 {
		t.Fatalf("late unconfirmed record charged wallet %d held %d", bal, res)
	}

	held := abandoned
	held.Token = fmt.Sprintf("%032x", 9)
	held.Bytes = 0
	held.Action = "reserve"
	if n := call(agent, held); n != 200 {
		t.Fatalf("exact-wallet hold %d", n)
	}
	now := time.Now().UTC().Truncate(time.Second)
	dropserver.SetAgentClock(h.App, func() time.Time { return now })
	send := func(phase string) {
		body := reportOf(hostA, appReport(held.App, held.Deployment, phase))
		body["nibrunner"].(map[string]any)["reportedAt"] = now.Format(time.RFC3339)
		if n, reply := report(t, h, agent, body); n != 200 {
			t.Fatalf("compute report: %d %v", n, reply)
		}
	}
	send("running")
	now = now.Add(time.Minute)
	send("sleeping")
	wallet := h.MustOp(token, "credits.get", nil)["state"].(map[string]any)
	if num(wallet["trial_seconds"]) != 0 || num(wallet["pending_compute_seconds"]) != 60 || num(wallet["balance"]) != 50 {
		t.Fatalf("compute spent buffer during storage hold: %v", wallet)
	}
	held.Action = "observed"
	if n := call(agent, held); n != 200 {
		t.Fatal(n)
	}
	wallet = h.MustOp(token, "credits.get", nil)["state"].(map[string]any)
	if num(wallet["trial_seconds"]) != 60 || num(wallet["pending_compute_seconds"]) != 0 || num(wallet["balance"]) != 0 {
		t.Fatalf("deferred compute after actual depletion: %v", wallet)
	}

}
