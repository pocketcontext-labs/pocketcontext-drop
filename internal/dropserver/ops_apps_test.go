package dropserver_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func appOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	app, ok := result["app"].(map[string]any)
	if !ok {
		t.Fatalf("no app in %v", result)
	}
	return app
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// TestAppCreateDefaults: app.create fills the plan's defaults, derives the
// nibrunner ids and the hostname, starts at deployment 1 and writes one event.
func TestAppCreateDefaults(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	user, token := h.User("user@example.test", "user")
	app := appOf(t, h.MustOp(token, "app.create", map[string]any{"name": "  Hello app  ", "slug": "hello"}))
	id := app["id"].(string)
	want := map[string]any{
		"name": "Hello app", "slug": "hello", "owner": user.Id, "owner_email": "user@example.test",
		"nib_app_id": "d-" + id, "volume_id": "d-" + id + "-vol", "hostname": "hello." + droptest.AppDomain,
		"desired_state": "on-request", "idle_timeout_ms": 300000.0, "vcpu": 1.0, "memory_mib": 512.0, "disk_mib": 1024.0,
		"http_port": 8080.0, "executable_name": "server", "health_kind": "tcp", "health_path": "",
		"deployment_seq": 1.0, "deployment_id": "d-" + id + "-dep-1", "archived": false,
	}
	for k, v := range want {
		if app[k] != v {
			t.Errorf("%s = %v, want %v", k, app[k], v)
		}
	}
	if len(app["args"].([]any)) != 0 || len(app["env"].(map[string]any)) != 0 {
		t.Errorf("args %v env %v", app["args"], app["env"])
	}
	if e := events(t, h, "app.create"); len(e) != 1 || e[0].GetString("app") != id || e[0].GetString("actor") != user.Id {
		t.Fatalf("events %v", e)
	}
	full := appOf(t, h.MustOp(token, "app.create", map[string]any{
		"name": "Full", "slug": "full-app", "desired_state": "running", "idle_timeout_ms": 60000, "vcpu": 2, "memory_mib": 4096,
		"disk_mib": 8192, "http_port": 1, "executable_name": "crew", "args": []string{"serve", "--port", "1"},
		"env": map[string]string{"WELCOME_TEXT": "hi", "URL": "https://${NIBRUN_HOSTNAME}:${NIBRUN_HTTP_PORT}/"}, "health_kind": "http",
	}))
	if full["health_path"] != "/" || full["desired_state"] != "running" || num(full["disk_mib"]) != 8192 || len(full["args"].([]any)) != 3 {
		t.Fatalf("%v", full)
	}
	// By slug or id, for the owner.
	if got := appOf(t, h.MustOp(token, "app.get", map[string]any{"app": "full-app"})); got["id"] != full["id"] || num(got["http_port"]) != 1 {
		t.Fatalf("app.get by slug: %v", got)
	}
}

// TestAppValidation: the server refuses what the node's renderer would.
func TestAppValidation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("app-owner@example.test", "user")
	base := func(extra map[string]any) map[string]any {
		in := map[string]any{"name": "App", "slug": "valid-app"}
		for k, v := range extra {
			in[k] = v
		}
		return in
	}
	for _, c := range []struct {
		field string
		in    map[string]any
	}{
		{"name", base(map[string]any{"name": "  "})},
		{"name", base(map[string]any{"name": strings.Repeat("x", 81)})},
		{"name", base(map[string]any{"name": "bad\x07name"})},
		{"slug", base(map[string]any{"slug": "ab"})},
		{"slug", base(map[string]any{"slug": "Has-Caps"})},
		{"slug", base(map[string]any{"slug": "ends-"})},
		{"slug", base(map[string]any{"slug": "drop-dev"})},
		{"slug", base(map[string]any{"slug": "www"})},
		{"desired_state", base(map[string]any{"desired_state": "absent"})},
		{"idle_timeout_ms", base(map[string]any{"idle_timeout_ms": 59999})},
		{"idle_timeout_ms", base(map[string]any{"idle_timeout_ms": 86400001})},
		{"vcpu", base(map[string]any{"vcpu": 3})},
		{"vcpu", base(map[string]any{"vcpu": 0})},
		{"memory_mib", base(map[string]any{"memory_mib": 127})},
		{"memory_mib", base(map[string]any{"memory_mib": 4097})},
		{"disk_mib", base(map[string]any{"disk_mib": 255})},
		{"disk_mib", base(map[string]any{"disk_mib": 16385})},
		{"http_port", base(map[string]any{"http_port": 0})},
		{"http_port", base(map[string]any{"http_port": 65536})},
		{"executable_name", base(map[string]any{"executable_name": "../bin/sh"})},
		{"executable_name", base(map[string]any{"executable_name": "Server"})},
		{"args", base(map[string]any{"args": make([]string, 65)})},
		{"args", base(map[string]any{"args": []string{strings.Repeat("a", 4097)}})},
		{"args", base(map[string]any{"args": []string{"a\x00b"}})},
		{"health_kind", base(map[string]any{"health_kind": "grpc"})},
		{"health_path", base(map[string]any{"health_path": "/up"})},
		{"health_path", base(map[string]any{"health_kind": "http", "health_path": "no-slash"})},
		{"health_path", base(map[string]any{"health_kind": "http", "health_path": "/has space"})},
		{"env", base(map[string]any{"env": map[string]string{"lower": "x"}})},
		{"env", base(map[string]any{"env": map[string]string{"HOME": "/root"}})},
		{"env", base(map[string]any{"env": map[string]string{"PATH": "/bin"}})},
		{"env", base(map[string]any{"env": map[string]string{"NIBRUN_HOSTNAME": "x"}})},
		{"env", base(map[string]any{"env": map[string]string{"DROP_TOKEN": "x"}})},
		{"env", base(map[string]any{"env": map[string]string{"A": "${NIBRUN_DATA_DIR}"}})},
		{"env", base(map[string]any{"env": map[string]string{"A": strings.Repeat("v", 4097)}})},
		{"env", base(map[string]any{"env": map[string]string{"A": "nul\x00"}})},
	} {
		r := h.Op(token, "app.create", c.in)
		wantError(t, r, 400, "invalid_input")
		if field, _ := r.Error["data"].(map[string]any)["field"].(string); field != c.field {
			t.Errorf("%v: field %q, want %q", c.in, field, c.field)
		}
	}
	many := map[string]string{}
	for i := 0; i < 65; i++ {
		many["V"+strings.Repeat("X", i)] = "x"
	}
	wantError(t, h.Op(token, "app.create", base(map[string]any{"env": many})), 400, "invalid_input")
	wantError(t, h.Op(token, "app.create", base(map[string]any{"colour": "red"})), 400, "invalid_input")
	h.MustOp(token, "app.create", base(nil))
	for _, port := range []int{0, 65536} {
		wantError(t, h.Op(token, "app.set", map[string]any{"app": "valid-app", "http_port": port}), 400, "invalid_input")
	}
	wantError(t, h.Op(token, "app.create", base(nil)), 409, "slug_taken")
	wantError(t, h.Op(token, "app.set", map[string]any{"app": "valid-app", "slug": "renamed"}), 400, "invalid_input")
	wantError(t, h.Op(token, "app.set", map[string]any{"app": "valid-app"}), 400, "invalid_input")
	wantError(t, h.Op(token, "app.set", map[string]any{"app": "valid-app", "env": map[string]any{"set": map[string]string{"A": "1"}, "unset": []string{"A"}}}), 400, "invalid_input")
	if n := len(events(t, h, "app.create")); n != 1 {
		t.Fatalf("refused calls wrote events: %d", n)
	}
}

func TestRetainedDisk(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("disk@example.test", "user")
	add := func(slug string, disk int) map[string]any {
		return map[string]any{"name": slug, "slug": slug, "disk_mib": disk}
	}
	h.MustOp(token, "app.create", add("large", 8192))
	h.MustOp(token, "app.archive", map[string]any{"app": "large"})
	h.MustOp(token, "app.create", add("small", 2048))
	wantError(t, h.Op(token, "app.create", add("extra", 1024)), 409, "quota_exceeded")
	wantError(t, h.Op(token, "app.set", map[string]any{"app": "small", "disk_mib": 4096}), 409, "quota_exceeded")
	h.MustOp(token, "app.restore", map[string]any{"app": "large"})
	if q := h.MustOp(token, "app.list", nil)["quota"].(map[string]any); num(q["used"]) != 10240 || num(q["limit"]) != 10240 {
		t.Fatalf("quota %v", q)
	}
}

// TestDeploymentBumps: a change to the instance configuration gives a new
// deployment; the name, start, stop and the disk do not.
func TestDeploymentBumps(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("user@example.test", "user")
	h.MustOp(token, "app.create", map[string]any{"name": "Bump", "slug": "bump", "env": map[string]string{"A": "1"}, "args": []string{"x"}})
	seq := 1
	for _, c := range []struct {
		change map[string]any
		bump   bool
	}{
		{map[string]any{"name": "Renamed"}, false},
		{map[string]any{"disk_mib": 2048}, false},
		{map[string]any{"desired_state": "stopped"}, false},
		{map[string]any{"desired_state": "on-request"}, false}, // back to the mode it had
		{map[string]any{"desired_state": "running"}, true},     // the activation changes
		{map[string]any{"idle_timeout_ms": 600000}, true},
		{map[string]any{"vcpu": 2}, true},
		{map[string]any{"vcpu": 2}, false}, // same value
		{map[string]any{"memory_mib": 1024}, true},
		{map[string]any{"http_port": 80}, true},
		{map[string]any{"http_port": 65535}, true},
		{map[string]any{"executable_name": "crew"}, true},
		{map[string]any{"args": []string{"x"}}, false},
		{map[string]any{"args": []string{"y"}}, true},
		{map[string]any{"args": []string{}}, true},
		{map[string]any{"env": map[string]any{"set": map[string]string{"A": "1"}}}, false},
		{map[string]any{"env": map[string]any{"set": map[string]string{"B": "2"}}}, true},
		{map[string]any{"env": map[string]any{"unset": []string{"NOT_SET"}}}, false},
		{map[string]any{"env": map[string]any{"unset": []string{"B"}}}, true},
		{map[string]any{"health_kind": "http"}, true},
		{map[string]any{"health_path": "/up"}, true},
		{map[string]any{"health_kind": "tcp"}, true},
	} {
		in := map[string]any{"app": "bump"}
		for k, v := range c.change {
			in[k] = v
		}
		r := h.MustOp(token, "app.set", in)
		if c.bump {
			seq++
		}
		app := appOf(t, r)
		if r["deployment_bumped"] != c.bump || num(app["deployment_seq"]) != seq || !strings.HasSuffix(app["deployment_id"].(string), "-dep-"+strconv.Itoa(seq)) {
			t.Fatalf("%v: bumped %v, seq %v, want %v and %d", c.change, r["deployment_bumped"], app["deployment_seq"], c.bump, seq)
		}
	}
	for _, op := range []string{"app.stop", "app.start"} {
		if app := appOf(t, h.MustOp(token, op, map[string]any{"app": "bump"})); num(app["deployment_seq"]) != seq {
			t.Fatalf("%s bumped the deployment", op)
		}
	}
	e := events(t, h, "app.set")
	detail := map[string]any{}
	_ = e[len(e)-1].UnmarshalJSONField("detail", &detail)
	if detail["deployment_bumped"] != true || num(detail["deployment_seq"]) != seq {
		t.Fatalf("event detail %v", detail)
	}
}

// TestStartStopKeepsTheMode: app.start returns a stopped app to the state it
// had before it stopped.
func TestStartStopKeepsTheMode(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("user@example.test", "user")
	h.MustOp(token, "app.create", map[string]any{"name": "Mode", "slug": "mode", "desired_state": "running"})
	state := func(op string) any {
		return appOf(t, h.MustOp(token, op, map[string]any{"app": "mode"}))["desired_state"]
	}
	if state("app.stop") != "stopped" || state("app.start") != "running" {
		t.Fatal("start did not return to running")
	}
	h.MustOp(token, "app.set", map[string]any{"app": "mode", "desired_state": "on-request"})
	if state("app.stop") != "stopped" || state("app.start") != "on-request" {
		t.Fatal("start did not return to on-request")
	}
	created := appOf(t, h.MustOp(token, "app.create", map[string]any{"name": "Off", "slug": "off-app", "desired_state": "stopped"}))
	if created["desired_state"] != "stopped" {
		t.Fatal(created)
	}
	if appOf(t, h.MustOp(token, "app.start", map[string]any{"app": "off-app"}))["desired_state"] != "on-request" {
		t.Fatal("an app created stopped starts on request")
	}
}

// TestSecretOperations: values go to the secret store at the plan's
// parameter, the database keeps metadata only, every change bumps the
// deployment, and names follow the renderer's rules.
func TestSecretOperations(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	user, token := h.User("user@example.test", "user")
	app := appOf(t, h.MustOp(token, "app.create", map[string]any{"name": "Secret", "slug": "secret-app", "env": map[string]string{"PLAIN": "x"}}))
	nib := app["nib_app_id"].(string)
	parameter := "/drop/apps/" + nib[2:] + "/API_KEY"
	set := h.MustOp(token, "secret.set", map[string]any{"app": "secret-app", "name": "API_KEY", "value": "first value"})
	secret := set["secret"].(map[string]any)
	if secret["name"] != "API_KEY" || num(secret["version"]) != 1 || num(secret["length"]) != 11 || secret["set_via"] != "ui" ||
		secret["updated_by"] != user.Id || set["deployment_bumped"] != true || set["deployment_id"] != nib+"-dep-2" {
		t.Fatalf("%v", set)
	}
	if v, err := h.Secrets.Get(context.Background(), parameter, 1); err != nil || v != "first value" {
		t.Fatalf("store %q %v", v, err)
	}
	again := h.MustOp(token, "secret.set", map[string]any{"app": "secret-app", "name": "API_KEY", "value": "second"})
	if num(again["secret"].(map[string]any)["version"]) != 2 || again["deployment_id"] != nib+"-dep-3" {
		t.Fatalf("%v", again)
	}
	if v, _ := h.Secrets.Get(context.Background(), parameter, 2); v != "second" {
		t.Fatalf("version 2 = %q", v)
	}
	listed := h.MustOp(token, "secret.list", map[string]any{"app": "secret-app"})["secrets"].([]any)
	if len(listed) != 1 || num(listed[0].(map[string]any)["version"]) != 2 {
		t.Fatalf("%v", listed)
	}
	if detail := h.MustOp(token, "app.get", map[string]any{"app": "secret-app"}); len(detail["secrets"].([]any)) != 1 {
		t.Fatalf("%v", detail)
	}
	for _, c := range []struct {
		name, value, field string
	}{
		{"lower", "x", "name"}, {"HOME", "x", "name"}, {"NIBRUN_X", "x", "name"}, {"DROP_X", "x", "name"}, {"PATH", "x", "name"},
		{"PLAIN", "x", "name"}, {"OK", "", "value"}, {"OK", strings.Repeat("v", 4097), "value"}, {"OK", "${NIBRUN_OTHER}", "value"},
	} {
		r := h.Op(token, "secret.set", map[string]any{"app": "secret-app", "name": c.name, "value": c.value})
		wantError(t, r, 400, "invalid_input")
		if field := r.Error["data"].(map[string]any)["field"]; field != c.field {
			t.Errorf("%s: field %v", c.name, field)
		}
	}
	wantError(t, h.Op(token, "app.set", map[string]any{"app": "secret-app", "env": map[string]any{"set": map[string]string{"API_KEY": "plain"}}}), 400, "invalid_input")
	// An idempotency key is ignored: no hash of a value is stored.
	h.MustOp(token, "secret.set", map[string]any{"app": "secret-app", "name": "OTHER", "value": "v"})
	if r := h.Op(token, "secret.set", map[string]any{"app": "secret-app", "name": "OTHER", "value": "w"}, "key-1"); r.Status != 200 || r.Replayed {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if n, _ := h.App.CountRecords("op_keys"); n != 0 {
		t.Fatalf("secret.set stored %d idempotency keys", n)
	}
	deleted := h.MustOp(token, "secret.delete", map[string]any{"app": "secret-app", "name": "API_KEY"})
	if deleted["deleted"] != "API_KEY" || deleted["deployment_bumped"] != true {
		t.Fatalf("%v", deleted)
	}
	if _, err := h.Secrets.Get(context.Background(), parameter, 2); err == nil {
		t.Fatal("secret.delete left the parameter")
	}
	wantError(t, h.Op(token, "secret.delete", map[string]any{"app": "secret-app", "name": "API_KEY"}), 404, "not_found")
	if n := len(events(t, h, "secret.set")) + len(events(t, h, "secret.delete")); n != 5 {
		t.Fatalf("%d secret events", n)
	}
}

// TestSecretStoreUnavailable: without a store secret writes answer 503, and a
// failing store answers 502 and changes nothing.
func TestSecretStoreUnavailable(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{NoSecrets: true})
	_, token := h.User("app-owner@example.test", "user")
	h.MustOp(token, "app.create", map[string]any{"name": "App", "slug": "no-store"})
	wantError(t, h.Op(token, "secret.set", map[string]any{"app": "no-store", "name": "A", "value": "v"}), 503, "secrets_unavailable")

	failing := droptest.NewHosted(t, droptest.Options{Secrets: failingStore{}})
	_, token = failing.User("app-owner@example.test", "user")
	failing.MustOp(token, "app.create", map[string]any{"name": "App", "slug": "failing"})
	wantError(t, failing.Op(token, "secret.set", map[string]any{"app": "failing", "name": "A", "value": "v"}), 502, "secret_store_failed")
	if n, _ := failing.App.CountRecords("app_secret_names"); n != 0 {
		t.Fatal("a failed store call left a secret name")
	}
	if app := appOf(t, failing.MustOp(token, "app.get", map[string]any{"app": "failing"})); num(app["deployment_seq"]) != 1 {
		t.Fatal("a failed store call bumped the deployment")
	}
}
