package dropserver_test

import (
	"encoding/json"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func events(t *testing.T, h *droptest.Hosted, action string) []*core.Record {
	t.Helper()
	records, err := h.App.FindRecordsByFilter("events", "action = {:a}", "created", 0, 0, map[string]any{"a": action})
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func wantError(t *testing.T, r droptest.OpResult, status int, code string) {
	t.Helper()
	if r.Status != status || r.Error["code"] != code || r.Error["message"] == "" {
		t.Fatalf("want %d %s, got %d %s", status, code, r.Status, r.Raw)
	}
}

// TestOpRequiresAnAccountAndTheRole: anonymous and invalid sessions get 401,
// other auth collections and suspended accounts 403, a user calling an admin
// operation 403, and malformed requests 400 with a stable code.
func TestOpRequiresAnAccountAndTheRole(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, user := h.User("user@example.test", "user")
	admin := h.Token(h.Owner())
	wantError(t, h.Op("", "me", nil), 401, "unauthenticated")
	if r := h.Op("not-a-token", "me", nil); r.Status != 401 {
		t.Fatalf("invalid token: %d", r.Status)
	}
	superusers, _ := h.App.FindCollectionByNameOrId("_superusers")
	su := core.NewRecord(superusers)
	su.SetEmail("root@example.test")
	su.SetPassword("synthetic-superuser-password")
	if err := h.App.Save(su); err != nil {
		t.Fatal(err)
	}
	wantError(t, h.Op(h.Token(su), "me", nil), 403, "forbidden")
	for _, op := range []string{"user.list", "user.set", "invite.create", "invite.list", "invite.revoke"} {
		wantError(t, h.Op(user, op, map[string]any{}), 403, "forbidden")
	}
	wantError(t, h.Op(user, "no.such_op", nil), 400, "unknown_op")
	wantError(t, h.Op(user, "me", map[string]any{"extra": 1}), 400, "invalid_input")
	wantError(t, h.Op(user, "me", []int{1}), 400, "invalid_input")
	wantError(t, h.Op(admin, "invite.create", map[string]any{"email": "x@example.test", "colour": "red"}), 400, "invalid_input")
	for _, body := range []string{`{"op":"me","extra":1}`, `{"op":"me"} {}`, `not json`} {
		if w := h.Request("POST", "/api/drop/op", user, body); w.Code != 400 || !strings.Contains(w.Body.String(), `"invalid_request"`) {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := h.Request("POST", "/api/drop/op", user, `{"op":"me","input":"`+strings.Repeat("x", 300<<10)+`"}`); w.Code == 200 {
		t.Fatal("an oversized request was accepted")
	}
	if r := h.MustOp(user, "me", nil); r["email"] != "user@example.test" || r["role"] != "user" {
		t.Fatalf("me: %v", r)
	}
	if r := h.MustOp(admin, "me", nil); r["role"] != "admin" || r["owner"] != true || r["admin"] != true {
		t.Fatalf("owner me: %v", r)
	}
	if w := h.Request("GET", "/api/drop/me", "", ""); w.Code != 401 {
		t.Fatalf("anonymous me: %d", w.Code)
	}
	if n := len(events(t, h, "me")); n != 0 {
		t.Fatalf("a read operation wrote %d events", n)
	}
}

// TestInviteOperations: create (defaults, sign-in URL, validation, refusals),
// list, revoke, each change writing one event.
func TestInviteOperations(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	created := h.MustOp(admin, "invite.create", map[string]any{"email": " New.Person@Example.test "})
	invite := created["invite"].(map[string]any)
	if invite["email"] != "new.person@example.test" || invite["role"] != "user" || invite["app_quota"] != float64(3) || invite["state"] != "open" || invite["invited_by"] != h.Owner().Id {
		t.Fatalf("invite: %v", invite)
	}
	if created["sign_in_url"] != "https://"+droptest.Domain+"/#/invite?email=new.person%40example.test" {
		t.Fatalf("sign-in URL %v", created["sign_in_url"])
	}
	h.NoMail() // no invite email is sent
	wantError(t, h.Op(admin, "invite.create", map[string]any{"email": "NEW.person@example.test"}), 409, "invite_exists")
	wantError(t, h.Op(admin, "invite.create", map[string]any{"email": droptest.OwnerEmail}), 409, "account_exists")
	for _, bad := range []map[string]any{
		{"email": "not an address"}, {"email": "Name <a@example.test>"}, {"email": "ünï@example.test"}, {"email": "account@drop.invalid"},
		{"email": "a@example.test", "role": "root"}, {"email": "a@example.test", "app_quota": 101}, {"email": "a@example.test", "app_quota": -1},
		{"email": "a@example.test", "expires_days": 31}, {"email": strings.Repeat("a", 250) + "@example.test"},
	} {
		wantError(t, h.Op(admin, "invite.create", bad), 400, "invalid_input")
	}
	second := h.MustOp(admin, "invite.create", map[string]any{"email": "boss@example.test", "role": "admin", "app_quota": 0, "expires_days": 1})["invite"].(map[string]any)
	if second["role"] != "admin" || second["app_quota"] != float64(0) {
		t.Fatalf("second invite: %v", second)
	}
	list := h.MustOp(admin, "invite.list", map[string]any{"state": "open"})["invites"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["email"] != "boss@example.test" {
		t.Fatalf("open invites newest first: %v", list)
	}
	revoked := h.MustOp(admin, "invite.revoke", map[string]any{"invite": second["id"]})["invite"].(map[string]any)
	if revoked["state"] != "revoked" {
		t.Fatalf("revoked: %v", revoked)
	}
	wantError(t, h.Op(admin, "invite.revoke", map[string]any{"invite": second["id"]}), 409, "invite_closed")
	wantError(t, h.Op(admin, "invite.revoke", map[string]any{"invite": "aaaaaaaaaaaaaaa"}), 404, "not_found")
	if n := len(h.MustOp(admin, "invite.list", map[string]any{"state": "open"})["invites"].([]any)); n != 1 {
		t.Fatalf("open invites after revoking: %d", n)
	}
	if all := h.MustOp(admin, "invite.list", nil); len(all["invites"].([]any)) != 2 || all["has_more"] != false {
		t.Fatalf("all invites: %v", all)
	}
	wantError(t, h.Op(admin, "invite.list", map[string]any{"state": "closed"}), 400, "invalid_input")
	// A revoked address may be invited again.
	h.MustOp(admin, "invite.create", map[string]any{"email": "boss@example.test"})
	creates := events(t, h, "invite.create")
	if len(creates) != 3 || creates[0].GetString("actor") != h.Owner().Id {
		t.Fatalf("invite.create events: %d", len(creates))
	}
	var detail map[string]any
	_ = json.Unmarshal([]byte(creates[0].GetString("detail")), &detail)
	if detail["email"] != "new.person@example.test" || detail["role"] != "user" {
		t.Fatalf("event detail: %v", detail)
	}
	if n := len(events(t, h, "invite.revoke")); n != 1 {
		t.Fatalf("invite.revoke events: %d", n)
	}
}

// TestUserOperations: list and set, with the rules that keep an admin from
// changing their own role or suspension and anyone from demoting or
// suspending the owner.
func TestUserOperations(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	member, _ := h.User("member@example.test", "user")
	deputy, deputyToken := h.User("deputy@example.test", "admin")
	users := h.MustOp(owner, "user.list", nil)["users"].([]any)
	if len(users) != 3 {
		t.Fatalf("users: %v", users)
	}
	byEmail := map[string]map[string]any{}
	for _, u := range users {
		m := u.(map[string]any)
		byEmail[m["email"].(string)] = m
	}
	if byEmail[droptest.OwnerEmail]["owner"] != true || byEmail["deputy@example.test"]["role"] != "admin" || byEmail["member@example.test"]["signed_in"] != true {
		t.Fatalf("user list: %v", byEmail)
	}
	wantError(t, h.Op(owner, "user.set", map[string]any{"user": deputy.Id, "role": "developer"}), 403, "forbidden")
	wantError(t, h.Op(deputyToken, "user.set", map[string]any{"user": member.Id, "role": "admin"}), 403, "forbidden")
	set := h.MustOp(deputyToken, "user.set", map[string]any{"user": member.Id, "role": "developer", "app_quota": 9})["user"].(map[string]any)
	if set["role"] != "developer" || set["app_quota"] != float64(9) {
		t.Fatalf("set: %v", set)
	}
	wantError(t, h.Op(deputyToken, "user.set", map[string]any{"user": deputy.Id, "role": "user"}), 403, "forbidden")
	wantError(t, h.Op(deputyToken, "user.set", map[string]any{"user": deputy.Id, "suspended": true}), 403, "forbidden")
	wantError(t, h.Op(deputyToken, "user.set", map[string]any{"user": droptest.OwnerEmail, "role": "user"}), 403, "forbidden")
	wantError(t, h.Op(deputyToken, "user.set", map[string]any{"user": droptest.OwnerEmail, "suspended": true}), 403, "forbidden")
	wantError(t, h.Op(owner, "user.set", map[string]any{"user": member.Id}), 400, "invalid_input")
	wantError(t, h.Op(owner, "user.set", map[string]any{"user": member.Id, "app_quota": 500}), 400, "invalid_input")
	wantError(t, h.Op(owner, "user.set", map[string]any{"user": member.Id, "role": "root"}), 400, "invalid_input")
	wantError(t, h.Op(owner, "user.set", map[string]any{"user": "nobody@example.test", "role": "user"}), 404, "not_found")
	// Changing your own quota is allowed; it changes nothing else.
	h.MustOp(deputyToken, "user.set", map[string]any{"user": deputy.Id, "app_quota": 5})
	sets := events(t, h, "user.set")
	if len(sets) != 2 || sets[0].GetString("subject_user") != member.Id || sets[0].GetString("actor") != deputy.Id {
		t.Fatalf("user.set events: %d", len(sets))
	}
}

// TestIdempotencyKeys: a retry with the same key and input replays the first
// result without acting again; a different input under the key is refused;
// keys belong to one account.
func TestIdempotencyKeys(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, deputy := h.User("deputy@example.test", "admin")
	first := h.Op(owner, "invite.create", map[string]any{"email": "retry@example.test"}, "key-1")
	again := h.Op(owner, "invite.create", map[string]any{"email": "retry@example.test"}, "key-1")
	if first.Status != 200 || first.Replayed || again.Status != 200 || !again.Replayed {
		t.Fatalf("first %d %v, again %d %v %s", first.Status, first.Replayed, again.Status, again.Replayed, again.Raw)
	}
	if first.Result["invite"].(map[string]any)["id"] != again.Result["invite"].(map[string]any)["id"] {
		t.Fatal("the replay returned another invite")
	}
	if n := len(events(t, h, "invite.create")); n != 1 {
		t.Fatalf("a replay wrote another event: %d", n)
	}
	wantError(t, h.Op(owner, "invite.create", map[string]any{"email": "other@example.test"}, "key-1"), 409, "idempotency_conflict")
	wantError(t, h.Op(owner, "invite.create", map[string]any{"email": "x@example.test"}, "bad key!"), 400, "invalid_idempotency_key")
	// Another account's key-1 is its own: it acts (and here meets the open invite).
	wantError(t, h.Op(deputy, "invite.create", map[string]any{"email": "retry@example.test"}, "key-1"), 409, "invite_exists")
	// A failed operation stores nothing; the key stays usable.
	wantError(t, h.Op(owner, "invite.create", map[string]any{"email": "bad"}, "key-2"), 400, "invalid_input")
	if r := h.Op(owner, "invite.create", map[string]any{"email": "good@example.test"}, "key-2"); r.Status != 200 || r.Replayed {
		t.Fatalf("key after a failure: %d %s", r.Status, r.Raw)
	}
}

func TestHostedRoleBoundaries(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	member, user := h.User("member@example.test", "user")
	for _, token := range []string{admin, user} {
		for _, op := range []string{"app.create", "release.upload_link", "release.deploy", "beta.prepare", "beta.deploy", "beta.probe_upload", "beta.import_url", "developer.create", "developer.upload_link", "developer.deploy"} {
			wantError(t, h.Op(token, op, nil), 403, "forbidden")
		}
	}
	wantError(t, h.Op(admin, "workspace.create", nil), 403, "forbidden")
	wantError(t, h.Op(admin, "user.set", map[string]any{"user": h.Owner().Id, "role": "developer"}), 403, "forbidden")
	for _, role := range []string{"developer", "user"} {
		target := map[string]any{"user": member.Id, "role": role}
		wantError(t, h.Op(user, "user.set", target), 403, "forbidden")
		h.MustOp(admin, "user.set", target)
		self := h.MustOp(user, "me", nil)
		if self["role"] != role || self["admin"] != false || self["developer"] != (role == "developer") {
			t.Fatalf("unexpected account permissions: %v", self)
		}
		users := h.MustOp(admin, "user.directory", map[string]any{"search": member.Id, "role": role})["users"].([]any)
		if len(users) != 1 || users[0].(map[string]any)["role"] != role {
			t.Fatalf("unexpected directory: %v", users)
		}
		wantError(t, h.Op(user, "node.list", nil), 403, "forbidden")
	}
}
func TestAccountWithWorkspacesCannotBecomeAdmin(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	account, dev := h.User("builder@example.test", "developer")
	id := newApp(t, h, dev, "existing-app")
	link := uploadLink(t, h, dev, id, nil)
	admin := h.Token(h.Owner())
	wantError(t, h.Op(admin, "user.set", map[string]any{"user": account.Id, "role": "admin"}), 403, "forbidden")
	h.MustOp(admin, "user.set", map[string]any{"user": account.Id, "role": "user"})
	wantUploadError(t, upload(t, h, link, droptest.ELF(4096, 1)), 404, "upload_link_invalid")
	h.MustOp(admin, "user.set", map[string]any{"user": account.Id, "role": "developer"})
	wantUploadError(t, upload(t, h, link, droptest.ELF(4096, 1)), 404, "upload_link_invalid")
}
