package dropserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

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

// TestUnknownAddressGetsNoAccount: until invites exist (M3) nobody but the
// owner can sign in. Another address gets the same answer, no email and no
// account.
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

// TestAppsBelongToTheirOwner: each account sees only its own apps, and a
// create or update request cannot choose or move the owner.
func TestAppsBelongToTheirOwner(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Owner()
	users, err := h.App.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	other := core.NewRecord(users)
	other.SetEmail("other@example.test")
	other.SetRandomPassword()
	other.SetVerified(true)
	if err := h.App.Save(other); err != nil {
		t.Fatal(err)
	}
	token := func(r *core.Record) string {
		s, err := r.NewAuthToken()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ownerToken, otherToken := token(owner), token(other)
	// Writes are local only; send them to the unfiltered router as the local
	// reader does, with the account's own token.
	router := func(method, path, authorization, body string) map[string]any {
		r := httptest.NewRequest(method, "http://127.0.0.1:8100"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", authorization)
		w := httptest.NewRecorder()
		h.Server.Router().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	created := router("POST", "/api/collections/apps/records", ownerToken, `{"name":"Owner app","slug":"owner-app","owner":"`+other.Id+`"}`)
	if created["owner"] != owner.Id {
		t.Fatalf("create chose owner %v", created["owner"])
	}
	moved := router("PATCH", "/api/collections/apps/records/"+created["id"].(string), ownerToken, `{"owner":"`+other.Id+`"}`)
	if moved["owner"] != owner.Id {
		t.Fatalf("update moved the app to %v", moved["owner"])
	}
	router("POST", "/api/collections/apps/records", otherToken, `{"name":"Other app","slug":"other-app"}`)
	list := func(authorization string) string {
		w := h.Request("GET", "/api/collections/apps/records", authorization, "")
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	if body := list(ownerToken); !strings.Contains(body, "owner-app") || strings.Contains(body, "other-app") {
		t.Fatalf("owner sees %s", body)
	}
	if body := list(otherToken); strings.Contains(body, "owner-app") || !strings.Contains(body, "other-app") {
		t.Fatalf("other account sees %s", body)
	}
	if w := h.Request("GET", "/api/collections/apps/records/"+created["id"].(string), otherToken, ""); w.Code != 404 {
		t.Fatalf("other account viewed the owner's app: %d", w.Code)
	}
}
