package dropserver_test

import (
	"bufio"
	"encoding/json"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
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

// The isolation suite (plan section 9, M10): users A and B, the owner O (an
// admin), nodes N1 and N2. Every row is a test that fails if one party
// reaches another's data. Where a row lives outside this file, the test is
// named.
//
//	Surface      What is checked                                              Test
//	-----------  -----------------------------------------------------------  ------------------------------------------
//	Records API  users never listed or viewed, by anyone (admins use ops)      TestUserAndInviteIsolation
//	             invites: admins only; events: own apps (admins all)          TestUserAndInviteIsolation
//	             apps: own (admins all); app_secret_names not public           TestAppAndSecretIsolation
//	             releases, placements, nodes, node_reports, upload_links,     TestIsolationMatrix
//	             users not on the public route table (404 for every token)
//	             rules alone (unfiltered router): A sees none of B's rows;     TestIsolationMatrix, TestNodeIsolation
//	             a node sees only its own nodes record and placements
//	             record POST/PATCH/DELETE refused for every token              TestUserAndInviteIsolation, TestIsolationMatrix
//	Operations   B's app by id or slug is 404 for A on every app, secret,     TestAppAndSecretIsolation, TestIsolationMatrix
//	             release, status and upload-link op; admins read, never write
//	             user, invite, node and placement ops: 403 for users          TestUserAndInviteIsolation, TestIsolationMatrix
//	             node tokens on /api/drop/op and /api/drop/me: 403            TestNodeIsolation
//	             deletion receipts scoped by account and immutable app ID     TestDeletionReceiptAfterNodeConfirmationAndRetry
//	Beta         admission and probe completion require admin, uploads and   TestBetaProbeLeasesAndDeployment
//	             deployment scoped to approved bytes and app owner; legacy APIs blocked
//	Catalog      Only a developer can submit their own currently deployed     TestPrivateDeveloperRelease
//	             private release; only an admin can review and accept the
//	             exact digest; users see it only after rehearsal/publication
//	Upload       a link is scoped to one app by its token; A cannot mint one  TestIsolationMatrix, TestUploadLink* (releases_test.go)
//	             for B's app; per-address limiter                             TestUploadRateLimit (releases_test.go)
//	Agent routes anonymous 401, users' tokens (owner included) 403            TestNodeIsolation
//	             N1's desired document holds only N1's apps; reports about    TestNodeIsolation
//	             another node's app are ignored; another host is 409
//	             Crew mailbox snapshot and node isolation through a move       TestCrewSignInProvisioning
//	             per-address node sign-in limiter, per-node route limiter     TestNodeSignInRateLimit, TestAgentRouteRateLimit (hardening_test.go)
//	             a restore invalidates node tokens                            TestHostedRestoreInvalidatesNodeTokens (hardening_test.go)
//	             host services usage (node_reports.host_usage*): N2's usage   TestHostUsageReports (host_usage_test.go)
//	             about N1's app ignored; owners read only their apps' rows
//	Host         the node's S3 proxy socket: only nibrunnerd's uid; only a    TestHostServicesEndToEnd,
//	services     current deployment; only its app's prefix, bucket and        TestHostServicesRefuseOtherPeers (internal/agent),
//	             operations; the STS session never leaves the agent           TestParseRequestAllowsOnlyTheAppsOperations (internal/hostservices)
//	Realtime     A's subscriptions never deliver B's records; N1's never     TestRealtimeIsolation
//	             deliver N2's record or placements, or any app, release,
//	             event or report; anonymous subscriptions deliver nothing
//	MCP          drop_read/drop_write: B's app 404 for A (apps, releases,     TestMCPIsolation (internal/client/drop_mcp_test.go)
//	             secrets, status, events, upload links, deploys); admin ops
//	             refused for users; node tokens are not OAuth grants
//	             tools/list per role                                          TestMCPToolsByRole, TestMCPAdminOpAsUserForbidden
//	Usage        app_usage_hourly, app_usage_daily: own apps (admins all),    TestIsolationMatrix, TestRealtimeIsolation
//	             not public routes; usage.get B's app 404 for A;
//	             usage.set_limits and usage.collect 403 for users;           TestIsolationMatrix
//	             apps.usage_limits and replica_sync_interval only via ops     TestUsageCollectEndToEnd, TestReplicaSyncIntervalSetting
//	             (usage_test.go); events app.usage_exceeded and
//	             app.metrics_filter follow the events rules; drop_read
//	             what=usage                                                   TestMCPIsolation
//	Secrets      a value reaches only the store: never records, answers,      TestSecretSentinel, TestMCPSecretSentinel,
//	             logs, events, reports or desired documents                   TestDesiredDocument, internal/leakscan
//	Browser      #/apps/<not yours> and #/admin/users as a non-admin show      TestTenantReader (tests, CI)
//	             Not found

func listCount(t *testing.T, h *droptest.Hosted, path, token string) int {
	t.Helper()
	w := h.Request("GET", path, token, "")
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

func TestUserAndInviteIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	a, aToken := h.User("a@example.test", "developer")
	b, bToken := h.User("b@example.test", "developer")
	invite := h.MustOp(owner, "invite.create", map[string]any{"email": "c@example.test"})["invite"].(map[string]any)["id"].(string)
	h.MustOp(owner, "user.set", map[string]any{"user": b.Id, "app_quota": 4})
	// An app of A and an event about it, written as an operation would.
	appA := createApp(t, h, a.Id, "app-of-a")
	if err := dropserver.WriteEvent(h.App, dropserver.EventRecord{Actor: a.Id, App: appA, Action: "app.set", Detail: map[string]any{"changed": []string{"name"}}}); err != nil {
		t.Fatal(err)
	}

	// Accounts: users records are never public; a user's user.list and
	// user.set are refused.
	for _, token := range []string{aToken, owner} {
		for _, path := range []string{"/api/collections/users/records", "/api/collections/users/records/" + b.Id} {
			if w := h.Request("GET", path, token, ""); w.Code != 404 {
				t.Errorf("GET %s: %d", path, w.Code)
			}
		}
	}
	wantError(t, h.Op(aToken, "user.list", nil), 403, "forbidden")
	wantError(t, h.Op(aToken, "user.set", map[string]any{"user": b.Id, "app_quota": 50}), 403, "forbidden")
	wantError(t, h.Op(aToken, "user.set", map[string]any{"user": a.Id, "role": "admin"}), 403, "forbidden")

	// Invites: only admins see them, through the records API and the operations.
	if n := listCount(t, h, "/api/collections/invites/records", aToken); n != 0 {
		t.Errorf("a user lists %d invites", n)
	}
	if w := h.Request("GET", "/api/collections/invites/records/"+invite, aToken, ""); w.Code != 404 {
		t.Errorf("a user views an invite: %d", w.Code)
	}
	if n := listCount(t, h, "/api/collections/invites/records", ""); n != 0 {
		t.Errorf("anonymous lists %d invites", n)
	}
	if n := listCount(t, h, "/api/collections/invites/records", owner); n != 3 { // A, B and C
		t.Errorf("the admin lists %d invites", n)
	}
	wantError(t, h.Op(aToken, "invite.list", nil), 403, "forbidden")
	wantError(t, h.Op(aToken, "invite.create", map[string]any{"email": "friend@example.test"}), 403, "forbidden")
	wantError(t, h.Op(aToken, "invite.revoke", map[string]any{"invite": invite}), 403, "forbidden")

	// Events: an account sees those of its own apps, admins all of them.
	if n := listCount(t, h, "/api/collections/events/records", aToken); n != 2 {
		t.Errorf("A lists %d events, want the two of its app", n)
	}
	if n := listCount(t, h, "/api/collections/events/records", bToken); n != 0 {
		t.Errorf("B lists %d events", n)
	}
	if n := listCount(t, h, "/api/collections/events/records", ""); n != 0 {
		t.Errorf("anonymous lists %d events", n)
	}
	if n := listCount(t, h, "/api/collections/events/records", owner); n < 3 {
		t.Errorf("the admin lists %d events", n)
	}

	// Record writes are refused for everyone, admins included.
	for _, token := range []string{aToken, owner} {
		for _, c := range []struct{ method, path, body string }{
			{"POST", "/api/collections/invites/records", `{"email":"x@example.test"}`},
			{"PATCH", "/api/collections/invites/records/" + invite, `{"revoked_at":""}`},
			{"DELETE", "/api/collections/invites/records/" + invite, ""},
			{"POST", "/api/collections/events/records", `{"action":"x"}`},
			{"PATCH", "/api/collections/users/records/" + a.Id, `{"role":"admin"}`},
		} {
			if w := h.Request(c.method, c.path, token, c.body); w.Code != 404 {
				t.Errorf("%s %s: %d", c.method, c.path, w.Code)
			}
		}
	}
	if fresh, _ := h.App.FindRecordById("users", a.Id); fresh.GetString("role") != "developer" {
		t.Fatal("A changed its own role")
	}
}

// createApp creates an app owned by an account through app.create.
func createApp(t *testing.T, h *droptest.Hosted, owner, slug string) string {
	t.Helper()
	user, err := h.App.FindRecordById("users", owner)
	if err != nil {
		t.Fatal(err)
	}
	return h.MustOp(h.Token(user), "app.create", map[string]any{"name": slug, "slug": slug})["app"].(map[string]any)["id"].(string)
}

// TestAppAndSecretIsolation (plan section 9): users A and B and the owner O,
// an admin. Each account reads and changes only its own apps and secrets;
// another account's app is a 404 by id and by slug; admins read every app but
// change only their own.
func TestAppAndSecretIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, a := h.User("a@example.test", "developer")
	_, b := h.User("b@example.test", "developer")
	owner := h.Token(h.Owner())
	appA := appOf(t, h.MustOp(a, "app.create", map[string]any{"name": "A", "slug": "app-of-a"}))["id"].(string)
	h.MustOp(b, "app.create", map[string]any{"name": "B", "slug": "app-of-b"})
	h.MustOp(a, "secret.set", map[string]any{"app": appA, "name": "TOKEN", "value": "synthetic"})

	// Records API: own apps only; admins all; anonymous none; secret names are
	// not a public route.
	for token, want := range map[string]int{a: 1, b: 1, "": 0, owner: 2} {
		if n := listCount(t, h, "/api/collections/apps/records", token); n != want {
			t.Errorf("lists %d apps, want %d", n, want)
		}
	}
	if w := h.Request("GET", "/api/collections/apps/records/"+appA, b, ""); w.Code != 404 {
		t.Errorf("B views A's app: %d", w.Code)
	}
	for _, token := range []string{a, owner} {
		if w := h.Request("GET", "/api/collections/app_secret_names/records", token, ""); w.Code != 404 {
			t.Errorf("secret names are public: %d", w.Code)
		}
	}
	if n := listCount(t, h, "/api/collections/events/records", b); n != 1 {
		t.Errorf("B lists %d events, want its own app.create", n)
	}

	// Operations on another account's app: 404, by id and by slug.
	for _, ref := range []string{appA, "app-of-a"} {
		for op, input := range map[string]map[string]any{
			"app.get": {}, "app.set": {"name": "Hijacked"}, "app.start": {}, "app.stop": {}, "app.archive": {}, "app.restore": {},
			"secret.list": {}, "secret.set": {"name": "TOKEN", "value": "x"}, "secret.delete": {"name": "TOKEN"},
		} {
			input["app"] = ref
			wantError(t, h.Op(b, op, input), 404, "not_found")
		}
	}
	list := h.MustOp(b, "app.list", nil)["apps"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["slug"] != "app-of-b" {
		t.Fatalf("B lists %v", list)
	}
	wantError(t, h.Op(b, "app.list", map[string]any{"all": true}), 403, "forbidden")

	// Admins read every app and its secret names, and change only their own.
	all := h.MustOp(owner, "app.list", map[string]any{"all": true})["apps"].([]any)
	if len(all) != 2 {
		t.Fatalf("admin lists %v", all)
	}
	emails := map[any]bool{}
	for _, app := range all {
		emails[app.(map[string]any)["owner_email"]] = true
	}
	if !emails["a@example.test"] || !emails["b@example.test"] {
		t.Fatalf("owner emails %v", emails)
	}
	if got := appOf(t, h.MustOp(owner, "app.get", map[string]any{"app": "app-of-a"})); got["id"] != appA {
		t.Fatal("admin cannot read A's app")
	}
	if s := h.MustOp(owner, "secret.list", map[string]any{"app": appA})["secrets"].([]any); len(s) != 1 {
		t.Fatalf("admin secret names %v", s)
	}
	wantError(t, h.Op(owner, "app.set", map[string]any{"app": appA, "name": "Admin was here"}), 403, "forbidden")
	wantError(t, h.Op(owner, "secret.set", map[string]any{"app": appA, "name": "TOKEN", "value": "x"}), 403, "forbidden")
	if fresh, _ := h.App.FindRecordById("apps", appA); fresh.GetString("name") != "A" {
		t.Fatal("A's app changed")
	}
}

// TestNodeIsolation: a node's token reaches only the agent routes, its own
// nodes record and its own placements; users' tokens never reach the agent
// routes; a node sees only its own apps.
func TestNodeIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	userA, tokenA := h.User("a@example.test", "developer")
	n1, _, t1 := h.Node("node-one", hostA)
	n2, _, t2 := h.Node("node-two", hostB)
	app1 := deployedApp(t, h, tokenA, "app-one", 1)
	app2 := deployedApp(t, h, tokenA, "app-two", 2)
	h.MustOp(owner, "placement.set", map[string]any{"app": app1, "node": "node-one"})
	h.MustOp(owner, "placement.set", map[string]any{"app": app2, "node": n2})

	// Node tokens on the operation route and the account route: 403.
	for _, token := range []string{t1, t2} {
		wantError(t, h.Op(token, "me", nil), 403, dropapi.CodeForbidden)
		wantError(t, h.Op(token, "app.list", nil), 403, dropapi.CodeForbidden)
		if w := h.Request("GET", "/api/drop/me", token, ""); w.Code != 403 {
			t.Fatalf("node on /api/drop/me: %d", w.Code)
		}
		// Public record reads: the rules admit no node.
		for _, c := range []string{"apps", "invites", "events"} {
			if n := listCount(t, h, "/api/collections/"+c+"/records", token); n != 0 {
				t.Fatalf("node lists %d %s", n, c)
			}
		}
		if w := h.Request("GET", "/api/collections/apps/records/"+app1, token, ""); w.Code != 404 {
			t.Fatalf("node views an app: %d", w.Code)
		}
		// No record write and no users records.
		if w := h.Request("POST", "/api/collections/nodes/records", token, `{}`); w.Code != 404 {
			t.Fatalf("node record write: %d", w.Code)
		}
		if w := h.Request("GET", "/api/collections/users/records", token, ""); w.Code != 404 {
			t.Fatalf("users records: %d", w.Code)
		}
	}
	// On the unfiltered router the rules alone decide: a node sees its own
	// record and placements, never another node's or other collections.
	for path, want := range map[string]int{
		"/api/collections/nodes/records/" + n1:       200,
		"/api/collections/nodes/records/" + n2:       404,
		"/api/collections/apps/records/" + app1:      404,
		"/api/collections/node_reports/records":      200, // empty list
		"/api/collections/releases/records":          200, // empty list
		"/api/collections/app_secret_names/records":  200, // empty list
		"/api/collections/upload_links/records":      403,
		"/api/collections/users/records/" + userA.Id: 404,
	} {
		if w := routerGet(h, path, t1); w.Code != want {
			t.Errorf("node GET %s: %d, want %d", path, w.Code, want)
		} else if want == 200 && strings.HasSuffix(path, "/records") && !strings.Contains(w.Body.String(), `"items":[]`) {
			t.Errorf("node lists %s: %s", path, w.Body)
		}
	}
	if w := routerGet(h, "/api/collections/placements/records", t1); w.Code != 200 || strings.Count(w.Body.String(), `"collectionName":"placements"`) != 1 || !strings.Contains(w.Body.String(), n1) || strings.Contains(w.Body.String(), n2) {
		t.Fatalf("node one's placements: %s", w.Body)
	}
	for _, c := range []string{"nodes", "placements"} {
		if w := routerGet(h, "/api/collections/"+c+"/records", tokenA); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
			t.Fatalf("a user lists %s: %s", c, w.Body)
		}
	}

	// Agent routes: anonymous 401, users' tokens 403 (owner included).
	for _, token := range []string{"", tokenA, owner} {
		want := 403
		if token == "" {
			want = 401
		}
		if w := h.Request("GET", dropapi.AgentDesiredRoute, token, ""); w.Code != want {
			t.Errorf("desired with a token %v: %d, want %d", token != "", w.Code, want)
		}
		body, _ := json.Marshal(reportOf(hostA))
		if w := h.Request("POST", dropapi.AgentReportRoute, token, string(body)); w.Code != want {
			t.Errorf("report: %d, want %d", w.Code, want)
		}
	}

	// Each node gets only its own apps.
	nib1, nib2 := appRecord(t, h, app1).GetString("nib_app_id"), appRecord(t, h, app2).GetString("nib_app_id")
	d1, _ := desired(t, h, t1)
	d2, _ := desired(t, h, t2)
	if len(d1.Apps) != 1 || d1.Apps[0].AppID != nib1 || d1.HostID != hostA || d1.NodeID != n1 {
		t.Fatalf("node one's document %+v", d1)
	}
	if len(d2.Apps) != 1 || d2.Apps[0].AppID != nib2 {
		t.Fatalf("node two's document %+v", d2)
	}
	// A report about another node's app is ignored and stored nowhere.
	dep2 := appRecord(t, h, app2).GetString("nib_app_id") + "-dep-2"
	status, out := report(t, h, t1, reportOf(hostA, appReport(nib2, dep2, "running")))
	if status != 200 || num(out["accepted"]) != 0 || len(out["ignored"].([]any)) != 1 {
		t.Fatalf("foreign report: %d %v", status, out)
	}
	if rows, _ := h.App.FindAllRecords("node_reports"); len(rows) != 0 {
		t.Fatalf("%d node reports stored", len(rows))
	}
	// A report for another host is refused.
	if status, out := report(t, h, t1, reportOf(hostB)); status != 409 || out["code"] != dropapi.CodeHostMismatch {
		t.Fatalf("host mismatch: %d %v", status, out)
	}
}

// TestIsolationMatrix fills the rows the other isolation tests leave: the
// collections that are not on the public route table, rules alone on the
// unfiltered router for every collection, releases, status and upload links
// on another account's app, and node and placement operations for users.
func TestIsolationMatrix(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	userA, a := h.User("a@example.test", "developer")
	userB, b := h.User("b@example.test", "developer")
	n1, _, t1 := h.Node("node-one", hostA)
	n2, _, t2 := h.Node("node-two", hostB)
	appA := deployedApp(t, h, a, "app-of-a", 1)
	appB := deployedApp(t, h, b, "app-of-b", 2)
	h.MustOp(a, "secret.set", map[string]any{"app": appA, "name": "TOKEN", "value": "synthetic-a"})
	h.MustOp(b, "secret.set", map[string]any{"app": appB, "name": "TOKEN", "value": "synthetic-b"})
	h.MustOp(owner, "placement.set", map[string]any{"app": appA, "node": n1})
	h.MustOp(owner, "placement.set", map[string]any{"app": appB, "node": n2})
	nibA, nibB := appRecord(t, h, appA).GetString("nib_app_id"), appRecord(t, h, appB).GetString("nib_app_id")
	depA := nibA + "-dep-" + appRecord(t, h, appA).GetString("deployment_seq")
	depB := nibB + "-dep-" + appRecord(t, h, appB).GetString("deployment_seq")
	if status, out := report(t, h, t1, reportOf(hostA, appReport(nibA, depA, "running"))); status != 200 || num(out["accepted"]) != 1 {
		t.Fatalf("N1 report: %d %v", status, out)
	}
	if status, out := report(t, h, t2, reportOf(hostB, appReport(nibB, depB, "failed"))); status != 200 || num(out["accepted"]) != 1 {
		t.Fatalf("N2 report: %d %v", status, out)
	}
	releaseB := appRecord(t, h, appB).GetString("current_release")
	tokens := map[string]string{"anonymous": "", "A": a, "B": b, "O": owner, "N1": t1, "N2": t2}

	// The public route table: these collections are never public, whoever asks.
	usageRows(t, h, appA)
	usageRows(t, h, appB)
	for _, c := range []string{"users", "releases", "app_secret_names", "upload_links", "nodes", "placements", "node_reports", "app_usage_hourly", "app_usage_daily"} {
		for who, token := range tokens {
			for _, path := range []string{"/api/collections/" + c + "/records", "/api/collections/" + c + "/records/" + appA} {
				if w := h.Request("GET", path, token, ""); w.Code != 404 {
					t.Errorf("%s GET %s: %d", who, path, w.Code)
				}
			}
		}
	}
	// Record writes: refused for every token on every collection.
	for _, c := range []string{"users", "invites", "apps", "releases", "app_secret_names", "upload_links", "nodes", "placements", "node_reports", "events", "app_usage_hourly", "app_usage_daily"} {
		for who, token := range tokens {
			for _, m := range []struct{ method, path string }{
				{"POST", "/api/collections/" + c + "/records"},
				{"PATCH", "/api/collections/" + c + "/records/" + appA},
				{"DELETE", "/api/collections/" + c + "/records/" + appA},
			} {
				if w := h.Request(m.method, m.path, token, `{"name":"x"}`); w.Code != 404 {
					t.Errorf("%s %s %s: %d", who, m.method, m.path, w.Code)
				}
			}
		}
	}

	// The rules alone (the unfiltered router, as realtime uses them): what
	// each party lists in each collection, by owner of the rows.
	// An app's own node reports and events carry the opaque id of the node
	// that runs it (node_reports.node, events.actor_node); the node's record,
	// name and other apps stay unreadable. So node ids are checked only where
	// no row of the reader's own apps can carry them.
	ownerOf := map[string]string{appA: "A", appB: "B", nibA: "A", nibB: "B", releaseB: "B", n1: "N1", n2: "N2", userA.Id: "A", userB.Id: "B"}
	type want struct{ sees, never []string }
	matrix := map[string]map[string]want{
		"A": {
			"apps": {[]string{"A"}, []string{"B"}}, "releases": {[]string{"A"}, []string{"B"}},
			"app_secret_names": {[]string{"A"}, []string{"B"}}, "node_reports": {[]string{"A"}, []string{"B", "N2"}},
			"events": {[]string{"A"}, []string{"B", "N2"}}, "nodes": {nil, []string{"N1", "N2"}},
			"placements": {nil, []string{"A", "B", "N1", "N2"}}, "invites": {nil, []string{"A", "B"}},
			"app_usage_hourly": {[]string{"A"}, []string{"B"}}, "app_usage_daily": {[]string{"A"}, []string{"B"}},
		},
		"B": {
			"apps": {[]string{"B"}, []string{"A"}}, "releases": {[]string{"B"}, []string{"A"}},
			"node_reports": {[]string{"B"}, []string{"A", "N1"}}, "events": {[]string{"B"}, []string{"A", "N1"}},
			"app_usage_hourly": {[]string{"B"}, []string{"A"}}, "app_usage_daily": {[]string{"B"}, []string{"A"}},
		},
		"N1": {
			"nodes":      {nil, []string{"N1", "N2"}}, // the list is admins' only; N1 views its own record by id
			"placements": {[]string{"A", "N1"}, []string{"B", "N2"}},
			"apps":       {nil, []string{"A", "B"}}, "releases": {nil, []string{"A", "B"}}, "app_secret_names": {nil, []string{"A", "B"}},
			"node_reports": {nil, []string{"A", "B"}}, "events": {nil, []string{"A", "B"}}, "invites": {nil, []string{"A", "B"}},
			"app_usage_hourly": {nil, []string{"A", "B"}}, "app_usage_daily": {nil, []string{"A", "B"}},
		},
		"anonymous": {
			"apps": {nil, []string{"A", "B"}}, "events": {nil, []string{"A", "B"}}, "nodes": {nil, []string{"N1", "N2"}},
			"node_reports": {nil, []string{"A", "B"}}, "placements": {nil, []string{"A", "B"}},
			"app_usage_hourly": {nil, []string{"A", "B"}}, "app_usage_daily": {nil, []string{"A", "B"}},
		},
		"O": {
			"apps": {[]string{"A", "B"}, nil}, "nodes": {[]string{"N1", "N2"}, nil}, "placements": {[]string{"A", "B"}, nil},
			"node_reports": {[]string{"A", "B"}, nil}, "events": {[]string{"A", "B"}, nil},
			"app_usage_hourly": {[]string{"A", "B"}, nil}, "app_usage_daily": {[]string{"A", "B"}, nil},
		},
	}
	for who, collections := range matrix {
		for c, w := range collections {
			r := routerGet(h, "/api/collections/"+c+"/records?perPage=500", tokens[who])
			if r.Code != 200 {
				t.Errorf("%s lists %s: %d", who, c, r.Code)
				continue
			}
			found := map[string]bool{}
			for id, party := range ownerOf {
				if strings.Contains(r.Body.String(), id) {
					found[party] = true
				}
			}
			for _, p := range w.sees {
				if !found[p] {
					t.Errorf("%s does not see %s's rows in %s", who, p, c)
				}
			}
			for _, p := range w.never {
				if found[p] {
					t.Errorf("%s sees %s's rows in %s: %s", who, p, c, r.Body)
				}
			}
		}
	}
	// Views by id follow the same rules.
	for _, v := range []struct {
		who, path string
		code      int
	}{
		{"A", "/api/collections/apps/records/" + appB, 404},
		{"A", "/api/collections/releases/records/" + releaseB, 404},
		{"A", "/api/collections/nodes/records/" + n1, 404},
		{"N1", "/api/collections/nodes/records/" + n2, 404},
		{"N1", "/api/collections/nodes/records/" + n1, 200},
		{"N1", "/api/collections/apps/records/" + appA, 404},
		{"N2", "/api/collections/releases/records/" + releaseB, 404},
		{"O", "/api/collections/apps/records/" + appA, 200},
		{"B", "/api/collections/releases/records/" + releaseB, 200},
	} {
		if r := routerGet(h, v.path, tokens[v.who]); r.Code != v.code {
			t.Errorf("%s GET %s: %d, want %d", v.who, v.path, r.Code, v.code)
		}
	}

	// Operations on B's app: 404 for A, by id and by slug, for every app op.
	for _, ref := range []string{appB, "app-of-b"} {
		for op, input := range map[string]map[string]any{
			"release.list": {}, "release.upload_link": {}, "release.deploy": {"release": releaseB}, "status": {},
			"event.list": {}, "app.get": {}, "secret.list": {}, "usage.get": {},
		} {
			input["app"] = ref
			wantError(t, h.Op(a, op, input), 404, "not_found")
		}
	}
	// A deploys B's release on A's own app: the release is not found.
	wantError(t, h.Op(a, "release.deploy", map[string]any{"app": appA, "release": releaseB}), 404, "not_found")
	// Node and placement operations are admin only.
	for op, input := range map[string]map[string]any{
		"node.list": nil, "node.create": {"name": "mine", "host_id": "h-1"}, "node.rotate_secret": {"node": n1},
		"node.set_state": {"node": n1, "state": "disabled"}, "placement.set": {"app": appA, "node": n2},
		"placement.clear": {"app": appA}, "user.list": nil, "invite.list": nil,
		"usage.set_limits": {"app": appA, "requests_a": 1}, "usage.collect": nil,
	} {
		wantError(t, h.Op(a, op, input), 403, "forbidden")
	}
	// Status names the node for admins only.
	if s := h.MustOp(a, "status", map[string]any{"app": appA}); s["node"] != nil || s["phase"] != "running" {
		t.Errorf("A's status %v", s)
	}
	if s := h.MustOp(owner, "status", map[string]any{"app": appB}); s["node"] != "node-two" || s["phase"] != "failed" {
		t.Errorf("admin status %v", s)
	}
	// Nothing changed hands.
	if r, _ := h.App.FindFirstRecordByData("placements", "app", appA); r.GetString("node") != n1 {
		t.Error("A's placement moved")
	}
	if seqOf(t, h, n1) < 1 || appRecord(t, h, appB).GetString("current_release") != releaseB {
		t.Error("B's app changed")
	}
}

// usageRows writes one hourly and one daily usage row of an app, as the
// usage collector does.
func usageRows(t *testing.T, h *droptest.Hosted, app string) {
	t.Helper()
	for name, fields := range map[string]map[string]any{
		"app_usage_hourly": {"hour": "2026-11-25T14:00:00Z", "source": "s3log", "class_b": 3},
		"app_usage_daily":  {"day": "2026-11-25", "current_bytes": 10},
	} {
		c, err := h.App.FindCachedCollectionByNameOrId(name)
		if err != nil {
			t.Fatal(err)
		}
		r := core.NewRecord(c)
		r.Set("app", app)
		for k, v := range fields {
			r.Set(k, v)
		}
		if err := h.App.Save(r); err != nil {
			t.Fatal(err)
		}
	}
}

// sseEvent is one realtime message: its topic and data.
type sseEvent struct{ Name, Data string }

// realtimeClient is one realtime connection through the public handler.
type realtimeClient struct {
	id     string
	resp   *http.Response
	mu     sync.Mutex
	events []sseEvent
	got    chan struct{}
}

func openRealtime(t *testing.T, base string) *realtimeClient {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/realtime", nil)
	req.Host = droptest.Domain
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("realtime stream: %v %v", err, resp)
	}
	c := &realtimeClient{resp: resp, got: make(chan struct{}, 1024)}
	connected := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		var name, data string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			case line == "":
				if name == "PB_CONNECT" {
					var hello struct {
						ClientID string `json:"clientId"`
					}
					_ = json.Unmarshal([]byte(data), &hello)
					connected <- hello.ClientID
				} else if name != "" {
					c.mu.Lock()
					c.events = append(c.events, sseEvent{name, data})
					c.mu.Unlock()
					c.got <- struct{}{}
				}
				name, data = "", ""
			}
		}
	}()
	select {
	case c.id = <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("no PB_CONNECT")
	}
	t.Cleanup(func() { resp.Body.Close() })
	return c
}

func (c *realtimeClient) subscribe(t *testing.T, base, token string, topics ...string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"clientId": c.id, "subscriptions": topics})
	req, _ := http.NewRequest("POST", base+"/api/realtime", strings.NewReader(string(body)))
	req.Host = droptest.Domain
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("subscribe %v: %v %v", topics, err, resp)
	}
	resp.Body.Close()
}

// waitFor waits until an event whose topic and data contain both strings
// arrives.
func (c *realtimeClient) waitFor(t *testing.T, topic, contains string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		for _, e := range c.events {
			if e.Name == topic && strings.Contains(e.Data, contains) {
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()
		select {
		case <-c.got:
		case <-deadline:
			t.Fatalf("no %s event with %s; got %v", topic, contains, c.snapshot())
		}
	}
}

func (c *realtimeClient) snapshot() []sseEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sseEvent(nil), c.events...)
}

// TestRealtimeIsolation: realtime subscriptions use the collection rules, so
// A never receives B's records (even subscribed to B's app by id), a node
// never receives another node's record or placements or any app data, and an
// anonymous subscription receives nothing.
func TestRealtimeIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = droptest.Domain
		h.Server.Public().ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close) // after the streams close (cleanups run last first)
	base := server.URL
	owner := h.Token(h.Owner())
	userB, b := h.User("b@example.test", "developer")
	_, a := h.User("a@example.test", "developer")
	n1, _, t1 := h.Node("node-one", hostA)
	n2, _, t2 := h.Node("node-two", hostB)
	everything := []string{"apps", "releases", "app_secret_names", "node_reports", "events", "nodes", "placements", "invites", "users", "app_usage_hourly", "app_usage_daily"}

	admin := openRealtime(t, base)
	admin.subscribe(t, base, owner, "apps")
	node1 := openRealtime(t, base)
	node1.subscribe(t, base, t1, append([]string{"nodes/" + n1, "nodes/" + n2}, everything...)...)
	anonymous := openRealtime(t, base)
	anonymous.subscribe(t, base, "", everything...)

	// B's activity: an app, a release, a secret, a placement on N2, a report.
	appB := deployedApp(t, h, b, "app-of-b", 2)
	clientA := openRealtime(t, base)
	clientA.subscribe(t, base, a, append([]string{"apps/" + appB, "users/" + userB.Id}, everything...)...)
	h.MustOp(b, "app.set", map[string]any{"app": appB, "name": "B renamed"})
	h.MustOp(b, "secret.set", map[string]any{"app": appB, "name": "TOKEN", "value": "synthetic-b"})
	path := uploadLink(t, h, b, appB, map[string]any{"activate": true})
	if u := upload(t, h, path, droptest.ELF(4096, 7)); u.Status != 200 {
		t.Fatal(u.Raw)
	}
	h.MustOp(owner, "placement.set", map[string]any{"app": appB, "node": n2})
	nibB := appRecord(t, h, appB).GetString("nib_app_id")
	depB := nibB + "-dep-" + appRecord(t, h, appB).GetString("deployment_seq")
	if status, out := report(t, h, t2, reportOf(hostB, appReport(nibB, depB, "failed"))); status != 200 || num(out["accepted"]) != 1 {
		t.Fatalf("N2 report: %d %v", status, out)
	}
	h.MustOp(owner, "invite.create", map[string]any{"email": "c@example.test"})
	usageRows(t, h, appB)

	// A's own activity, then a placement on N1: the positive controls that
	// prove each stream is live before the absences are checked.
	appA := deployedApp(t, h, a, "app-of-a", 1)
	h.MustOp(owner, "placement.set", map[string]any{"app": appA, "node": n1})
	usageRows(t, h, appA)
	clientA.waitFor(t, "apps", appA)
	clientA.waitFor(t, "app_usage_hourly", appA)
	clientA.waitFor(t, "app_usage_daily", appA)
	node1.waitFor(t, "placements", n1)
	node1.waitFor(t, "nodes/"+n1, n1)
	admin.waitFor(t, "apps", appB)
	admin.waitFor(t, "apps", appA)
	time.Sleep(300 * time.Millisecond) // any late, wrongly delivered message

	for _, e := range clientA.snapshot() {
		if strings.Contains(e.Data, appB) || strings.Contains(e.Data, nibB) || strings.Contains(e.Data, userB.Id) || strings.Contains(e.Data, n2) || strings.Contains(e.Data, "c@example.test") {
			t.Errorf("A received B's or another party's record on %s: %s", e.Name, e.Data)
		}
		switch e.Name {
		case "nodes", "placements", "invites", "users":
			t.Errorf("A received %s: %s", e.Name, e.Data)
		}
	}
	for _, e := range node1.snapshot() {
		if e.Name != "nodes/"+n1 && e.Name != "nodes" && e.Name != "placements" {
			t.Errorf("N1 received %s: %s", e.Name, e.Data)
		}
		if strings.Contains(e.Data, n2) || strings.Contains(e.Data, appB) {
			t.Errorf("N1 received N2's or B's record on %s: %s", e.Name, e.Data)
		}
	}
	if got := anonymous.snapshot(); len(got) != 0 {
		t.Errorf("an anonymous subscription received %v", got)
	}
}

// TestCrewSignInProvisioning exercises the verified mailbox through the same
// creation, upload, placement and node pull a real Crew deployment uses.
func TestCrewSignInProvisioning(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, _, nodeA := h.Node("node-one", hostA)
	_, _, nodeB := h.Node("node-two", hostB)
	create := func(token, slug string) map[string]any {
		t.Helper()
		return appOf(t, h.MustOp(token, "app.create", map[string]any{"slug": slug, "name": slug, "kind": "crew"}))
	}
	place := func(token, app, node string) {
		t.Helper()
		path := uploadLink(t, h, token, app, map[string]any{"activate": true})
		if a := upload(t, h, path, droptest.ELF(4096, 1)); a.Status != http.StatusOK {
			t.Fatal(a.Raw)
		}
		h.MustOp(admin, "placement.set", map[string]any{"app": app, "node": node})
	}
	// The hosted owner hasn't signed in yet. An existing account alone is insufficient.
	wantError(t, h.Op(admin, "app.create", map[string]any{"name": "Pending", "slug": "pending", "kind": "crew"}), 403, "forbidden")
	const alice = "alice+crew@example.test"
	const bob = "bob@example.test"
	h.Invite(alice, "developer")
	h.Invite(bob, "developer")
	a := h.SignIn(alice)
	b := h.SignIn(bob)
	ordinary := appOf(t, h.MustOp(a.Token, "app.create", map[string]any{"name": "Ordinary", "slug": "plain-app"}))
	if ordinary["kind"] != "ordinary" || ordinary["sign_in_email_enabled"] != false || ordinary["crew_owner_email"] != nil {
		t.Fatal("ordinary app received mail configuration")
	}
	wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": ordinary["id"], "sign_in_email_enabled": true}), 400, "invalid_input")
	for _, name := range []string{"AWS_WORKLOAD_IDENTITY", "AWS_ROLE_SESSION_NAME", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE"} {
		wantError(t, h.Op(a.Token, "secret.set", map[string]any{"app": ordinary["id"], "name": name, "value": "fixture"}), 400, "invalid_input")
		wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": ordinary["id"], "env": map[string]any{"set": map[string]string{name: "fixture"}}}), 400, "invalid_input")
	}
	appA, appB := create(a.Token, "crew-alice"), create(b.Token, "crew-bob")
	if appA["kind"] != "crew" || appA["crew_owner_email"] != alice || appA["sign_in_email_enabled"] != true {
		t.Fatalf("creation %v", appA)
	}
	idA, idB := appA["id"].(string), appB["id"].(string)
	if num(appA["hook_port"]) != 8081 || appA["executable_name"] != "crew" || num(ordinary["hook_port"]) != 0 {
		t.Fatal("invalid app defaults")
	}
	wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": idA, "hook_port": 8080}), 400, "invalid_input")
	wantError(t, h.Op(b.Token, "app.set", map[string]any{"app": idA, "hook_port": 8181}), 404, "not_found")
	change := h.MustOp(a.Token, "app.set", map[string]any{"app": idA, "hook_port": 8181})
	if change["deployment_bumped"] != true {
		t.Fatal("hook change did not bump deployment")
	}
	for _, name := range []string{"CREW_DROP_BUCKET", "AWS_REGION", "CREW_DROP_APP_ID", "AWS_WORKLOAD_IDENTITY", "POCKETCONTEXT_DOMAIN", "CREW_DROP_BOOTSTRAP", "AWS_ROLE_SESSION_NAME"} {
		wantError(t, h.Op(a.Token, "secret.set", map[string]any{"app": idA, "name": name, "value": "dummy"}), 400, "invalid_input")
		wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": idA, "env": map[string]any{"set": map[string]string{name: "dummy"}}}), 400, "invalid_input")
	}
	for _, field := range []string{"crew_owner_email", "recipient", "owner_email"} {
		wantError(t, h.Op(a.Token, "app.create", map[string]any{"slug": "other", "name": "Other", "kind": "crew", field: bob}), 400, "invalid_input")
		wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": idA, field: bob}), 400, "invalid_input")
	}
	wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": idA, "kind": "ordinary"}), 400, "invalid_input")
	wantError(t, h.Op(a.Token, "app.set", map[string]any{"app": idA, "env": map[string]any{"set": map[string]string{"DROP_OWNER_EMAIL": bob}}}), 400, "invalid_input")
	wantError(t, h.Op(a.Token, "secret.set", map[string]any{"app": idA, "name": "DROP_OWNER_EMAIL", "value": bob}), 400, "invalid_input")
	wantError(t, h.Op(b.Token, "app.set", map[string]any{"app": idA, "sign_in_email_enabled": false}), 404, "not_found")
	place(a.Token, idA, "node-one")
	place(b.Token, idB, "node-two")
	assertRecipient := func(node, want, other string, enabled bool) {
		t.Helper()
		d, raw := desired(t, h, node)
		if len(d.Apps) != 1 || d.Apps[0].CrewOwnerEmail != want || d.Apps[0].SignInEmailEnabled != enabled || strings.Contains(raw, other) {
			t.Fatalf("unexpected per-node Crew binding: %s", raw)
		}
	}
	assertRecipient(nodeA, alice, bob, true)
	assertRecipient(nodeB, bob, alice, true)
	wantError(t, h.Op(b.Token, "app.set", map[string]any{"app": idA, "finish_enrollment": true}), 404, "not_found")
	finished := h.MustOp(a.Token, "app.set", map[string]any{"app": idA, "finish_enrollment": true})
	if finished["deployment_bumped"] != true {
		t.Fatal("finishing enrollment did not remove bootstrap")
	}
	afterEnrollment, _ := desired(t, h, nodeA)
	if !afterEnrollment.Apps[0].ManagedCrew || afterEnrollment.Apps[0].CrewFreshEnrollment {
		t.Fatal("finished enrollment must keep the managed runtime without bootstrap")
	}
	h.MustOp(a.Token, "app.set", map[string]any{"app": idA, "sign_in_email_enabled": false})
	assertRecipient(nodeA, alice, bob, false)
	h.MustOp(a.Token, "app.set", map[string]any{"app": idA, "sign_in_email_enabled": true})

	const updated = "alice-new@example.test"
	user, err := h.App.FindAuthRecordByEmail("users", alice)
	if err != nil {
		t.Fatal(err)
	}
	user.SetEmail(updated)
	user.SetVerified(false)
	if err = h.App.Save(user); err != nil {
		t.Fatal(err)
	}
	// Verify the new address through the same hosted OTP endpoint.
	next := h.SignIn(updated)
	if newApp := create(next.Token, "crew-new"); newApp["crew_owner_email"] != updated {
		t.Fatal(newApp)
	}
	h.MustOp(next.Token, "app.set", map[string]any{"app": idA, "memory_mib": 768})
	assertRecipient(nodeA, alice, updated, true)
	h.MustOp(admin, "placement.set", map[string]any{"app": idA, "node": "node-two"})
	if d, _ := desired(t, h, nodeA); len(d.Apps) != 0 {
		t.Fatal("old node still receives moved app")
	}
	d, _ := desired(t, h, nodeB)
	if len(d.Apps) != 2 {
		t.Fatal("destination missing app")
	}
	for _, app := range d.Apps {
		if app.AppID == appA["nib_app_id"] && (app.CrewOwnerEmail != alice || !app.SignInEmailEnabled) {
			t.Fatal("move changed binding")
		}
	}
}

func TestLinkedReleaseEntryAndImportAccess(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	raw := "name=pocketbase&binary=https%3A%2F%2Fexample.test%2Fpb.zip&port=8090&arg=%5B%22serve%22%5D&minimal=true"
	r := h.Request("GET", "/deploy?"+raw, "", "")
	if r.Code != http.StatusFound || r.Header().Get("Location") != "/#/deploy?"+raw {
		t.Fatalf("GET deploy returned %d location %q", r.Code, r.Header().Get("Location"))
	}
	wantError(t, h.Op("", "beta.import_url", map[string]any{"url": "https://example.test/"}), 401, "unauthenticated")
	_, token := h.User("linked-release@example.test", "developer")
	result := h.Op(token, "beta.import_url", map[string]any{"url": "https://127.0.0.1/test.tgz"})
	if result.Status != http.StatusBadRequest {
		t.Errorf("nonpublic release returned %d", result.Status)
	}
}

func TestOwnerPrivateFirstRunAction(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, a := h.User("appowner@example.test", "developer")
	_, b := h.User("otherowner@example.test", "developer")
	node, _, agent := h.Node("node-one", hostA)
	app := deployedApp(t, h, a, "first-run", 1)
	h.MustOp(admin, "placement.set", map[string]any{"app": app, "node": node})
	record := appRecord(t, h, app)
	record.Set("generic_beta", true)
	if err := h.App.Save(record); err != nil {
		t.Fatal(err)
	}
	nib := record.GetString("nib_app_id")
	dep := nib + "-dep-" + record.GetString("deployment_seq")
	observed := appReport(nib, dep, "running")
	code := strings.Repeat("Z", 32)
	expiry := time.Now().UTC().Add(8 * time.Minute)
	observed["firstRun"] = map[string]string{"kind": "zeroclaw_pairing", "value": code, "expires_at": expiry.Format(time.RFC3339)}
	send := func() {
		t.Helper()
		if status, _ := report(t, h, agent, reportOf(hostA, observed)); status != 200 {
			t.Fatalf("report rejected: %d", status)
		}
	}
	send()
	filter := map[string]any{"app": app}
	if action, ok := h.MustOp(a, "status", filter)["first_run"].(map[string]any); !ok || action["value"] != code {
		t.Fatal("owner missing private setup")
	}
	if h.MustOp(admin, "status", filter)["first_run"] != nil {
		t.Fatal("setup exposed to hosted admin")
	}
	wantError(t, h.Op(b, "status", filter), 404, "not_found")
	for _, route := range []string{"/api/collections/node_reports/records", "/api/collections/events/records"} {
		for _, token := range []string{admin, a, b, agent} {
			response := routerGet(h, route, token)
			if strings.Contains(response.Body.String(), code) {
				t.Fatal("setup entered collection output")
			}
		}
	}
	delete(observed, "firstRun")
	send()
	if h.MustOp(a, "status", filter)["first_run"] == nil {
		t.Fatal("rotation removed active setup")
	}
	observed["restartCount"] = 1
	send()
	if h.MustOp(a, "status", filter)["first_run"] != nil {
		t.Fatal("stale setup crossed process restart")
	}
	observed["firstRun"] = map[string]string{"kind": "zeroclaw_pairing", "value": code, "expires_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)}
	send()
	if h.MustOp(a, "status", filter)["first_run"] != nil {
		t.Fatal("expired setup remained available")
	}
	observed["firstRun"] = map[string]string{"kind": "zeroclaw_pairing", "value": code, "expires_at": expiry.Format(time.RFC3339)}
	observed["logTail"] = code
	send()
	if err := dropserver.Hooks().OnHostedRestore(h.App); err != nil {
		t.Fatal(err)
	}
	if result := h.MustOp(a, "status", filter); result["first_run"] != nil || result["log_tail"] != nil {
		t.Fatal("restore retained pre-restore guest bootstrap output")
	}
}
