package dropserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/logger"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/leakscan"
)

// Hardening (M10): the limiters on the node routes, the restore of a hosted
// backup and the node's total allocatable.

// fromAddress sends one request through the public handler from a fixed
// client address.
func fromAddress(h *droptest.Hosted, address, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://"+droptest.Domain+path, strings.NewReader(body))
	r.TLS, r.Host, r.RemoteAddr = nil, droptest.Domain, address
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	h.Server.Public().ServeHTTP(w, r)
	return w
}

// frozenAgentClock stops the agent routes' clock and returns a function that
// moves it on.
func frozenAgentClock(h *droptest.Hosted) func(time.Duration) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	dropserver.SetAgentClock(h.App, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	return func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
}

// TestNodeSignInRateLimit: core's hosted budget counts users accounts only,
// so node sign-in and refresh have two limits per client address:
// PocketBase's own rule for password sign-in (2 per 3 seconds) and Drop's
// bucket over both routes (10 at once, then one every 6 seconds; the only
// limit on refresh). Other addresses are unaffected.
func TestNodeSignInRateLimit(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, password, _ := h.Node("node-one", hostA)
	advance := frozenAgentClock(h)
	right, _ := json.Marshal(map[string]string{"identity": "node-one", "password": password})
	const address = "203.0.113.50:1000"

	// Both middleware limits run before form validation. Send an empty form
	// so password hashing cannot consume the three-second test window.
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, fromAddress(h, address, "POST", dropapi.NodeAuthRoute, "", "{}").Code)
	}
	if codes[0] != 400 || codes[1] != 400 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("password attempts %v, want 400 400 429", codes)
	}
	// Drop's bucket: refresh without a valid token is refused (401) until the
	// address's bucket is empty, then 429 rate_limited before any check. The
	// two password attempts PocketBase let through spent two of the ten.
	refreshed := 0
	for i := 0; i < 12; i++ {
		w := fromAddress(h, address, "POST", dropapi.NodeRefreshRoute, "", "")
		if w.Code == http.StatusTooManyRequests {
			if !strings.Contains(w.Body.String(), dropapi.CodeRateLimited) || w.Header().Get("Retry-After") == "" {
				t.Fatalf("429 answer %v %s", w.Header(), w.Body)
			}
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("refresh %d: %d %s", i, w.Code, w.Body)
		}
		refreshed++
	}
	if refreshed != 8 {
		t.Fatalf("%d refreshes before the limit, want 8 (10 less the two checked sign-ins)", refreshed)
	}
	// Another address signs in at once.
	if w := fromAddress(h, "203.0.113.51:1000", "POST", dropapi.NodeAuthRoute, "", string(right)); w.Code != http.StatusOK {
		t.Fatalf("another address: %d %s", w.Code, w.Body)
	}
	// Six seconds later on Drop's clock, one more request from the address.
	advance(6 * time.Second)
	if w := fromAddress(h, address, "POST", dropapi.NodeRefreshRoute, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("after the refill: %d %s", w.Code, w.Body)
	}
	if w := fromAddress(h, address, "POST", dropapi.NodeRefreshRoute, "", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("one refill gives one request: %d", w.Code)
	}
}

// TestAgentRouteRateLimit: each node gets 30 agent route requests at once,
// then one a second, whatever its address; another node is unaffected.
func TestAgentRouteRateLimit(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, _, t1 := h.Node("node-one", hostA)
	_, _, t2 := h.Node("node-two", hostB)
	advance := frozenAgentClock(h)
	for i := 0; i < 30; i++ {
		if w := fromAddress(h, fmt.Sprintf("198.51.100.%d:1", 1+i), "GET", dropapi.AgentDesiredRoute, t1, ""); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	w := h.Request("GET", dropapi.AgentDesiredRoute, t1, "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), dropapi.CodeRateLimited) {
		t.Fatalf("31st request: %d %v %s", w.Code, w.Header(), w.Body)
	}
	body, _ := json.Marshal(reportOf(hostA))
	if w := h.Request("POST", dropapi.AgentReportRoute, t1, string(body)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("reports share the budget: %d", w.Code)
	}
	if w := h.Request("GET", dropapi.AgentDesiredRoute, t2, ""); w.Code != http.StatusOK {
		t.Fatalf("node two: %d", w.Code)
	}
	advance(time.Second)
	if w := h.Request("GET", dropapi.AgentDesiredRoute, t1, ""); w.Code != http.StatusOK {
		t.Fatalf("after a second: %d", w.Code)
	}
	if w := h.Request("GET", dropapi.AgentDesiredRoute, t1, ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("one second gives one request: %d", w.Code)
	}
}

// TestHostedRestoreInvalidatesNodeTokens: serving a restored backup
// (--hosted-restore) ends every node token issued before it, as core does
// for accounts; the node's password still signs in, so the agent recovers by
// itself.
func TestHostedRestoreInvalidatesNodeTokens(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, p1, t1 := h.Node("node-one", hostA)
	_, _, t2 := h.Node("node-two", hostB)
	_, owner := h.User("app-owner@example.test", "developer")
	app := newApp(t, h, owner, "restore-app")
	link := uploadLink(t, h, owner, app, nil)
	hook := dropserver.Hooks().OnHostedRestore
	if hook == nil {
		t.Fatal("Drop registers no restore hook")
	}
	if err := hook(h.App); err != nil {
		t.Fatal(err)
	}
	wantUploadError(t, upload(t, h, link, droptest.ELF(4096, 1)), 404, dropapi.CodeUploadLinkInvalid)
	for _, token := range []string{t1, t2} {
		if w := h.Request("GET", dropapi.AgentDesiredRoute, token, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("a node token from before the restore: %d", w.Code)
		}
		if w := h.Request("POST", dropapi.NodeRefreshRoute, token, ""); w.Code == http.StatusOK {
			t.Fatal("a node token from before the restore refreshes")
		}
	}
	status, fresh := h.NodeLogin("node-one", p1)
	if status != http.StatusOK {
		t.Fatalf("sign-in after the restore: %d", status)
	}
	if w := h.Request("GET", dropapi.AgentDesiredRoute, fresh, ""); w.Code != http.StatusOK {
		t.Fatalf("new token: %d", w.Code)
	}
	// Accounts' tokens are core's: unchanged here.
	if me := h.Op(h.Token(h.Owner()), "me", nil); me.Status != 200 {
		t.Fatal(me.Raw)
	}
}

// TestPlacementChecksNodeTotal: placement.set checks the sum of the apps on
// a node, not each app alone, against the node's allocatable; archived apps
// and the app being moved do not count.
func TestPlacementChecksNodeTotal(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, a := h.User("a@example.test", "developer")
	h.MustOp(owner, "user.set", map[string]any{"user": mustUser(t, h, "a@example.test"), "app_quota": 10})
	n1, _, t1 := h.Node("node-one", hostA)
	small := reportOf(hostA)
	small["nibrunner"].(map[string]any)["allocatable"] = map[string]any{"vcpuCount": 3, "memoryMib": 2048, "cacheBytes": 0}
	small["nibrunner"].(map[string]any)["capacity"] = map[string]any{"vcpuCount": 3, "memoryMib": 2048, "cacheBytes": 0}
	if status, out := report(t, h, t1, small); status != 200 {
		t.Fatalf("report %v", out)
	}
	mk := func(slug string, vcpu, memory int) string {
		return appOf(t, h.MustOp(a, "app.create", map[string]any{"name": slug, "slug": slug, "vcpu": vcpu, "memory_mib": memory}))["id"].(string)
	}
	one, two, three := mk("app-one", 2, 512), mk("app-two", 1, 512), mk("app-three", 1, 512)
	h.MustOp(owner, "placement.set", map[string]any{"app": one, "node": n1})
	// The running first app has already been subtracted in allocatable.
	small["nibrunner"].(map[string]any)["allocatable"] = map[string]any{"vcpuCount": 1, "memoryMib": 1536}
	if code, out := report(t, h, t1, small); code != 200 {
		t.Fatal(out)
	}
	h.MustOp(owner, "placement.set", map[string]any{"app": two, "node": n1}) // 3 of 3 vCPUs
	r := h.Op(owner, "placement.set", map[string]any{"app": three, "node": n1})
	wantError(t, r, 409, dropapi.CodeNodeTooSmall)
	if !strings.Contains(r.Raw, "3 vCPUs") {
		t.Errorf("the refusal explains the totals: %s", r.Raw)
	}
	small["nibrunner"].(map[string]any)["allocatable"] = small["nibrunner"].(map[string]any)["capacity"]
	if code, out := report(t, h, t1, small); code != 200 {
		t.Fatal(out)
	}
	// Placing an app again on its node does not count it twice.
	h.MustOp(owner, "placement.set", map[string]any{"app": one, "node": n1})
	// Resource edits on an existing placement have the same aggregate budget.
	resize := h.Op(a, "app.set", map[string]any{"app": two, "vcpu": 2})
	wantError(t, resize, 409, dropapi.CodeNodeTooSmall)
	if strings.Contains(resize.Raw, "node-one") {
		t.Fatal("resize exposed the node name")
	}
	// Archived apps free their share.
	h.MustOp(a, "app.archive", map[string]any{"app": two})
	h.MustOp(owner, "placement.set", map[string]any{"app": three, "node": n1}) // 3 vCPUs, 1024 MiB
	wantError(t, h.Op(a, "app.restore", map[string]any{"app": two}), 409, dropapi.CodeNodeTooSmall)
	h.MustOp(owner, "placement.clear", map[string]any{"app": one}) // 1 vCPU, 512 MiB
	h.MustOp(owner, "placement.set", map[string]any{"app": mk("app-big", 1, 1536), "node": n1})
	// Memory counts too: 2 vCPUs and 2048 MiB are taken.
	wantError(t, h.Op(owner, "placement.set", map[string]any{"app": mk("app-four", 1, 512), "node": n1}), 409, dropapi.CodeNodeTooSmall)
}

func mustUser(t *testing.T, h *droptest.Hosted, email string) string {
	t.Helper()
	u, err := h.App.FindAuthRecordByEmail("users", email)
	if err != nil {
		t.Fatal(err)
	}
	return u.Id
}

// TestSecretLeakScan: one pass over every place a credential could leak to on
// the server side, with internal/leakscan. Sentinels: a secret value, a node
// password, an upload token and the node's and account's tokens. Sources:
// every answer (except the one-time answers that show a password or a link
// once), the desired document and the reports, the database row by row
// (events, node reports, idempotency keys and every other table), the log
// database, the data directory's raw files and the standard log.
func TestSecretLeakScan(t *testing.T) {
	var std bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&std)
	defer log.SetOutput(previous)
	h := droptest.NewHosted(t, droptest.Options{})
	handler, ok := h.App.Logger().Handler().(*logger.BatchHandler)
	if !ok {
		t.Fatalf("logger handler %T", h.App.Logger().Handler())
	}
	handler.SetLevel(slog.Level(-99999))
	random := make([]byte, 12)
	_, _ = rand.Read(random)
	secret := "LEAK-SENTINEL-" + hex.EncodeToString(random)

	owner := h.Token(h.Owner())
	_, a := h.User("a@example.test", "developer")
	var answers []leakscan.Source
	keep := func(name string, r droptest.OpResult) droptest.OpResult {
		answers = append(answers, leakscan.Text(name, r.Raw))
		if r.Status != 200 {
			t.Fatalf("%s: %s", name, r.Raw)
		}
		return r
	}
	app := appOf(t, keep("app.create", h.Op(a, "app.create", map[string]any{"name": "Leak", "slug": "leak-app"})).Result)["id"].(string)
	keep("secret.set", h.Op(a, "secret.set", map[string]any{"app": app, "name": "API_TOKEN", "value": secret}, "retry-1"))
	keep("secret.set replay", h.Op(a, "secret.set", map[string]any{"app": app, "name": "API_TOKEN", "value": secret}, "retry-1"))
	// The link answer shows the token once; it is not scanned.
	link := h.MustOp(a, "release.upload_link", map[string]any{"app": app, "activate": true})
	path, _ := link["path"].(string)
	uploadToken := path[strings.LastIndex(path, "/")+1:]
	if len(uploadToken) != 32 {
		t.Fatalf("link %v", link)
	}
	if u := upload(t, h, path, droptest.ELF(4096, 5)); u.Status != 200 {
		t.Fatal(u.Raw)
	} else {
		answers = append(answers, leakscan.Text("upload", u.Raw))
	}
	// node.create shows the password once; it is not scanned.
	n1, password, nodeToken := h.Node("node-one", hostA)
	keep("placement.set", h.Op(owner, "placement.set", map[string]any{"app": app, "node": n1}))
	d, raw := desired(t, h, nodeToken)
	answers = append(answers, leakscan.Text("desired", raw))
	dep := d.Apps[0].DeploymentID
	reportBody := reportOf(hostA, appReport(d.Apps[0].AppID, dep, "running"))
	status, out := report(t, h, nodeToken, reportBody)
	if status != 200 {
		t.Fatalf("report %d %v", status, out)
	}
	answers = append(answers, leakscan.Text("report answer", fmt.Sprint(out)))
	for _, op := range []string{"status", "secret.list", "app.get", "release.list", "event.list"} {
		keep(op, h.Op(a, op, map[string]any{"app": app}))
	}
	keep("node.list", h.Op(owner, "node.list", nil))
	keep("event.list all", h.Op(owner, "event.list", map[string]any{"all": true}))
	for _, p := range []string{"/api/collections/events/records?perPage=500", "/api/collections/apps/records"} {
		answers = append(answers, leakscan.Text(p, h.Request("GET", p, a, "").Body.String()))
	}
	if err := handler.WriteAll(context.WithValue(context.Background(), logger.BlockKey, true)); err != nil {
		t.Fatal(err)
	}
	// The control: the store holds the value.
	nib := appRecord(t, h, app).GetString("nib_app_id")
	if v, err := h.Secrets.Get(context.Background(), "/drop/apps/"+nib[2:]+"/API_TOKEN", 1); err != nil || v != secret {
		t.Fatalf("the store does not hold the value: %v", err)
	}
	sentinels := []leakscan.Sentinel{{Label: "secret value", Value: secret}, {Label: "node password", Value: password}, {Label: "upload token", Value: uploadToken},
		{Label: "node token", Value: nodeToken}, {Label: "account token", Value: a}}
	sources := append(answers,
		leakscan.DB("database", h.App.DB()), leakscan.DB("log database", h.App.AuxDB()),
		leakscan.Path(h.App.DataDir()), leakscan.Text("standard log", std.String()))
	leakscan.Require(t, sentinels, sources...)
	// The scan sees what it should: a planted value is found.
	if f, _ := leakscan.Scan(sentinels, leakscan.DB("database", h.App.DB())); len(f) != 0 {
		t.Fatal(f)
	}
	if f, _ := leakscan.Scan([]leakscan.Sentinel{{Label: "secret name", Value: "API_TOKEN"}}, leakscan.DB("database", h.App.DB())); len(f) == 0 {
		t.Fatal("the database scan misses the secret name it holds")
	}
}

// firstRead runs a concurrent state change after request authentication and
// before the protected operation has received the complete request.
type firstRead struct {
	io.Reader
	before func()
}

func (r *firstRead) Read(p []byte) (int, error) {
	if r.before != nil {
		r.before()
		r.before = nil
	}
	return r.Reader.Read(p)
}

func TestAuthorizationAtCommit(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	user, auth := h.User("tenant@example.test", "developer")
	app := newApp(t, h, auth, "commit-test")
	path := uploadLink(t, h, auth, app, map[string]any{"activate": true})
	deputy, deputyToken := h.User("deputy@example.test", "admin")
	for _, tc := range []struct {
		method, path, content string
		body                  io.Reader
		change                map[string]any
		status                int
	}{
		{http.MethodPost, dropapi.Route, "application/json", strings.NewReader(`{"op":"invite.create","input":{"email":"new@example.test"}}`), map[string]any{"suspended": true}, 401},
		{http.MethodPut, path, "application/octet-stream", bytes.NewReader(droptest.ELF(4096, 1)), map[string]any{"suspended": true}, 404},
	} {
		tc.change["user"] = user.Id
		if tc.method == http.MethodPost {
			tc.change["user"] = deputy.Id
		}
		body := &firstRead{Reader: tc.body, before: func() { h.MustOp(owner, "user.set", tc.change) }}
		req := httptest.NewRequest(tc.method, "https://"+droptest.Domain+tc.path, body)
		req.TLS, req.Host = nil, droptest.Domain
		req.Header.Set("Content-Type", tc.content)
		if tc.method == http.MethodPost {
			req.Header.Set("Authorization", deputyToken)
		}
		w := httptest.NewRecorder()
		h.Server.Public().ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s returned %d: %s", tc.method, w.Code, w.Body)
		}
	}
	if len(events(t, h, "release.upload")) != 0 || appRecord(t, h, app).GetString("current_release") != "" {
		t.Fatal("suspended upload became a release")
	}
}

func TestDesiredFailsOnMissingCurrentRelease(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, _, node := h.Node("node-one", hostA)
	_, user := h.User("tenant@example.test", "developer")
	app := deployedApp(t, h, user, "placed-app", 1)
	h.MustOp(owner, "placement.set", map[string]any{"app": app, "node": "node-one"})
	if _, err := h.App.DB().NewQuery("UPDATE apps SET current_release='missingrelease0' WHERE id={:app}").Bind(dbx.Params{"app": app}).Execute(); err != nil {
		t.Fatal(err)
	}
	w := h.Request(http.MethodGet, dropapi.AgentDesiredRoute, node, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing release returned a desired document: %d %s", w.Code, w.Body)
	}
}

// TestCleanupMigrationAndRestore exercises a database from before the cleanup
// migration. A routine upgrade keeps outstanding upload links; a deliberate
// restore invalidates them and drops stale external deletion requests.
func TestCleanupMigrationAndRestore(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, owner := h.User("app-owner@example.test", "developer")
	app := newApp(t, h, owner, "upgrade-app")
	bytes := droptest.ELF(4096, 2)
	if u := upload(t, h, uploadLink(t, h, owner, app, nil), bytes); u.Status != 200 {
		t.Fatal(u.Raw)
	}
	keep := uploadLink(t, h, owner, app, nil)
	preRestore := uploadLink(t, h, owner, app, nil)
	db := h.App.DB()
	for _, stmt := range []string{
		"DROP TABLE drop_secret_deletions",
		"DELETE FROM _migrations WHERE file='1820001300_secret_cleanup.js'",
		"UPDATE events SET detail=json_set(detail,'$.stored',json('false')) WHERE action='release.upload'",
	} {
		if _, err := db.NewQuery(stmt).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.App.RunAppMigrations(); err != nil {
		t.Fatal(err)
	}
	records := events(t, h, "release.upload")
	if len(records) != 1 || strings.Contains(records[0].GetString("detail"), `"stored"`) || !strings.Contains(records[0].GetString("detail"), `"sha256"`) {
		t.Fatal("upgrade did not remove only the shared-store flag")
	}
	if u := upload(t, h, keep, bytes); u.Status != 200 {
		t.Fatal("upgrade invalidated pending link", u.Raw)
	}
	parameter := "/drop/apps/" + app + "/TOKEN"
	if _, err := db.Insert("drop_secret_deletions", dbx.Params{"parameter": parameter}).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secrets.Put(context.Background(), parameter, "recreated"); err != nil {
		t.Fatal(err)
	}
	if err := dropserver.Hooks().OnHostedRestore(h.App); err != nil {
		t.Fatal(err)
	}
	wantUploadError(t, upload(t, h, preRestore, bytes), 404, dropapi.CodeUploadLinkInvalid)
	var queued int
	if err := db.NewQuery("SELECT count(*) FROM drop_secret_deletions").Row(&queued); err != nil || queued != 0 {
		t.Fatalf("stale deletion queued: %d %v", queued, err)
	}
	if value, err := h.Secrets.Get(context.Background(), parameter, 1); err != nil || value != "recreated" {
		t.Fatal("restore deleted a newer parameter")
	}
}
