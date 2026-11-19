package dropserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appconfig"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// TestHostedMultiServerStarts: Drop composes core's hosted Multi request path,
// whose startup creates the owner of an empty database.
func TestHostedMultiServerStarts(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	if w := h.Request("GET", "/up", "", ""); w.Code != 200 {
		t.Fatalf("/up: %d", w.Code)
	}
	w := h.Request("GET", "/api/bundle/auth-mode", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"email"`) {
		t.Fatalf("auth mode: %d %s", w.Code, w.Body)
	}
	owner := h.Owner()
	if owner.Verified() || !appconfig.Runtime.IsOwner(h.App, owner) {
		t.Fatal("hosted startup did not create the owner")
	}
	if n, err := h.App.CountRecords("users"); err != nil || n != 1 {
		t.Fatalf("accounts = %d, %v", n, err)
	}
}

// TestOwnerSignsInWithCode: the owner signs in with the six-digit code of the
// sign-in email, the session reaches the API, and the first sign-in verifies
// the account (core never prunes it).
func TestOwnerSignsInWithCode(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	auth := h.SignIn("Owner@Example.test")
	if auth.Record["email"] != droptest.OwnerEmail || auth.Record["id"] != h.Owner().Id {
		t.Fatalf("signed in as %v", auth.Record)
	}
	if !h.Owner().Verified() {
		t.Fatal("first sign-in did not verify the owner")
	}
	if w := h.Request("GET", "/api/collections/apps/records", auth.Token, ""); w.Code != 200 {
		t.Fatalf("apps with the session: %d %s", w.Code, w.Body)
	}
	if w := h.Request("POST", "/api/collections/users/auth-refresh", auth.Token, ""); w.Code != 200 {
		t.Fatalf("session renewal: %d %s", w.Code, w.Body)
	}
	if w := h.Request("GET", "/api/collections/apps/records", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"totalItems":0`) {
		t.Fatalf("anonymous apps read: %d %s", w.Code, w.Body)
	}
	if w := h.Request("GET", "/api/collections/apps/records", "not-a-token", ""); w.Code != 401 {
		t.Fatalf("invalid session: %d", w.Code)
	}
}

// TestUnknownAddressGetsNoAccount: Drop is invite-only. An address without an
// invite gets the same answer, no email and no account.
func TestUnknownAddressGetsNoAccount(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	for _, email := range []string{"stranger@example.test", "account@drop.invalid"} {
		h.RequestOTP(email)
	}
	h.NoMail()
	if n, err := h.App.CountRecords("users"); err != nil || n != 1 {
		t.Fatalf("accounts = %d, %v", n, err)
	}
	if w := h.Request("POST", "/api/collections/users/auth-with-password", "", `{"identity":"owner@example.test","password":"x"}`); w.Code != 404 {
		t.Fatalf("password sign-in: %d", w.Code)
	}
}

// TestLocalReadsAndWritesArePrivate: the SQL and search reads read whole
// tables, so they stay local even for a signed-in owner; record writes and
// local administration are never public.
func TestLocalReadsAndWritesArePrivate(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	token := h.SignIn(droptest.OwnerEmail).Token
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/context/query", `{"sql":"SELECT 1"}`},
		{"POST", "/api/context/search", `{"index":"apps","query":"x"}`},
		{"GET", "/api/context/schema", ""},
		{"POST", "/api/collections/apps/records", `{"name":"App","slug":"app"}`},
		{"POST", "/api/batch", `{"requests":[]}`},
		{"GET", "/api/collections/users/records", ""},
		{"POST", "/api/bundle/token", "{}"},
		{"GET", "/_/", ""},
	} {
		if w := h.Request(c.method, c.path, token, c.body); w.Code != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
}

// TestAppRecordsAreReadOnly: apps change only through operations. Record
// writes are refused even on the unfiltered local router with the owner's
// own token, and app.create sets the owner to the caller.
func TestAppRecordsAreReadOnly(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Owner()
	other, _ := h.User("other@example.test", "user")
	ownerToken := h.Token(owner)
	created := h.MustOp(ownerToken, "app.create", map[string]any{"name": "Owner app", "slug": "owner-app"})["app"].(map[string]any)
	if created["owner"] != owner.Id {
		t.Fatalf("app.create chose owner %v", created["owner"])
	}
	if r := h.Op(ownerToken, "app.create", map[string]any{"name": "Theirs", "slug": "their-app", "owner": other.Id}); r.Status != 400 {
		t.Fatalf("an owner field was accepted: %d %s", r.Status, r.Raw)
	}
	router := func(method, path, body string) int {
		r := httptest.NewRequest(method, "http://127.0.0.1:8100"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", ownerToken)
		w := httptest.NewRecorder()
		h.Server.Router().ServeHTTP(w, r)
		return w.Code
	}
	id := created["id"].(string)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/collections/apps/records", `{"name":"Direct","slug":"direct-app"}`},
		{"PATCH", "/api/collections/apps/records/" + id, `{"owner":"` + other.Id + `"}`},
		{"DELETE", "/api/collections/apps/records/" + id, ""},
		{"POST", "/api/collections/app_secret_names/records", `{"app":"` + id + `","name":"X"}`},
	} {
		if code := router(c.method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("%s %s: %d", c.method, c.path, code)
		}
	}
	if fresh, _ := h.App.FindRecordById("apps", id); fresh.GetString("owner") != owner.Id {
		t.Fatal("the app moved")
	}
}
