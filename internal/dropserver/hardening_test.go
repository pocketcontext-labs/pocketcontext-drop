package dropserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	wrong, _ := json.Marshal(map[string]string{"identity": "node-one", "password": strings.Repeat("x", 40)})
	right, _ := json.Marshal(map[string]string{"identity": "node-one", "password": password})
	const address = "203.0.113.50:1000"

	// PocketBase's rule: the third password attempt within 3 seconds.
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, fromAddress(h, address, "POST", dropapi.NodeAuthRoute, "", string(wrong)).Code)
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
	hook := dropserver.Hooks().OnHostedRestore
	if hook == nil {
		t.Fatal("Drop registers no restore hook")
	}
	if err := hook(h.App); err != nil {
		t.Fatal(err)
	}
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
	_, a := h.User("a@example.test", "user")
	h.MustOp(owner, "user.set", map[string]any{"user": mustUser(t, h, "a@example.test"), "app_quota": 10})
	n1, _, t1 := h.Node("node-one", hostA)
	small := reportOf(hostA)
	small["nibrunner"].(map[string]any)["allocatable"] = map[string]any{"vcpuCount": 3, "memoryMib": 2048, "cacheBytes": 0}
	if status, out := report(t, h, t1, small); status != 200 {
		t.Fatalf("report %v", out)
	}
	mk := func(slug string, vcpu, memory int) string {
		return appOf(t, h.MustOp(a, "app.create", map[string]any{"name": slug, "slug": slug, "vcpu": vcpu, "memory_mib": memory}))["id"].(string)
	}
	one, two, three := mk("app-one", 2, 512), mk("app-two", 1, 512), mk("app-three", 1, 512)
	h.MustOp(owner, "placement.set", map[string]any{"app": one, "node": n1})
	h.MustOp(owner, "placement.set", map[string]any{"app": two, "node": n1}) // 3 of 3 vCPUs
	r := h.Op(owner, "placement.set", map[string]any{"app": three, "node": n1})
	wantError(t, r, 409, dropapi.CodeNodeTooSmall)
	if !strings.Contains(r.Raw, "3 vCPUs") {
		t.Errorf("the refusal explains the totals: %s", r.Raw)
	}
	// Placing an app again on its node does not count it twice.
	h.MustOp(owner, "placement.set", map[string]any{"app": one, "node": n1})
	// Archived apps free their share.
	h.MustOp(a, "app.archive", map[string]any{"app": two})
	h.MustOp(owner, "placement.set", map[string]any{"app": three, "node": n1}) // 3 vCPUs, 1024 MiB
	h.MustOp(owner, "placement.clear", map[string]any{"app": one})             // 1 vCPU, 512 MiB
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
	_, a := h.User("a@example.test", "user")
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
