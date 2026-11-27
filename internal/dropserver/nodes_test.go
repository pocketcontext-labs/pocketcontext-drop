package dropserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// Nodes, placements and the agent API (M7): node sign-in and isolation,
// desired_seq bumps, the desired document, reports and heartbeat throttling.

const hostA, hostB = "host-a-0001", "host-b-0002"

// deployedApp creates an app owned by token's account with a deployed
// release and returns its id.
func deployedApp(t *testing.T, h *droptest.Hosted, token, slug string, seed byte) string {
	t.Helper()
	app := newApp(t, h, token, slug)
	path := uploadLink(t, h, token, app, map[string]any{"activate": true})
	if a := upload(t, h, path, droptest.ELF(4096, seed)); a.Status != http.StatusOK || !a.Result.Deployed {
		t.Fatalf("upload: %s", a.Raw)
	}
	return app
}

func seqOf(t *testing.T, h *droptest.Hosted, node string) int {
	t.Helper()
	r, err := h.App.FindRecordById("nodes", node)
	if err != nil {
		t.Fatal(err)
	}
	return r.GetInt("desired_seq")
}

// desired pulls a node's desired document through the public handler.
func desired(t *testing.T, h *droptest.Hosted, token string) (dropapi.Desired, string) {
	t.Helper()
	w := h.Request("GET", dropapi.AgentDesiredRoute, token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("desired: %d %s", w.Code, w.Body)
	}
	var d dropapi.Desired
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d, w.Body.String()
}

func report(t *testing.T, h *droptest.Hosted, token string, r map[string]any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(r)
	w := h.Request("POST", dropapi.AgentReportRoute, token, string(body))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func reportOf(host string, apps ...map[string]any) map[string]any {
	if apps == nil {
		apps = []map[string]any{}
	}
	return map[string]any{
		"schema": dropapi.ReportSchema, "hostId": host, "agentVersion": "0.0.0-test", "desiredSeq": 1,
		"acceptedRevision": "drop-1-000000000000",
		"nibrunner": map[string]any{
			"state": "ready", "message": "", "reportedAt": "2026-11-20T10:00:00Z",
			"capacity":    map[string]any{"vcpuCount": 8, "memoryMib": 16384, "cacheBytes": 1 << 30},
			"allocatable": map[string]any{"vcpuCount": 6, "memoryMib": 12288, "cacheBytes": 1 << 30},
			"versions":    map[string]any{"agent": "v2026.11.12", "firecracker": "1.10", "guestImage": "g1", "zerofs": "z1"},
		},
		"apps": apps,
	}
}

func appReport(nibAppID, deployment, phase string) map[string]any {
	return map[string]any{"appId": nibAppID, "deploymentId": deployment, "phase": phase, "message": "", "restartCount": 0, "lastExitCode": nil, "convergedAt": ""}
}

func appRecord(t *testing.T, h *droptest.Hosted, id string) *core.Record {
	t.Helper()
	r, err := h.App.FindRecordById("apps", id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// routerGet reads a record route on the unfiltered router, where only the
// collection rules decide (the realtime subscriptions use the same rules).
func routerGet(h *droptest.Hosted, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://"+droptest.Domain+path, nil)
	r.Host, r.TLS = droptest.Domain, nil
	r.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	h.Server.Router().ServeHTTP(w, h.Server.MarkPublic(r))
	return w
}

func TestNodeCreateSignInAndSecrets(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	created := h.Op(owner, "node.create", map[string]any{"name": "walter-aws", "host_id": hostA}, "create-1")
	if created.Status != 200 {
		t.Fatal(created.Raw)
	}
	password, _ := created.Result["password"].(string)
	if len(password) != dropapi.NodePasswordLength || created.Result["identity"] != "walter-aws" || created.Result["collection"] != "nodes" {
		t.Fatalf("node.create %v", created.Result)
	}
	// The password is shown once: never stored for replay or in an event.
	wantError(t, h.Op(owner, "node.create", map[string]any{"name": "walter-aws", "host_id": hostA}, "create-1"), 409, dropapi.CodeIdempotencyNoResult)
	if dump := dumpTables(t, h.App.DB()); strings.Contains(dump, password) {
		t.Fatal("the node password reached the database in clear")
	}
	wantError(t, h.Op(owner, "node.create", map[string]any{"name": "walter-aws", "host_id": "other"}), 409, dropapi.CodeNodeExists)
	wantError(t, h.Op(owner, "node.create", map[string]any{"name": "other", "host_id": hostA}), 409, dropapi.CodeNodeExists)
	wantError(t, h.Op(owner, "node.create", map[string]any{"name": "Bad Name", "host_id": hostB}), 400, dropapi.CodeInvalidInput)
	_, userToken := h.User("user@example.test", "user")
	wantError(t, h.Op(userToken, "node.create", map[string]any{"name": "mine", "host_id": hostB}), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(userToken, "node.list", nil), 403, dropapi.CodeForbidden)

	status, token := h.NodeLogin("walter-aws", password)
	if status != 200 {
		t.Fatalf("node sign-in %d", status)
	}
	if status, _ := h.NodeLogin("walter-aws", strings.Repeat("x", 40)); status != 400 {
		t.Fatalf("wrong password: %d", status)
	}
	if w := h.Request("POST", dropapi.NodeRefreshRoute, token, ""); w.Code != 200 {
		t.Fatalf("refresh %d %s", w.Code, w.Body)
	}
	// Users still never sign in with a password on the hosted origin.
	if w := h.Request("POST", "/api/collections/users/auth-with-password", "", `{"identity":"owner@example.test","password":"x"}`); w.Code != 404 {
		t.Fatalf("users password sign-in: %d", w.Code)
	}
	// An oversized sign-in body is refused.
	if w := h.Request("POST", dropapi.NodeAuthRoute, "", `{"identity":"walter-aws","password":"`+strings.Repeat("x", 20<<10)+`"}`); w.Code == 200 {
		t.Fatal("an oversized node sign-in was accepted")
	}

	// Rotating ends the old password and token.
	rotated := h.MustOp(owner, "node.rotate_secret", map[string]any{"node": "walter-aws"})
	if status, _ := h.NodeLogin("walter-aws", password); status != 400 {
		t.Fatal("the old password still signs in")
	}
	if w := h.Request("GET", dropapi.AgentDesiredRoute, token, ""); w.Code != 401 {
		t.Fatalf("old token after rotation: %d", w.Code)
	}
	status, token = h.NodeLogin("walter-aws", rotated["password"].(string))
	if status != 200 {
		t.Fatal("the new password does not sign in")
	}
	// Draining keeps the agent working; disabling locks it out.
	h.MustOp(owner, "node.set_state", map[string]any{"node": "walter-aws", "state": "draining"})
	if d, _ := desired(t, h, token); d.State != "draining" {
		t.Fatalf("draining state %q", d.State)
	}
	h.MustOp(owner, "node.set_state", map[string]any{"node": "walter-aws", "state": "disabled"})
	if w := h.Request("GET", dropapi.AgentDesiredRoute, token, ""); w.Code != 401 {
		t.Fatalf("disabled node's token: %d", w.Code)
	}
	if status, _ := h.NodeLogin("walter-aws", rotated["password"].(string)); status != 403 {
		t.Fatal("a disabled node signed in")
	}
	list := h.MustOp(owner, "node.list", nil)["nodes"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["state"] != "disabled" {
		t.Fatalf("node.list %v", list)
	}
	for _, e := range events(t, h, "node.create") {
		if strings.Contains(e.GetString("detail"), password) {
			t.Fatal("the password is in an event")
		}
	}
}

func TestDesiredSeqBumps(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, tokenA := h.User("a@example.test", "developer")
	n1, _, _ := h.Node("node-one", hostA)
	n2, _, _ := h.Node("node-two", hostB)
	app := deployedApp(t, h, tokenA, "app-one", 1)
	bare := newApp(t, h, tokenA, "no-release")

	s1, s2 := seqOf(t, h, n1), seqOf(t, h, n2)
	step := func(name string, run func(), bump1, bump2 bool) {
		t.Helper()
		run()
		g1, g2 := seqOf(t, h, n1), seqOf(t, h, n2)
		if (g1 > s1) != bump1 || (g2 > s2) != bump2 || g1 > s1+1 || g2 > s2+1 {
			t.Fatalf("%s: node one %d -> %d (bump %v), node two %d -> %d (bump %v)", name, s1, g1, bump1, s2, g2, bump2)
		}
		s1, s2 = g1, g2
	}
	op := func(token, name string, input map[string]any) func() {
		return func() { h.MustOp(token, name, input) }
	}
	step("placement of an app without a release", op(owner, "placement.set", map[string]any{"app": bare, "node": n1}), false, false)
	step("placement", op(owner, "placement.set", map[string]any{"app": app, "node": n1}), true, false)
	step("placing again", op(owner, "placement.set", map[string]any{"app": app, "node": n1}), false, false)
	step("rename", op(tokenA, "app.set", map[string]any{"app": app, "name": "Renamed"}), false, false)
	step("env", op(tokenA, "app.set", map[string]any{"app": app, "env": map[string]any{"set": map[string]string{"WELCOME_TEXT": "hi"}}}), true, false)
	step("stop", op(tokenA, "app.stop", map[string]any{"app": app}), true, false)
	step("start", op(tokenA, "app.start", map[string]any{"app": app}), true, false)
	step("secret set", op(tokenA, "secret.set", map[string]any{"app": app, "name": "API_TOKEN", "value": "synthetic-1"}), true, false)
	step("secret set again", op(tokenA, "secret.set", map[string]any{"app": app, "name": "API_TOKEN", "value": "synthetic-2"}), true, false)
	step("secret delete", op(tokenA, "secret.delete", map[string]any{"app": app, "name": "API_TOKEN"}), true, false)
	step("unrelated invite", op(owner, "invite.create", map[string]any{"email": "z@example.test"}), false, false)
	step("upload without activate", func() {
		path := uploadLink(t, h, tokenA, app, nil)
		if a := upload(t, h, path, droptest.ELF(4096, 9)); a.Status != 200 {
			t.Fatal(a.Raw)
		}
	}, false, false)
	releases := h.MustOp(tokenA, "release.list", map[string]any{"app": app})["releases"].([]any)
	newest := releases[0].(map[string]any)["id"].(string)
	step("release.deploy", op(tokenA, "release.deploy", map[string]any{"app": app, "release": newest}), true, false)
	step("upload with activate", func() {
		path := uploadLink(t, h, tokenA, app, map[string]any{"activate": true})
		if a := upload(t, h, path, droptest.ELF(4096, 10)); a.Status != 200 || !a.Result.Deployed {
			t.Fatal(a.Raw)
		}
	}, true, false)
	step("move to node two", op(owner, "placement.set", map[string]any{"app": app, "node": n2}), true, true)
	step("draining", op(owner, "node.set_state", map[string]any{"node": n2, "state": "draining"}), false, false)
	step("archive", op(tokenA, "app.archive", map[string]any{"app": app}), false, true)
	step("restore", op(tokenA, "app.restore", map[string]any{"app": app}), false, true)
	step("clear", op(owner, "placement.clear", map[string]any{"app": app}), false, true)
}

func TestDesiredDocument(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, tokenA := h.User("a@example.test", "developer")
	_, _, t1 := h.Node("node-one", hostA)
	app := deployedApp(t, h, tokenA, "app-one", 1)
	h.MustOp(tokenA, "app.set", map[string]any{"app": app, "env": map[string]any{"set": map[string]string{"MODE_NAME": "demo"}}})
	h.MustOp(tokenA, "secret.set", map[string]any{"app": app, "name": "API_TOKEN", "value": "sentinel-secret-value"})
	h.MustOp(owner, "placement.set", map[string]any{"app": app, "node": "node-one"})
	record := appRecord(t, h, app)
	nib := record.GetString("nib_app_id")

	d, raw := desired(t, h, t1)
	if d.Schema != dropapi.DesiredSchema || d.Node != "node-one" || d.HostID != hostA || d.State != "active" || d.DesiredSeq < 1 || len(d.ContentHash) != 64 || len(d.Rejected) != 0 {
		t.Fatalf("envelope %+v", d)
	}
	if strings.Contains(raw, "sentinel-secret-value") || strings.Contains(raw, `"value"`) {
		t.Fatal("the desired document carries a secret value field")
	}
	if strings.Contains(raw, "signInEmailEnabled") || strings.Contains(raw, "crewOwnerEmail") {
		t.Fatal("ordinary app changed the drop.desired/v1 wire format")
	}
	a := d.Apps[0]
	release := record.GetString("current_release")
	rel, _ := h.App.FindRecordById("releases", release)
	if a.AppID != nib || a.DeploymentID != nib+"-dep-"+record.GetString("deployment_seq") || a.Volume.VolumeID != nib+"-vol" ||
		a.Volume.SizeBytes != 1024<<20 || a.Release.SHA256 != rel.GetString("sha256") || a.Release.S3Key != "releases/"+rel.GetString("sha256") ||
		a.DesiredState != "on-request" || a.IdleTimeoutMs != 300000 || a.ExecutableName != "server" || a.HTTPPort != 8080 ||
		a.Resources.VCPU != 1 || a.Resources.MemoryMib != 512 || a.HealthCheck.Kind != "tcp" || a.Env["MODE_NAME"] != "demo" ||
		len(a.Hostnames) != 1 || a.Hostnames[0].Hostname != "app-one."+droptest.AppDomain || a.Hostnames[0].Kind != "platform" {
		t.Fatalf("app %+v", a)
	}
	if len(a.Secrets) != 1 || a.Secrets[0].Name != "API_TOKEN" || a.Secrets[0].Parameter != "/drop/apps/"+nib[2:]+"/API_TOKEN" || a.Secrets[0].Version != 1 {
		t.Fatalf("secrets %+v", a.Secrets)
	}
	// The agent's side: fill the values and render with its own limits.
	out, err := render.Render(render.Input{HostID: d.HostID, DesiredSeq: d.DesiredSeq,
		Apps:   []render.App{a.RenderApp(map[string]string{"API_TOKEN": "resolved"})},
		Limits: render.Limits{AllowedHostSuffix: "." + droptest.AppDomain, SSMPrefix: "/drop"}})
	if err != nil || len(out.Rejected) != 0 || !strings.Contains(string(out.Document), `"API_TOKEN": "resolved"`) {
		t.Fatalf("render: %v %+v", err, out)
	}
	// The same seq gives the same document.
	if again, _ := desired(t, h, t1); again.ContentHash != d.ContentHash || again.DesiredSeq != d.DesiredSeq {
		t.Fatal("a second pull differs")
	}

	// A smaller allocatable than the app needs: the server's check rejects it,
	// and the seq goes up.
	h.MustOp(tokenA, "app.set", map[string]any{"app": app, "vcpu": 2})
	before := d.DesiredSeq + 1
	small := reportOf(hostA)
	small["nibrunner"].(map[string]any)["allocatable"] = map[string]any{"vcpuCount": 1, "memoryMib": 4096, "cacheBytes": 0}
	if status, out := report(t, h, t1, small); status != 200 || int64(num(out["desiredSeq"])) <= before {
		t.Fatalf("report: %d %v (seq before %d)", status, out, before)
	}
	d, _ = desired(t, h, t1)
	if len(d.Apps) != 0 || len(d.Rejected) != 1 || d.Rejected[0].AppID != nib || d.Rejected[0].Field != "resources.vcpu" {
		t.Fatalf("rejected %+v", d)
	}
	wantError(t, h.Op(owner, "placement.set", map[string]any{"app": newAppPlaced(t, h, tokenA), "node": "node-one"}), 409, dropapi.CodeNodeTooSmall)
}

// newAppPlaced creates an app needing two vCPUs (for the allocatable check).
func newAppPlaced(t *testing.T, h *droptest.Hosted, token string) string {
	t.Helper()
	return appOf(t, h.MustOp(token, "app.create", map[string]any{"name": "Big", "slug": "big-app", "vcpu": 2}))["id"].(string)
}

func TestDiskLockedAfterFirstPlacement(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, tokenA := h.User("a@example.test", "developer")
	h.Node("node-one", hostA)
	app := newApp(t, h, tokenA, "app-one")
	h.MustOp(tokenA, "app.set", map[string]any{"app": app, "disk_mib": 2048})
	h.MustOp(owner, "placement.set", map[string]any{"app": app, "node": "node-one"})
	h.MustOp(owner, "placement.clear", map[string]any{"app": app})
	wantError(t, h.Op(tokenA, "app.set", map[string]any{"app": app, "disk_mib": 4096}), 409, dropapi.CodeDiskLocked)
	got := appOf(t, h.MustOp(tokenA, "app.set", map[string]any{"app": app, "disk_mib": 2048, "vcpu": 2}))
	if got["disk_locked"] != true || num(got["disk_mib"]) != 2048 {
		t.Fatalf("app %v", got)
	}
	wantError(t, h.Op(owner, "placement.clear", map[string]any{"app": app}), 404, dropapi.CodeNotFound)
	h.MustOp(tokenA, "app.archive", map[string]any{"app": app})
	wantError(t, h.Op(owner, "placement.set", map[string]any{"app": app, "node": "node-one"}), 409, dropapi.CodeAppArchived)
	h.MustOp(owner, "node.set_state", map[string]any{"node": "node-one", "state": "draining"})
	h.MustOp(tokenA, "app.restore", map[string]any{"app": app})
	wantError(t, h.Op(owner, "placement.set", map[string]any{"app": app, "node": "node-one"}), 409, dropapi.CodeNodeUnavailable)
}

// writeCounter counts database writes to nodes, node_reports and events.
type writeCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func countWrites(h *droptest.Hosted) *writeCounter {
	c := &writeCounter{n: map[string]int{}}
	count := func(e *core.ModelEvent) error {
		if r, ok := e.Model.(*core.Record); ok {
			c.mu.Lock()
			c.n[r.Collection().Name]++
			c.mu.Unlock()
		}
		return e.Next()
	}
	h.App.OnModelAfterCreateSuccess().BindFunc(count)
	h.App.OnModelAfterUpdateSuccess().BindFunc(count)
	return c
}

func (c *writeCounter) take() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.n
	c.n = map[string]int{}
	return out
}

// TestHeartbeatWritesAtMostEveryFiveMinutes: in steady state (a report every
// 15 seconds with nothing new) the node is written once per five minutes and
// its app reports only when they change.
func TestHeartbeatWritesAtMostEveryFiveMinutes(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, tokenA := h.User("a@example.test", "developer")
	n1, _, t1 := h.Node("node-one", hostA)
	app := deployedApp(t, h, tokenA, "app-one", 1)
	h.MustOp(owner, "placement.set", map[string]any{"app": app, "node": n1})
	nib := appRecord(t, h, app).GetString("nib_app_id")
	dep := nib + "-dep-" + appRecord(t, h, app).GetString("deployment_seq")

	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	dropserver.SetAgentClock(h.App, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	writes := countWrites(h)
	send := func(phase string) {
		t.Helper()
		r := reportOf(hostA, appReport(nib, dep, phase))
		// nibrunner's own report time moves on every time; it alone never writes.
		mu.Lock()
		r["nibrunner"].(map[string]any)["reportedAt"] = now.Format(time.RFC3339)
		mu.Unlock()
		if status, out := report(t, h, t1, r); status != 200 || num(out["accepted"]) != 1 {
			t.Fatalf("report: %d %v", status, out)
		}
	}

	send("starting")
	if w := writes.take(); w["nodes"] != 1 || w["node_reports"] != 1 || w["events"] != 1 {
		t.Fatalf("first report writes %v", w)
	}
	advance(15 * time.Second)
	send("running") // quiet transition: the row changes, no event
	if w := writes.take(); w["nodes"] != 0 || w["node_reports"] != 1 || w["events"] != 0 {
		t.Fatalf("phase change writes %v", w)
	}
	// Steady state: 18 more reports over the rest of the five minutes.
	for i := 0; i < 18; i++ {
		advance(15 * time.Second)
		send("running")
	}
	if w := writes.take(); len(w) != 0 {
		t.Fatalf("steady state wrote %v", w)
	}
	status := h.MustOp(tokenA, "status", map[string]any{"app": app})
	if status["phase"] != "running" || status["node"] != nil || status["placed"] != true {
		t.Fatalf("status %v", status)
	}
	advance(15 * time.Second) // five minutes after the first write
	send("running")
	if w := writes.take(); w["nodes"] != 1 || len(w) != 1 {
		t.Fatalf("after five minutes %v", w)
	}
	advance(15 * time.Second)
	send("running")
	if w := writes.take(); len(w) != 0 {
		t.Fatalf("just after the periodic write %v", w)
	}
	// A failure is written at once, with an event.
	advance(15 * time.Second)
	send("failed")
	if w := writes.take(); w["node_reports"] != 1 || w["events"] != 1 || w["nodes"] != 0 {
		t.Fatalf("failure writes %v", w)
	}
	if e := events(t, h, "app.phase"); len(e) != 2 || e[1].GetString("actor_node") != n1 || e[1].GetString("app") != app || e[1].GetString("actor") != "" {
		t.Fatalf("phase events %v", e)
	}
	// The node stopped reporting: unknown. Admins see its name.
	advance(2 * time.Minute)
	status = h.MustOp(owner, "status", map[string]any{"app": app})
	if status["phase"] != "unknown" || status["node"] != "node-one" {
		t.Fatalf("stale status %v", status)
	}
	node := h.MustOp(owner, "node.list", nil)["nodes"].([]any)[0].(map[string]any)
	if node["online"] != false || node["agent_version"] != "0.0.0-test" || node["allocatable"].(map[string]any)["vcpuCount"] != float64(6) {
		t.Fatalf("node %v", node)
	}
}

func TestStatusPhases(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, tokenA := h.User("a@example.test", "developer")
	_, tokenB := h.User("b@example.test", "developer")
	_, _, t1 := h.Node("node-one", hostA)
	app := newApp(t, h, tokenA, "app-one")
	phase := func(token string) map[string]any {
		t.Helper()
		return h.MustOp(token, "status", map[string]any{"app": app})
	}
	if s := phase(tokenA); s["phase"] != "no_release" {
		t.Fatalf("%v", s)
	}
	wantError(t, h.Op(tokenB, "status", map[string]any{"app": app}), 404, dropapi.CodeNotFound)
	path := uploadLink(t, h, tokenA, app, map[string]any{"activate": true})
	upload(t, h, path, droptest.ELF(4096, 3))
	if s := phase(tokenA); s["phase"] != "unplaced" || s["placed"] != false {
		t.Fatalf("%v", s)
	}
	h.MustOp(owner, "placement.set", map[string]any{"app": app, "node": "node-one"})
	if s := phase(tokenA); s["phase"] != "pending" {
		t.Fatalf("%v", s)
	}
	record := appRecord(t, h, app)
	nib, dep := record.GetString("nib_app_id"), record.GetString("nib_app_id")+"-dep-"+record.GetString("deployment_seq")
	r := appReport(nib, dep, "running")
	r["lastHook"] = map[string]any{"event": "suspend", "outcome": "timeout", "durationMs": 5000, "time": "2026-11-24T09:00:00Z"}
	r["restartCount"], r["lastExitCode"] = 2, 1
	if status, out := report(t, h, t1, reportOf(hostA, r)); status != 200 {
		t.Fatal(out)
	}
	if s := phase(tokenA); s["phase"] != "running" || num(s["restart_count"]) != 2 || num(s["last_exit_code"]) != 1 || s["reported_deployment_id"] != dep || s["last_hook"].(map[string]any)["outcome"] != "timeout" {
		t.Fatalf("%v", s)
	}
	h.MustOp(tokenA, "app.set", map[string]any{"app": app, "memory_mib": 1024})
	if s := phase(tokenA); s["phase"] != "deploying" {
		t.Fatalf("%v", s)
	}
	h.MustOp(tokenA, "app.archive", map[string]any{"app": app})
	if s := phase(owner); s["phase"] != "archived" {
		t.Fatalf("%v", s)
	}
	// Bad reports are refused whole.
	bad := reportOf(hostA, appReport(nib, dep, "exploded"))
	if status, out := report(t, h, t1, bad); status != 400 || out["code"] != dropapi.CodeInvalidInput {
		t.Fatalf("bad phase: %d %v", status, out)
	}
	extra := reportOf(hostA)
	extra["cron"] = []any{}
	if status, _ := report(t, h, t1, extra); status != 400 {
		t.Fatalf("unknown field: %d", status)
	}
}
