package dropserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/logger"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// failingStore refuses every call with an error that echoes the value, as a
// careless SDK error might.
type failingStore struct{}

func (failingStore) Put(_ context.Context, name, value string) (int64, error) {
	return 0, fmt.Errorf("store refused %s = %s", name, value)
}
func (failingStore) Delete(context.Context, string) error { return errors.New("store refused delete") }

// dumpTables returns every value of every table of a database as text.
func dumpTables(t *testing.T, db dbx.Builder) string {
	t.Helper()
	var tables []string
	if err := db.NewQuery("SELECT name FROM sqlite_master WHERE type = 'table'").Column(&tables); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, table := range tables {
		rows, err := db.NewQuery("SELECT * FROM " + "`" + table + "`").Rows()
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for _, v := range values {
				switch x := v.(type) {
				case []byte:
					out.Write(x)
				default:
					fmt.Fprint(&out, x)
				}
				out.WriteByte('\n')
			}
		}
		rows.Close()
	}
	return out.String()
}

// TestSecretSentinel: a secret value reaches the secret store and nothing
// else: not a database record (events, idempotency keys and every other
// table), not an operation's answer (success or error), not a log line.
func TestSecretSentinel(t *testing.T) {
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
	sentinel := "SENTINEL-" + hex.EncodeToString(random)
	other := sentinel + "-2"

	_, token := h.User("user@example.test", "user")
	_, otherToken := h.User("other@example.test", "user")
	owner := h.Token(h.Owner())
	var answers []string
	call := func(auth, op string, input any, key ...string) droptest.OpResult {
		r := h.Op(auth, op, input, key...)
		answers = append(answers, r.Raw)
		return r
	}
	if r := call(token, "app.create", map[string]any{"name": "Sentinel", "slug": "sentinel"}); r.Status != 200 {
		t.Fatal(r.Raw)
	}
	set := func(auth, name, value string, key ...string) droptest.OpResult {
		return call(auth, "secret.set", map[string]any{"app": "sentinel", "name": name, "value": value}, key...)
	}
	for _, r := range []droptest.OpResult{
		set(token, "API_KEY", sentinel, "retry-1"),
		set(token, "API_KEY", other),
		set(token, "SECOND", sentinel),
	} {
		if r.Status != 200 {
			t.Fatal(r.Raw)
		}
	}
	// Refusals answer without echoing the value.
	for _, r := range []droptest.OpResult{
		set(token, "lower", sentinel),
		set(token, "HOME", sentinel),
		set(token, "OK", sentinel+"${NIBRUN_SECRET}"),
		set(token, "OK", sentinel+strings.Repeat("x", 4096)),
		set(otherToken, "API_KEY", sentinel),
		set(owner, "API_KEY", sentinel),
		set("", "API_KEY", sentinel),
		call(token, "secret.set", map[string]any{"app": "sentinel", "name": "API_KEY", "value": sentinel, "extra": true}),
	} {
		if r.Status == 200 {
			t.Fatalf("accepted: %s", r.Raw)
		}
	}
	// Through MCP (M6 marks the request in-process): set_via mcp.
	body := fmt.Sprintf(`{"op":"secret.set","input":{"app":"sentinel","name":"FROM_MCP","value":%q}}`, sentinel)
	r := httptest.NewRequest("POST", "https://"+droptest.Domain+dropapi.Route, strings.NewReader(body))
	r.Host, r.TLS, r.RemoteAddr = droptest.Domain, nil, "198.51.100.200:40000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	h.Server.Public().ServeHTTP(w, dropserver.WithVia(r, dropapi.SetViaMCP))
	answers = append(answers, w.Body.String())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"set_via":"mcp"`) {
		t.Fatalf("MCP-marked call: %d %s", w.Code, w.Body)
	}
	// A failing store whose error echoes the value: a generic answer and a
	// redacted log line.
	dropserver.SetServices(h.App, dropserver.Services{Secrets: failingStore{}})
	if r := set(token, "API_KEY", sentinel); r.Status != http.StatusBadGateway {
		t.Fatalf("failing store: %s", r.Raw)
	}
	dropserver.SetServices(h.App, dropserver.Services{Secrets: h.Secrets})
	// Every read an account has.
	for _, op := range []string{"secret.list", "app.get"} {
		call(token, op, map[string]any{"app": "sentinel"})
	}
	call(token, "app.list", nil)
	call(owner, "app.list", map[string]any{"all": true})
	for _, path := range []string{"/api/collections/events/records?perPage=200", "/api/collections/apps/records", "/api/collections/app_secret_names/records"} {
		for _, auth := range []string{token, owner} {
			answers = append(answers, h.Request("GET", path, auth, "").Body.String())
		}
	}

	// The control: the store holds the values.
	nib := h.MustOp(token, "app.get", map[string]any{"app": "sentinel"})["app"].(map[string]any)["nib_app_id"].(string)
	if v, err := h.Secrets.Get(context.Background(), "/drop/apps/"+nib[2:]+"/API_KEY", 1); err != nil || v != sentinel {
		t.Fatalf("the store does not hold the value: %v", err)
	}

	for i, answer := range answers {
		if strings.Contains(answer, sentinel) {
			t.Errorf("answer %d holds the secret: %s", i, answer)
		}
	}
	if records := dumpTables(t, h.App.DB()); strings.Contains(records, sentinel) {
		t.Error("a database record holds the secret")
	} else if !strings.Contains(records, "API_KEY") || !strings.Contains(records, "secret.set") {
		t.Error("the records dump misses the secret names or events")
	}
	if err := handler.WriteAll(context.WithValue(context.Background(), logger.BlockKey, true)); err != nil {
		t.Fatal(err)
	}
	logs := dumpTables(t, h.App.AuxDB())
	if !strings.Contains(logs, "Drop secret store failed") || !strings.Contains(logs, "[redacted]") {
		t.Fatalf("the store failure was not logged (redacted): %s", logs)
	}
	if strings.Contains(logs, sentinel) {
		t.Error("a log line holds the secret")
	}
	if strings.Contains(std.String(), sentinel) {
		t.Error("the standard log holds the secret")
	}
}

// TestAppAndSecretIsolation (plan section 9): users A and B and the owner O,
// an admin. Each account reads and changes only its own apps and secrets;
// another account's app is a 404 by id and by slug; admins read every app but
// change only their own.
func TestAppAndSecretIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, a := h.User("a@example.test", "user")
	_, b := h.User("b@example.test", "user")
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
	wantError(t, h.Op(owner, "app.set", map[string]any{"app": appA, "name": "Admin was here"}), 404, "not_found")
	wantError(t, h.Op(owner, "secret.set", map[string]any{"app": appA, "name": "TOKEN", "value": "x"}), 404, "not_found")
	if fresh, _ := h.App.FindRecordById("apps", appA); fresh.GetString("name") != "A" {
		t.Fatal("A's app changed")
	}
}
