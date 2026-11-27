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

	_, token := h.User("user@example.test", "developer")
	_, otherToken := h.User("other@example.test", "developer")
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
	if !strings.Contains(logs, "Drop secret store failed") {
		t.Fatalf("the store failure was not logged (redacted): %s", logs)
	}
	if strings.Contains(logs, sentinel) {
		t.Error("a log line holds the secret")
	}
	if strings.Contains(std.String(), sentinel) {
		t.Error("the standard log holds the secret")
	}
}

func TestSecretDeletionCommitAndRecovery(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, owner := h.User("app-owner@example.test", "developer")
	id := newApp(t, h, owner, "cleanup")
	set := map[string]any{"app": id, "name": "TOKEN", "value": "initial"}
	del := map[string]any{"app": id, "name": "TOKEN"}
	path := "/drop/apps/" + id + "/TOKEN"
	h.MustOp(owner, "secret.set", set)
	// Refuse the last audit insert after the operation has changed its metadata.
	// Both the declaration and the exact SSM version must survive rollback.
	if _, err := h.App.DB().NewQuery(`CREATE TRIGGER refuse_audit BEFORE INSERT ON events WHEN NEW.action='secret.delete' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`).Execute(); err != nil {
		t.Fatal(err)
	}
	if got := h.Op(owner, "secret.delete", del); got.Status != 500 {
		t.Fatalf("forced failure returned %s", got.Raw)
	}
	if val, err := h.Secrets.Get(context.Background(), path, 1); err != nil || val != "initial" {
		t.Fatal("failed transaction removed original secret")
	}
	if result := h.MustOp(owner, "secret.list", map[string]any{"app": id}); len(result["secrets"].([]any)) != 1 {
		t.Fatal("failed transaction removed declaration")
	}
	if _, err := h.App.DB().NewQuery("DROP TRIGGER refuse_audit").Execute(); err != nil {
		t.Fatal(err)
	}
	dropserver.SetServices(h.App, dropserver.Services{Secrets: failingStore{}})
	h.MustOp(owner, "secret.delete", del)
	if result := h.MustOp(owner, "secret.list", map[string]any{"app": id}); len(result["secrets"].([]any)) != 0 {
		t.Fatal("committed deletion still declared")
	}
	var pending string
	if err := h.App.DB().NewQuery("SELECT parameter FROM drop_secret_deletions").Row(&pending); err != nil || pending != path {
		t.Fatal("cleanup work was lost")
	}
	dropserver.SetServices(h.App, dropserver.Services{Secrets: h.Secrets})
	set["value"] = "replacement"
	h.MustOp(owner, "secret.set", set)
	if val, err := h.Secrets.Get(context.Background(), path, 1); err != nil || val != "replacement" {
		t.Fatal("recreation did not finish previous deletion")
	}
	var count int
	if err := h.App.DB().NewQuery("SELECT count(*) FROM drop_secret_deletions").Row(&count); err != nil || count != 0 {
		t.Fatal("stale cleanup can delete the replacement")
	}
}
