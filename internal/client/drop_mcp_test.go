package client

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
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/logger"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droppolicy"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/leakscan"
)

// The operations the M6 ticket (decision 11) puts in drop_write, by role.
// Those not registered yet (releases in M5b, nodes and placements in M7) are
// expected once they are: TestMCPWriteOpsFollowTicket then asks for MCP: true
// on their specs.
var (
	ticketUserWrites  = []string{"workspace.create", "workspace.resize", "workspace.upgrade", "app.set", "app.start", "app.stop", "secret.set", "secret.delete"}
	ticketAdminWrites = []string{"invite.create", "invite.revoke", "user.set", "node.create", "node.rotate_secret", "node.set_state", "placement.set", "placement.clear"}
	ticketUserReads   = []string{"me", "apps", "app", "releases", "secrets", "status", "usage", "events", "catalog", "shared"} // usage: owner decision 6 of S3 metering (Phase 1)
	ticketAdminReads  = []string{"users", "invites", "nodes"}
)

// mcpTool is one entry of tools/list.
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	} `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint    bool  `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
	} `json:"annotations"`
}

func listTools(t *testing.T, h *droptest.Hosted, bearer string) []mcpTool {
	t.Helper()
	w := h.MCP(bearer, "tools/list", map[string]any{})
	var out struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Error != nil {
		t.Fatalf("tools/list: %d %s", w.Code, w.Body)
	}
	return out.Result.Tools
}

// toolAnswer is one tools/call answer: the raw body, whether the tool failed
// and the decoded text content.
type toolAnswer struct {
	Raw     string
	IsError bool
	Data    map[string]any
}

func (a toolAnswer) err() map[string]any {
	e, _ := a.Data["error"].(map[string]any)
	return e
}

func callTool(t *testing.T, h *droptest.Hosted, bearer, name string, args any) toolAnswer {
	t.Helper()
	w := h.MCP(bearer, "tools/call", map[string]any{"name": name, "arguments": args})
	answer := toolAnswer{Raw: w.Body.String()}
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("tools/call %s: %d %s", name, w.Code, w.Body)
	}
	if out.Error != nil {
		answer.IsError = true
		return answer
	}
	answer.IsError = out.Result.IsError
	if len(out.Result.Content) == 1 {
		_ = json.Unmarshal([]byte(out.Result.Content[0].Text), &answer.Data)
	}
	return answer
}

// mustTool fails unless the tool succeeds and returns its data.
func mustTool(t *testing.T, h *droptest.Hosted, bearer, name string, args any) map[string]any {
	t.Helper()
	a := callTool(t, h, bearer, name, args)
	if a.IsError || a.Data == nil {
		t.Fatalf("%s %v: %s", name, args, a.Raw)
	}
	return a.Data
}

// wantToolError fails unless the tool answers an error with this status and code.
func wantToolError(t *testing.T, a toolAnswer, status int, code string) {
	t.Helper()
	e := a.err()
	if !a.IsError || e == nil || e["code"] != code || (status != 0 && e["status"] != float64(status)) {
		t.Errorf("want %d %s, got %s", status, code, a.Raw)
	}
}

func hostedMCP(t *testing.T) *droptest.Hosted {
	return droptest.NewHosted(t, droptest.Options{MCPHandler: newMCPHandler})
}

func registeredOnly(names []string) []string {
	var out []string
	for _, n := range names {
		if _, ok := dropapi.Lookup(n); ok {
			out = append(out, n)
		}
	}
	return out
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// TestMCPToolsByRole: tools/list shows each role exactly drop_read and
// drop_write, with that role's enums and the read-only and destructive hints.
func TestMCPToolsByRole(t *testing.T) {
	h := hostedMCP(t)
	_, user := h.User("user@example.test", "user")
	_, admin := h.User("admin@example.test", "admin")
	enums := map[string][2][]string{}
	for name, session := range map[string]string{"user": user, "admin": admin, "owner": h.Token(h.Owner())} {
		role := dropapi.RoleUser
		if name != "user" {
			role = dropapi.RoleAdmin
		}
		tools := listTools(t, h, h.OAuthToken(session))
		if len(tools) != 2 || tools[0].Name != "drop_read" || tools[1].Name != "drop_write" {
			t.Fatalf("%s tools: %+v", name, tools)
		}
		read, write := tools[0], tools[1]
		var whats []string
		for _, r := range dropReadsFor(role) {
			whats = append(whats, r.What)
		}
		if got := read.InputSchema.Properties["what"].Enum; !slices.Equal(got, whats) {
			t.Errorf("%s what enum %v, want %v", name, got, whats)
		}
		if got := write.InputSchema.Properties["op"].Enum; !slices.Equal(got, dropapi.MCPWriteNames(role)) {
			t.Errorf("%s op enum %v", name, got)
		}
		if !read.Annotations.ReadOnlyHint || read.Annotations.DestructiveHint == nil || *read.Annotations.DestructiveHint {
			t.Errorf("%s drop_read hints %+v", name, read.Annotations)
		}
		if write.Annotations.ReadOnlyHint || write.Annotations.DestructiveHint == nil || !*write.Annotations.DestructiveHint {
			t.Errorf("%s drop_write hints %+v", name, write.Annotations)
		}
		if write.Description != dropWriteDescription(dropapi.MCPWriteNames(role)) || read.Description != dropReadDescription(role) {
			t.Errorf("%s descriptions differ from the generated ones", name)
		}
		if !strings.Contains(write.Description, "passes through this conversation") || !strings.Contains(write.Description, "reader") {
			t.Errorf("%s drop_write lacks the secret warning", name)
		}
		enums[name] = [2][]string{read.InputSchema.Properties["what"].Enum, write.InputSchema.Properties["op"].Enum}
	}
	if reflect.DeepEqual(enums["user"], enums["admin"]) || !reflect.DeepEqual(enums["admin"], enums["owner"]) {
		t.Errorf("enums by role: %v", enums)
	}
	for _, op := range []string{"invite.create", "user.set"} {
		if slices.Contains(enums["user"][1], op) || !slices.Contains(enums["admin"][1], op) {
			t.Errorf("%s in the wrong enums", op)
		}
	}
	for _, what := range []string{"users", "invites"} {
		if slices.Contains(enums["user"][0], what) || !slices.Contains(enums["admin"][0], what) {
			t.Errorf("what=%s in the wrong enums", what)
		}
	}
	// An unlisted tool is refused.
	if a := callTool(t, h, h.OAuthToken(user), "query", map[string]any{"sql": "SELECT 1"}); !a.IsError {
		t.Errorf("an unlisted tool answered: %s", a.Raw)
	}
}

// TestMCPWriteTableMatchesRegistry: drop_write's op enum and op table are
// exactly the registered operations with MCP set that the role may call,
// each row generated from its spec, and nothing else.
func TestMCPWriteTableMatchesRegistry(t *testing.T) {
	for _, role := range []dropapi.Role{dropapi.RoleUser, dropapi.RoleDeveloper, dropapi.RoleAdmin} {
		var want []string
		for _, s := range dropapi.Specs() {
			if s.MCP && s.Allows(role) {
				if s.Read {
					t.Errorf("%s is a read with MCP set", s.Name)
				}
				want = append(want, s.Name)
			}
		}
		if got := dropapi.MCPWriteNames(role); !slices.Equal(got, want) || len(got) == 0 {
			t.Fatalf("%s enum %v, want %v", role, got, want)
		}
		description := dropWriteDescription(dropapi.MCPWriteNames(role))
		table := description[strings.Index(description, "\nOperations:\n")+len("\nOperations:\n"):]
		var rows []string
		for _, line := range strings.Split(table, "\n") {
			rows = append(rows, strings.Fields(strings.TrimPrefix(line, "- "))[0])
		}
		if !slices.Equal(rows, want) {
			t.Errorf("%s table rows %v, want %v", role, rows, want)
		}
		for _, name := range want {
			spec, _ := dropapi.Lookup(name)
			if !strings.Contains(table, opTableRow(spec)) || !strings.Contains(opTableRow(spec), spec.Summary) {
				t.Errorf("%s: row for %s is not generated from its spec", role, name)
			}
		}
		for _, s := range dropapi.Specs() {
			if !slices.Contains(want, s.Name) && strings.Contains(table, "- "+s.Name+" ") {
				t.Errorf("%s table lists %s", role, s.Name)
			}
		}
	}
	if slices.Equal(dropapi.MCPWriteNames(dropapi.RoleUser), dropapi.MCPWriteNames(dropapi.RoleAdmin)) {
		t.Error("the op enums do not differ by role")
	}
	// The generated signature follows the input type.
	spec, _ := dropapi.Lookup("app.set")
	if got := opInputSignature(spec); !strings.HasPrefix(got, "{app, name?,") || !strings.Contains(got, "env?: {set?, unset?}") {
		t.Errorf("app.set signature %s", got)
	}
}

// TestMCPWriteOpsFollowTicket: drop_write offers exactly the ticket's
// operations that are registered. An operation from the list registered
// without MCP: true (or one added with it) fails here.
func TestMCPWriteOpsFollowTicket(t *testing.T) {
	user := registeredOnly(ticketUserWrites)
	admin := registeredOnly(ticketAdminWrites)
	if got := dropapi.MCPWriteNames(dropapi.RoleUser); !slices.Equal(sortedCopy(got), sortedCopy(user)) {
		t.Errorf("user ops %v, want %v (set MCP: true on the spec)", got, user)
	}
	if got := dropapi.MCPWriteNames(dropapi.RoleAdmin); !slices.Equal(sortedCopy(got), sortedCopy(admin)) {
		t.Errorf("admin ops %v, want %v (set MCP: true on the spec)", got, admin)
	}
	var user2, admin2 []string
	for _, r := range dropReadsFor(dropapi.RoleUser) {
		user2 = append(user2, r.What)
	}
	for _, r := range dropReadsFor(dropapi.RoleAdmin) {
		admin2 = append(admin2, r.What)
	}
	if !slices.Equal(user2, ticketUserReads) || !slices.Equal(admin2, append(append([]string(nil), ticketUserReads...), ticketAdminReads...)) {
		t.Errorf("what values %v / %v", user2, admin2)
	}
}

// TestDropReadTable: every registered operation behind a `what` is a read of
// the listed role; the others answer "not available yet".
func TestDropReadTable(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range dropReads {
		if seen[r.What] {
			t.Errorf("what=%s twice", r.What)
		}
		seen[r.What] = true
		spec, registered := dropapi.Lookup(r.Op)
		if !registered {
			if !strings.Contains(dropReadDescription(dropapi.RoleAdmin), "- "+r.What+": "+r.Help) || !strings.Contains(dropReadDescription(dropapi.RoleAdmin), r.Help+" (not available yet)") && !strings.Contains(dropReadDescription(dropapi.RoleAdmin), r.Help+" (admin) (not available yet)") {
				t.Errorf("what=%s is not marked unavailable", r.What)
			}
			continue
		}
		if !spec.Read || spec.Role != r.Role {
			t.Errorf("what=%s: %s is read=%v role=%s, want a read of role %s", r.What, r.Op, spec.Read, spec.Role, r.Role)
		}
	}
	for _, op := range []string{"me", "app.list", "app.get", "secret.list", "event.list", "user.list", "invite.list"} {
		found := false
		for _, r := range dropReads {
			found = found || r.Op == op
		}
		if _, ok := dropapi.Lookup(op); !ok || !found {
			t.Errorf("read %s is not behind a what", op)
		}
	}
	if !slices.Equal(droppolicy.Observe.Tools, []string{"drop_read", "drop_write"}) || !slices.Equal(droppolicy.Observe.APIRoutes, []string{dropapi.Route}) {
		t.Errorf("observe policy %+v", droppolicy.Observe)
	}
}

// TestMCPReadsAndWrites: the user flow through both tools.
func TestMCPReadsAndWrites(t *testing.T) {
	h := hostedMCP(t)
	_, session := h.User("user@example.test", "developer")
	bearer := h.OAuthToken(session)
	me := mustTool(t, h, bearer, "drop_read", map[string]any{"what": "me"})
	if me["email"] != "user@example.test" || me["admin"] != false {
		t.Fatalf("me %v", me)
	}
	created := mustTool(t, h, bearer, "drop_write", map[string]any{"op": "app.create", "input": map[string]any{"name": "Hello", "slug": "hello-app"}, "idempotency_key": "create-1"})
	if created["op"] != "app.create" || dig(created, "result", "app", "slug") != "hello-app" {
		t.Fatalf("app.create %v", created)
	}
	replay := mustTool(t, h, bearer, "drop_write", map[string]any{"op": "app.create", "input": map[string]any{"name": "Hello", "slug": "hello-app"}, "idempotency_key": "create-1"})
	if replay["replayed"] != true {
		t.Errorf("replay %v", replay)
	}
	set := mustTool(t, h, bearer, "drop_write", map[string]any{"op": "app.set", "input": map[string]any{"app": "hello-app", "env": map[string]any{"set": map[string]any{"LOG_LEVEL": "debug"}}}})
	if dig(set, "result", "deployment_bumped") != true {
		t.Errorf("app.set %v", set)
	}
	apps := mustTool(t, h, bearer, "drop_read", map[string]any{"what": "apps", "limit": 10})
	if list, _ := apps["apps"].([]any); len(list) != 1 {
		t.Fatalf("apps %v", apps)
	}
	app := mustTool(t, h, bearer, "drop_read", map[string]any{"what": "app", "app": "hello-app"})
	if dig(app, "app", "env", "LOG_LEVEL") != "debug" {
		t.Errorf("app %v", app)
	}
	events := mustTool(t, h, bearer, "drop_read", map[string]any{"what": "events", "app": "hello-app"})
	if list, _ := events["events"].([]any); len(list) != 2 || dig(list[0], "action") != "app.set" || dig(list[0], "actor_email") != "user@example.test" {
		t.Errorf("events %v", events)
	}
	mustTool(t, h, bearer, "drop_write", map[string]any{"op": "app.stop", "input": map[string]any{"app": "hello-app"}})

	// Argument checks answer without reaching the server.
	for args, code := range map[string]string{
		`{"what":"apps","app":"hello-app"}`: "invalid_arguments", // app.list takes no app
		`{"what":"secrets"}`:                "invalid_arguments", // secret.list needs app
		`{"what":"nope"}`:                   "invalid_arguments",
		`{"what":"me","extra":1}`:           "invalid_arguments",
	} {
		var a map[string]any
		_ = json.Unmarshal([]byte(args), &a)
		wantToolError(t, callTool(t, h, bearer, "drop_read", a), 0, code)
	}
	for _, r := range dropReads {
		if _, ok := r.readSpec(); !ok && r.Role == dropapi.RoleUser {
			wantToolError(t, callTool(t, h, bearer, "drop_read", map[string]any{"what": r.What, "app": "hello-app"}), 0, "not_available")
		}
	}
	for _, args := range []map[string]any{
		{"op": "app.list"}, // a read
		{"op": "app.archive", "input": map[string]any{"app": "hello-app"}}, // not offered through MCP
		{"op": "nope"},
		{"op": "app.stop", "input": "hello-app"},
		{"op": "app.stop", "input": map[string]any{"app": "hello-app"}, "idempotency_key": "has space"},
	} {
		wantToolError(t, callTool(t, h, bearer, "drop_write", args), 0, "invalid_arguments")
	}
	// The server's own refusals come back with their status and code.
	wantToolError(t, callTool(t, h, bearer, "drop_write", map[string]any{"op": "app.create", "input": map[string]any{"name": "Bad", "slug": "B"}}), 400, "invalid_input")
	wantToolError(t, callTool(t, h, bearer, "drop_write", map[string]any{"op": "app.set", "input": map[string]any{"app": "hello-app", "bogus": 1}}), 400, "invalid_input")
}

// TestMCPAdminOpAsUserForbidden: a user who forces an admin operation or an
// admin read past the user server's enums gets 403 from the server.
func TestMCPAdminOpAsUserForbidden(t *testing.T) {
	h := hostedMCP(t)
	_, session := h.User("user@example.test", "user")
	bearer := h.OAuthToken(session)
	invite := h.Invite("someone@example.test", "user")
	for op, input := range map[string]map[string]any{
		"invite.create": {"email": "new@example.test"},
		"invite.revoke": {"invite": invite},
		"user.set":      {"user": "user@example.test", "role": "admin"},
	} {
		wantToolError(t, callTool(t, h, bearer, "drop_write", map[string]any{"op": op, "input": input}), 403, "forbidden")
	}
	for _, args := range []map[string]any{{"what": "users"}, {"what": "invites"}, {"what": "apps", "all": true}, {"what": "events", "all": true}} {
		wantToolError(t, callTool(t, h, bearer, "drop_read", args), 403, "forbidden")
	}
	if me := h.MustOp(session, "me", nil); me["role"] != "user" {
		t.Fatalf("the user became %v", me["role"])
	}
	if n, _ := h.App.CountRecords("invites", dbx.HashExp{"email_lower": "new@example.test"}); n != 0 {
		t.Fatal("a user created an invite")
	}
	// The same calls work for an admin.
	owner := h.OAuthToken(h.Token(h.Owner()))
	created := mustTool(t, h, owner, "drop_write", map[string]any{"op": "invite.create", "input": map[string]any{"email": "new@example.test"}})
	if url, _ := dig(created, "result", "sign_in_url").(string); url == "" {
		t.Errorf("invite.create %v", created)
	}
	if users := mustTool(t, h, owner, "drop_read", map[string]any{"what": "users"}); len(users["users"].([]any)) != 2 {
		t.Errorf("users %v", users)
	}
}

// TestMCPIsolation (plan section 9, MCP): users A and B and the owner O, an
// admin, through OAuth grants. Each account reads and changes only its own
// apps, secrets and events through the tools; another account's app is a 404;
// admins read everything and change only their own.
func TestMCPIsolation(t *testing.T) {
	h := hostedMCP(t)
	_, sessionA := h.User("a@example.test", "developer")
	_, sessionB := h.User("b@example.test", "developer")
	a, b, o := h.OAuthToken(sessionA), h.OAuthToken(sessionB), h.OAuthToken(h.Token(h.Owner()))
	appA := dig(mustTool(t, h, a, "drop_write", map[string]any{"op": "app.create", "input": map[string]any{"name": "A", "slug": "app-of-a"}}), "result", "app", "id").(string)
	appB := dig(mustTool(t, h, b, "drop_write", map[string]any{"op": "app.create", "input": map[string]any{"name": "B", "slug": "app-of-b"}}), "result", "app", "id").(string)
	mustTool(t, h, a, "drop_write", map[string]any{"op": "secret.set", "input": map[string]any{"app": appA, "name": "TOKEN", "value": "synthetic"}})
	mustTool(t, h, b, "drop_write", map[string]any{"op": "secret.set", "input": map[string]any{"app": appB, "name": "TOKEN", "value": "synthetic"}})

	// Lists: own apps and events only; admins everything with all.
	for bearer, want := range map[string]string{a: appA, b: appB} {
		apps := mustTool(t, h, bearer, "drop_read", map[string]any{"what": "apps"})["apps"].([]any)
		if len(apps) != 1 || dig(apps[0], "id") != want {
			t.Errorf("apps %v, want only %s", apps, want)
		}
		events := mustTool(t, h, bearer, "drop_read", map[string]any{"what": "events"})["events"].([]any)
		if len(events) != 2 {
			t.Errorf("events %v", events)
		}
		for _, e := range events {
			if dig(e, "app") != want {
				t.Errorf("another account's event: %v", e)
			}
		}
	}
	if all := mustTool(t, h, o, "drop_read", map[string]any{"what": "apps", "all": true})["apps"].([]any); len(all) != 2 {
		t.Errorf("admin apps %v", all)
	}
	if all := mustTool(t, h, o, "drop_read", map[string]any{"what": "events", "all": true})["events"].([]any); len(all) < 4 {
		t.Errorf("admin events %v", all)
	}

	// Every read and write on B's app, by id and by slug: 404 for A.
	for _, ref := range []string{appB, "app-of-b"} {
		for _, what := range []string{"app", "secrets", "events", "usage"} {
			wantToolError(t, callTool(t, h, a, "drop_read", map[string]any{"what": what, "app": ref}), 404, "not_found")
		}
		for op, input := range map[string]map[string]any{
			"app.set": {"name": "Hijacked"}, "app.start": {}, "app.stop": {},
			"secret.set": {"name": "TOKEN", "value": "x"}, "secret.delete": {"name": "TOKEN"},
		} {
			input["app"] = ref
			wantToolError(t, callTool(t, h, a, "drop_write", map[string]any{"op": op, "input": input}), 404, "not_found")
		}
	}
	// Usage: A reads its own; admins read A's.
	if got := mustTool(t, h, a, "drop_read", map[string]any{"what": "usage", "app": "app-of-a"}); dig(got, "app") != appA {
		t.Errorf("own usage %v", got)
	}
	if got := mustTool(t, h, o, "drop_read", map[string]any{"what": "usage", "app": "app-of-a"}); dig(got, "app") != appA {
		t.Errorf("admin usage read %v", got)
	}
	// Admins read A's app and secret names but change only their own.
	if got := mustTool(t, h, o, "drop_read", map[string]any{"what": "app", "app": "app-of-a"}); dig(got, "app", "id") != appA {
		t.Errorf("admin app read %v", got)
	}
	if s := mustTool(t, h, o, "drop_read", map[string]any{"what": "secrets", "app": appA})["secrets"].([]any); len(s) != 1 {
		t.Errorf("admin secret names %v", s)
	}
	wantToolError(t, callTool(t, h, o, "drop_write", map[string]any{"op": "app.set", "input": map[string]any{"app": appA, "name": "Admin was here"}}), 403, "forbidden")
	wantToolError(t, callTool(t, h, o, "drop_write", map[string]any{"op": "secret.delete", "input": map[string]any{"app": appA, "name": "TOKEN"}}), 403, "forbidden")
	for _, id := range []string{appA, appB} {
		record, _ := h.App.FindRecordById("apps", id)
		if name := record.GetString("name"); name != "A" && name != "B" || record.GetString("desired_state") != "on-request" {
			t.Errorf("app %s changed: %s %s", id, name, record.GetString("desired_state"))
		}
	}
	if n, _ := h.App.CountRecords("app_secret_names"); n != 2 {
		t.Errorf("%d secret names", n)
	}
	// Releases, status, upload links and deploys on B's app: 404 for A.
	link := mustTool(t, h, b, "drop_write", map[string]any{"op": "release.upload_link", "input": map[string]any{"app": appB, "activate": true}})
	path, _ := dig(link, "result", "path").(string)
	if w := h.Upload(path, droptest.ELF(4096, 3)); w.Code != http.StatusOK {
		t.Fatalf("B's upload: %d %s", w.Code, w.Body)
	}
	releaseB := dig(mustTool(t, h, b, "drop_read", map[string]any{"what": "releases", "app": appB}), "current_release").(string)
	for _, ref := range []string{appB, "app-of-b"} {
		for _, what := range []string{"releases", "status"} {
			wantToolError(t, callTool(t, h, a, "drop_read", map[string]any{"what": what, "app": ref}), 404, "not_found")
		}
		for op, input := range map[string]map[string]any{
			"release.upload_link": {}, "release.deploy": {"release": releaseB},
		} {
			input["app"] = ref
			wantToolError(t, callTool(t, h, a, "drop_write", map[string]any{"op": op, "input": input}), 404, "not_found")
		}
	}
	wantToolError(t, callTool(t, h, a, "drop_write", map[string]any{"op": "release.deploy", "input": map[string]any{"app": appA, "release": releaseB}}), 404, "not_found")
	// Nodes and placements are admins' only.
	n1, _, nodeToken := h.Node("node-one", "host-mcp-0001")
	wantToolError(t, callTool(t, h, a, "drop_read", map[string]any{"what": "nodes"}), 403, "forbidden")
	for op, input := range map[string]map[string]any{
		"node.create": {"name": "mine", "host_id": "h-2"}, "node.rotate_secret": {"node": n1},
		"node.set_state": {"node": n1, "state": "disabled"}, "placement.set": {"app": appA, "node": n1}, "placement.clear": {"app": appA},
	} {
		wantToolError(t, callTool(t, h, a, "drop_write", map[string]any{"op": op, "input": input}), 403, "forbidden")
	}
	if n, _ := h.App.CountRecords("placements"); n != 0 {
		t.Errorf("%d placements", n)
	}
	// A node token is not an OAuth grant: /mcp refuses it.
	if w := h.MCP("Bearer "+nodeToken, "tools/list", map[string]any{}); w.Code != http.StatusUnauthorized {
		t.Errorf("node token on /mcp: %d %s", w.Code, w.Body)
	}
	// One grant is one account: A's token never acts as B or O.
	if me := mustTool(t, h, a, "drop_read", map[string]any{"what": "me"}); me["email"] != "a@example.test" {
		t.Errorf("A's token is %v", me["email"])
	}
}

func TestSuspensionRevokesMCPGrant(t *testing.T) {
	h := hostedMCP(t)
	user, session := h.User("mcp@example.test", "user")
	bearer := h.OAuthToken(session)
	admin := h.Token(h.Owner())
	for _, suspended := range []bool{true, false} {
		h.MustOp(admin, "user.set", map[string]any{"user": user.Id, "suspended": suspended})
		if w := h.MCP(bearer, "tools/list", map[string]any{}); w.Code != http.StatusUnauthorized {
			t.Fatalf("prior MCP grant after suspended=%t: %d", suspended, w.Code)
		}
	}
}

// echoingStore refuses every call with an error that echoes the value.
type echoingStore struct{}

func (echoingStore) Put(_ context.Context, name, value string) (int64, error) {
	return 0, fmt.Errorf("store refused %s = %s", name, value)
}
func (echoingStore) Delete(context.Context, string) error { return fmt.Errorf("store refused delete") }

func dumpDB(t *testing.T, db dbx.Builder) string {
	t.Helper()
	var tables []string
	if err := db.NewQuery("SELECT name FROM sqlite_master WHERE type = 'table'").Column(&tables); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, table := range tables {
		rows, err := db.NewQuery("SELECT * FROM `" + table + "`").Rows()
		if err != nil {
			t.Fatal(err)
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
				if b, ok := v.([]byte); ok {
					out.Write(b)
				} else {
					fmt.Fprint(&out, v)
				}
				out.WriteByte('\n')
			}
		}
		rows.Close()
	}
	return out.String()
}

// TestMCPSecretSentinel (decision 9): a secret value set through drop_write
// reaches the secret store, marked set_via mcp, and nothing else: no MCP
// answer (success, refusals, argument errors, a failing store), no observe
// diagnostic, no log line, no database record.
func TestMCPSecretSentinel(t *testing.T) {
	// Diagnostics go to standard error; capture it for the whole test.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = writer
	captured := make(chan string)
	go func() {
		data, _ := io.ReadAll(reader)
		captured <- string(data)
	}()
	restored := false
	restore := func() string {
		if restored {
			return ""
		}
		restored = true
		os.Stderr = stderr
		writer.Close()
		return <-captured
	}
	defer restore()
	var std bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&std)
	defer log.SetOutput(previous)

	h := hostedMCP(t)
	handler, ok := h.App.Logger().Handler().(*logger.BatchHandler)
	if !ok {
		t.Fatalf("logger handler %T", h.App.Logger().Handler())
	}
	handler.SetLevel(slog.Level(-99999))
	random := make([]byte, 12)
	_, _ = rand.Read(random)
	sentinel := "SENTINEL-" + hex.EncodeToString(random)

	_, session := h.User("user@example.test", "developer")
	_, otherSession := h.User("other@example.test", "developer")
	bearer, other := h.OAuthToken(session), h.OAuthToken(otherSession)
	var answers []string
	call := func(bearer, tool string, args any) toolAnswer {
		a := callTool(t, h, bearer, tool, args)
		answers = append(answers, a.Raw)
		return a
	}
	write := func(bearer string, args map[string]any) toolAnswer { return call(bearer, "drop_write", args) }
	setInput := func(name, value string) map[string]any {
		return map[string]any{"app": "sentinel", "name": name, "value": value}
	}
	if a := write(bearer, map[string]any{"op": "app.create", "input": map[string]any{"name": "Sentinel", "slug": "sentinel"}}); a.IsError {
		t.Fatal(a.Raw)
	}
	set := write(bearer, map[string]any{"op": "secret.set", "input": setInput("FROM_MCP", sentinel), "idempotency_key": "set-1"})
	if set.IsError {
		t.Fatal(set.Raw)
	}
	secret, _ := dig(set.Data, "result", "secret").(map[string]any)
	if secret["set_via"] != "mcp" || secret["name"] != "FROM_MCP" || secret["length"] != float64(len(sentinel)) {
		t.Fatalf("secret.set result %v", set.Data)
	}
	if keys := slices.Sorted(func(yield func(string) bool) {
		for k := range secret {
			if !yield(k) {
				return
			}
		}
	}); !slices.Equal(keys, []string{"length", "name", "set_via", "updated"}) {
		t.Errorf("secret.set returns %v", keys)
	}
	// Refusals, by the server and by the tool's own argument checks.
	for _, a := range []toolAnswer{
		write(bearer, map[string]any{"op": "secret.set", "input": setInput("lower", sentinel)}),
		write(bearer, map[string]any{"op": "secret.set", "input": setInput("HOME", sentinel)}),
		write(bearer, map[string]any{"op": "secret.set", "input": setInput("TOO_LONG", sentinel+strings.Repeat("x", 4096))}),
		write(bearer, map[string]any{"op": "secret.set", "input": map[string]any{"app": "sentinel", "name": "X", "value": sentinel, "extra": sentinel}}),
		write(other, map[string]any{"op": "secret.set", "input": setInput("FROM_MCP", sentinel)}),
		write(bearer, map[string]any{"op": sentinel, "input": setInput("FROM_MCP", sentinel)}),
		write(bearer, map[string]any{"op": "secret.set", "input": sentinel}),
		write(bearer, map[string]any{"op": "secret.set", "input": setInput("FROM_MCP", sentinel), "idempotency_key": sentinel + " !"}),
		write(bearer, map[string]any{"op": "secret.set", "input": setInput("FROM_MCP", sentinel), sentinel: sentinel}),
		write(bearer, map[string]any{"op": "invite.create", "input": map[string]any{"email": sentinel}}),
		call(bearer, "drop_read", map[string]any{"what": sentinel}),
		call(bearer, "drop_read", map[string]any{"what": "secrets", "app": sentinel}),
		call(bearer, "drop_read", map[string]any{"what": "me", "value": sentinel}),
		call(bearer, sentinel, map[string]any{"value": sentinel}),
	} {
		if !a.IsError {
			t.Fatalf("accepted: %s", a.Raw)
		}
	}
	// A failing store whose error echoes the value: a generic answer.
	dropserver.SetServices(h.App, dropserver.Services{Secrets: echoingStore{}})
	if a := write(bearer, map[string]any{"op": "secret.set", "input": setInput("FROM_MCP", sentinel)}); a.err()["code"] != dropapi.CodeSecretStoreFailed {
		t.Fatalf("failing store: %s", a.Raw)
	}
	dropserver.SetServices(h.App, dropserver.Services{Secrets: h.Secrets})
	// Every read.
	for _, what := range []string{"secrets", "app", "events"} {
		a := call(bearer, "drop_read", map[string]any{"what": what, "app": "sentinel"})
		if a.IsError {
			t.Fatalf("%s: %s", what, a.Raw)
		}
		if what == "secrets" {
			s := a.Data["secrets"].([]any)[0].(map[string]any)
			if _, has := s["version"]; has || s["set_via"] != "mcp" {
				t.Errorf("what=secrets returns %v", s)
			}
		}
	}
	call(bearer, "drop_read", map[string]any{"what": "apps"})

	// The control: the store holds the value.
	nib := h.MustOp(session, "app.get", map[string]any{"app": "sentinel"})["app"].(map[string]any)["nib_app_id"].(string)
	if v, err := h.Secrets.Get(context.Background(), "/drop/apps/"+nib[2:]+"/FROM_MCP", 1); err != nil || v != sentinel {
		t.Fatalf("the store does not hold the value: %v", err)
	}
	for i, answer := range answers {
		if strings.Contains(answer, sentinel) {
			t.Errorf("answer %d holds the secret: %s", i, answer)
		}
	}
	if records := dumpDB(t, h.App.DB()); strings.Contains(records, sentinel) {
		t.Error("a database record holds the secret")
	} else if !strings.Contains(records, "FROM_MCP") {
		t.Error("the records dump misses the secret name")
	}
	if err := handler.WriteAll(context.WithValue(context.Background(), logger.BlockKey, true)); err != nil {
		t.Fatal(err)
	}
	if logs := dumpDB(t, h.App.AuxDB()); strings.Contains(logs, sentinel) {
		t.Error("a log line holds the secret")
	} else if !strings.Contains(logs, "Drop secret store failed") {
		t.Error("the store failure was not logged")
	}
	if strings.Contains(std.String(), sentinel) {
		t.Error("the standard log holds the secret")
	}
	diagnostics := restore()
	if strings.Contains(diagnostics, sentinel) {
		t.Error("a diagnostic holds the secret")
	}
	for _, want := range []string{`"event":"mcp_complete"`, `"tool":"drop_write"`, `"api_route":"/api/drop/op"`, `"tool":"unknown"`} {
		if !strings.Contains(diagnostics, want) {
			t.Errorf("diagnostics lack %s", want)
		}
	}
	// The same with internal/leakscan: encoded copies and the raw data files.
	sources := []leakscan.Source{leakscan.DB("database", h.App.DB()), leakscan.DB("log database", h.App.AuxDB()),
		leakscan.Path(h.App.DataDir()), leakscan.Text("standard log", std.String()), leakscan.Text("MCP diagnostics", diagnostics)}
	for i, answer := range answers {
		sources = append(sources, leakscan.Text(fmt.Sprintf("MCP answer %d", i), answer))
	}
	leakscan.Require(t, []leakscan.Sentinel{{Label: "secret value", Value: sentinel}}, sources...)
}

// The same OAuth grant must gain and lose private operations as the account
// changes, including on catalog-only installations and for an administrator.
func TestMCPDeveloperAccess(t *testing.T) {
	h := hostedMCP(t)
	admin := h.Token(h.Owner())
	_, testerSession := h.User("preview@sample.test", "user")
	testerToken := h.OAuthToken(testerSession)
	query := h.App.DB().NewQuery("UPDATE drop_official_config SET enabled = 1 WHERE id = 1")
	if _, err := query.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "user"} {
		account, session := h.User(role+"-developer@example.test", role)
		token := h.OAuthToken(session)
		input := map[string]any{"name": "Private build", "slug": role + "-build"}
		write := map[string]any{"op": "developer.create", "input": input, "idempotency_key": "create-build"}
		roles := []string{"user", "developer", "user"}
		if role == "admin" {
			roles = []string{"admin"}
		}
		for _, assigned := range roles {
			enabled := assigned == "developer"
			h.MustOp(admin, "user.set", map[string]any{"user": account.Id, "role": assigned})
			tools := listTools(t, h, token)
			if len(tools) != 2 || slices.Contains(tools[0].InputSchema.Properties["what"].Enum, "shared") {
				t.Fatalf("%s unexpected tools: %+v", role, tools)
			}
			names := tools[1].InputSchema.Properties["op"].Enum
			for _, name := range []string{"developer.create", "developer.upload_link", "developer.deploy"} {
				if slices.Contains(names, name) != enabled || strings.Contains(tools[1].Description, "- "+name+" ") != enabled {
					t.Errorf("%s wrong exposure of %s (developer %t): %v", role, name, enabled, names)
				}
			}
			if slices.Contains(names, "node.create") != (role == "admin") || slices.Contains(names, "workspace.create") != (role == "user") {
				t.Errorf("%s lost its ordinary schema: %v", role, names)
			}
			if self := mustTool(t, h, token, "drop_read", map[string]any{"what": "me"}); self["developer"] != enabled {
				t.Fatalf("me did not reflect the grant: %v", self)
			}
			if !enabled {
				wantToolError(t, callTool(t, h, token, "drop_write", write), 403, "forbidden")
				continue
			}
			result := mustTool(t, h, token, "drop_write", write)
			app := result["result"].(map[string]any)["app"].(map[string]any)
			if app["owner"] != account.Id || app["slug"] != "dev-"+role+"-build" {
				t.Fatalf("%s created an unexpected app: %v", role, app)
			}
			upload := mustTool(t, h, token, "drop_write", map[string]any{"op": "developer.upload_link", "input": map[string]any{"app": app["id"]}})
			if path, _ := dig(upload, "result", "path").(string); !strings.HasPrefix(path, dropapi.UploadRoutePrefix) {
				t.Fatalf("missing one-time upload path for %s", role)
			}
			wantToolError(t, callTool(t, h, testerToken, "drop_read", map[string]any{"what": "releases", "app": app["id"]}), 404, "not_found")
		}
	}
}

func TestOfficialUserHasAutomaticLifecycle(t *testing.T) {
	names := (mcpProfile{role: dropapi.RoleUser, official: true}).writeNames()
	for _, name := range names {
		if name == "app.start" || name == "app.stop" {
			t.Fatalf("official user offered %s", name)
		}
	}
}
