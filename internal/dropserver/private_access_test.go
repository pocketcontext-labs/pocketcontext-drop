package dropserver_test

import (
	"crypto/ed25519"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

func TestOwnerOnlyPrivateAccess(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner, ownerToken := h.User("builder@example.test", "user")
	_, testerToken := h.User("tester@example.test", "user")
	_, stranger := h.User("otherdev@example.test", "developer")
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
	for _, op := range []string{"private.grant", "private.revoke", "private.shared"} {
		wantError(t, h.Op(ownerToken, op, ref), 400, "unknown_op")
	}
	for _, token := range []string{admin, testerToken} {
		wantError(t, h.Op(token, "private.access", ref), 403, "forbidden")
	}
	wantError(t, h.Op(stranger, "private.access", ref), 404, "not_found")
	ticket := h.MustOp(ownerToken, "private.access", ref)
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
	h.MustOp(admin, "user.set", map[string]any{"user": owner.Id, "role": "user"})
	wantError(t, h.Op(ownerToken, "private.access", ref), 403, "forbidden")
	if err := verify("d-" + app); err == nil {
		t.Fatal("previous owner credential still valid after role loss")
	}
}
