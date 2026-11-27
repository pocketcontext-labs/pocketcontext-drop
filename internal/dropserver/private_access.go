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

// Invalidate outstanding tickets when a private app owner loses access.
func invalidatePrivateAccountAccess(app core.App, user *core.Record) error {
	return waitExec(app, "UPDATE drop_private_keys SET seed=randomblob(32) WHERE app IN (SELECT id FROM apps WHERE owner={:id})", dbx.Params{"id": user.Id})
}

func privateAccess(c *Ctx, in *dropapi.PrivateAccessInput) (any, error) {
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
	if c.Auth.Id != a.GetString("owner") {
		return nil, NotFound("App")
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
