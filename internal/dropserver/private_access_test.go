package dropserver_test

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

func TestPrivateTesterAccessIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner, ownerToken := h.User("builder@example.test", "user")
	tester, testerToken := h.User("tester@example.test", "user")
	_, stranger := h.User("uninvited@example.test", "user")
	admin := h.Token(h.Owner())
	h.MustOp(admin, "user.set", map[string]any{"user": owner.Id, "role": "developer"})
	app := newApp(t, h, ownerToken, "private-test")
	a, err := h.App.FindRecordById("apps", app)
	if err != nil {
		t.Fatal(err)
	}
	a.Set("developer_private", true)
	a.Set("hostname", "")
	if err := h.App.Save(a); err != nil {
		t.Fatal(err)
	}
	ref := map[string]any{"app": app}
	grant := map[string]any{"app": app, "email": tester.Email()}
	wantError(t, h.Op(admin, "private.access", ref), 403, "forbidden")
	wantError(t, h.Op(ownerToken, "private.grant", map[string]any{"app": app, "email": h.Owner().Email()}), 404, "not_found")
	for _, token := range []string{testerToken, stranger} {
		if r := h.Op(token, "private.access", ref); r.Status != 404 {
			t.Fatalf("access before invitation returned %d", r.Status)
		}
	}
	h.MustOp(ownerToken, "private.grant", grant)
	list := h.MustOp(testerToken, "private.shared", nil)["apps"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != app {
		t.Fatal(list)
	}
	wantError(t, h.Op(stranger, "private.access", ref), 404, "not_found")
	for _, token := range []string{stranger, admin} {
		if list := h.MustOp(token, "private.shared", nil)["apps"].([]any); len(list) != 0 {
			t.Fatal(list)
		}
	}
	ticket := h.MustOp(testerToken, "private.access", ref)
	if ticket["header"] != render.PrivateAccessHeader || ticket["url"] != "https://private-test."+droptest.AppDomain {
		t.Fatal("unexpected credential binding")
	}
	raw := ticket["token"].(string)
	key := func() *ed25519.PublicKey {
		var seed []byte
		if err := h.App.DB().NewQuery("SELECT seed FROM drop_private_keys WHERE app={:id}").Bind(dbx.Params{"id": app}).Row(&seed); err != nil {
			t.Fatal(err)
		}
		public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		return &public
	}
	verify := func(aud string) error {
		pub := *key()
		_, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return pub, nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(render.PrivateAccessIssuer), jwt.WithAudience(aud), jwt.WithExpirationRequired())
		return err
	}
	if err := verify("d-" + app); err != nil {
		t.Fatal(err)
	}
	if err := verify("d-000000000000000"); err == nil {
		t.Fatal("credential accepted for another app")
	}
	if r := h.Op(testerToken, "app.get", ref); r.Status != 404 {
		t.Fatal("tester can inspect app metadata")
	}
	h.MustOp(ownerToken, "private.revoke", grant)
	if r := h.Op(testerToken, "private.access", ref); r.Status != 404 || strings.Contains(r.Raw, raw) {
		t.Fatal("revoked tester issued a credential")
	}
	if err := verify("d-" + app); err == nil {
		t.Fatal("revocation did not replace the node verification key")
	}
	h.MustOp(ownerToken, "private.grant", grant)
	raw = h.MustOp(testerToken, "private.access", ref)["token"].(string)
	h.MustOp(admin, "user.set", map[string]any{"user": tester.Id, "role": "admin"})
	wantError(t, h.Op(testerToken, "private.access", ref), 403, "forbidden")
	if err := verify("d-" + app); err == nil {
		t.Fatal("promoted tester's issued token is still valid")
	}
	h.MustOp(admin, "user.set", map[string]any{"user": tester.Id, "role": "user"})
	wantError(t, h.Op(testerToken, "private.access", ref), 404, "not_found")

}
