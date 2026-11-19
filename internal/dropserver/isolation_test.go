package dropserver_test

import (
	"encoding/json"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// Isolation of accounts, invites and events (plan section 9): users A and B
// and the owner O as admin.

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
	a, aToken := h.User("a@example.test", "user")
	b, bToken := h.User("b@example.test", "user")
	invite := h.MustOp(owner, "invite.create", map[string]any{"email": "c@example.test"})["invite"].(map[string]any)["id"].(string)
	h.MustOp(owner, "user.set", map[string]any{"user": b.Id, "app_quota": 4})
	// An app of A and an event about it, written as an operation would.
	appA := createApp(t, h, a.Id, "app-of-a")
	if err := dropserver.WriteEvent(h.App, dropserver.EventRecord{Actor: a.Id, App: appA, Action: "app.create", Detail: map[string]any{"slug": "app-of-a"}}); err != nil {
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
	if n := listCount(t, h, "/api/collections/events/records", aToken); n != 1 {
		t.Errorf("A lists %d events, want the one of its app", n)
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
	if fresh, _ := h.App.FindRecordById("users", a.Id); fresh.GetString("role") != "user" {
		t.Fatal("A changed its own role")
	}
}

// createApp saves a placeholder app owned by an account.
func createApp(t *testing.T, h *droptest.Hosted, owner, slug string) string {
	t.Helper()
	collection, err := h.App.FindCollectionByNameOrId("apps")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("owner", owner)
	record.Set("name", slug)
	record.Set("slug", slug)
	if err := h.App.Save(record); err != nil {
		t.Fatal(err)
	}
	return record.Id
}
