package dropserver

import (
	"crypto/ed25519"
	"database/sql"
	"errors"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/golang-jwt/jwt/v5"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

func init() {
	register("private.access", privateAccess)
	register("private.grant", privateTester)
	register("private.revoke", privateTester)
	register("private.shared", privateShared)
}

func appHostname(a *core.Record, domain string) string {
	if !a.GetBool("developer_private") {
		return a.GetString("hostname")
	}
	if !ValidAppDomain(domain) {
		return ""
	}
	return a.GetString("slug") + "." + domain
}

func privateAppKey(app core.App, id string) (ed25519.PrivateKey, error) {
	var seed []byte
	if err := app.DB().NewQuery("SELECT seed FROM drop_private_keys WHERE app={:app}").Bind(dbx.Params{"app": id}).Row(&seed); err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("private app access key unavailable")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// Rotate affected keys when the owner loses developer access or a runtime
// tester is suspended. Node desired hashes include the new verification keys.
func invalidatePrivateAccountAccess(app core.App, user *core.Record) error {
	err := waitExec(app, `UPDATE drop_private_keys SET seed=randomblob(32)
	WHERE app IN (SELECT id FROM apps WHERE owner={:user}) OR
	({:revokeTester} AND app IN (SELECT app FROM drop_private_testers WHERE account={:user}))`,
		dbx.Params{"user": user.Id, "revokeTester": user.GetBool("suspended") || hostedAdmin(app, user)})
	if err != nil || !hostedAdmin(app, user) {
		return err
	}
	return waitExec(app, "DELETE FROM drop_private_testers WHERE account={:id}", dbx.Params{"id": user.Id})
}

func privateTester(c *Ctx, in *dropapi.PrivateTesterInput) (any, error) {
	a, err := c.findApp(in.App, true)
	if err != nil {
		return nil, err
	}
	if !a.GetBool("developer_private") || a.GetString("owner") != c.Auth.Id {
		return nil, NotFound("App")
	}
	email, err := canonicalEmail(in.Email)
	if err != nil {
		return nil, err
	}
	u, err := c.App.FindAuthRecordByEmail("users", email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFound("Account")
	}
	if err != nil {
		return nil, err
	}
	if u.Id == c.Auth.Id {
		return nil, Invalid("email", "The owner already has access.")
	}
	p := dbx.Params{"app": a.Id, "account": u.Id}
	grant := c.Spec.Name == "private.grant"
	if grant {
		if u.GetBool("suspended") || hostedAdmin(c.App, u) {
			return nil, NotFound("Account")
		}
		err = waitExec(c.App, "INSERT INTO drop_private_testers(app,account) VALUES ({:app},{:account}) ON CONFLICT DO NOTHING", p)
	} else {
		res, err := c.App.DB().Delete("drop_private_testers", dbx.HashExp{"app": a.Id, "account": u.Id}).Execute()
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n > 0 {
			// Invalidate outstanding runtime credentials as soon as the node
			// applies this revision, including those held by a removed tester.
			if err := waitExec(c.App, "UPDATE drop_private_keys SET seed=randomblob(32) WHERE app={:app}", dbx.Params{"app": a.Id}); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, err
	}
	c.Event(Event{App: a.Id, SubjectUser: u.Id})
	return map[string]bool{"granted": grant}, nil
}

func privateAccess(c *Ctx, in *dropapi.PrivateAccessInput) (any, error) {
	// Runtime-only invitations never authorize app.get, artifacts or writes.
	a, err := c.App.FindRecordById("apps", in.App)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFound("App")
	}
	if err != nil {
		return nil, err
	}
	if !a.GetBool("developer_private") || a.GetBool("deleting") || a.GetBool("archived") {
		return nil, NotFound("App")
	}
	owner, err := c.App.FindRecordById("users", a.GetString("owner"))
	if err != nil {
		return nil, err
	}
	if !isDeveloper(owner) || owner.GetBool("suspended") {
		return nil, NotFound("App")
	}
	if c.Auth.Id != owner.Id {
		var allowed bool
		if err := c.App.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_private_testers WHERE app={:app} AND account={:account})").Bind(dbx.Params{"app": a.Id, "account": c.Auth.Id}).Row(&allowed); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, NotFound("App")
		}
	}
	host := appHostname(a, servicesOf(c.App).AppDomain)
	if host == "" {
		return nil, Conflict("private_ingress_unavailable", "Private app ingress is not configured.")
	}
	key, err := privateAppKey(c.App, a.Id)
	if err != nil {
		return nil, err
	}
	exp := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{
		Issuer: render.PrivateAccessIssuer, Subject: c.Auth.Id, Audience: jwt.ClaimStrings{a.GetString("nib_app_id")}, ExpiresAt: jwt.NewNumericDate(exp),
	})
	signed, err := token.SignedString(key)
	if err != nil {
		return nil, err
	}
	c.Event(Event{App: a.Id})
	return map[string]any{"url": "https://" + host, "header": render.PrivateAccessHeader, "token": signed, "expires_at": exp}, nil
}

func privateShared(c *Ctx, _ *dropapi.MeInput) (any, error) {
	type sharedApp struct {
		ID       string `db:"id" json:"id"`
		Name     string `db:"name" json:"name"`
		Hostname string `db:"hostname" json:"hostname"`
	}
	apps := []sharedApp{}
	if c.Admin {
		return map[string]any{"apps": apps}, nil
	}
	err := c.App.DB().NewQuery(`SELECT a.id, a.name, a.slug || '.' || {:domain} AS hostname FROM drop_private_testers t
	JOIN apps a ON a.id=t.app JOIN users u ON u.id=a.owner WHERE t.account={:account} AND a.developer_private=1 AND a.deleting=0 AND a.archived=0
	AND u.suspended=0 AND u.role='developer' ORDER BY a.created DESC, a.id`).Bind(dbx.Params{"domain": servicesOf(c.App).AppDomain, "account": c.Auth.Id}).All(&apps)
	return map[string]any{"apps": apps}, err
}
