package dropserver

import (
	"crypto/ed25519"
	"errors"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// Older node agents still require a signed private route to stage encrypted artifacts.
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

// Revoke any ticket issued before the node was upgraded to public routing.
func invalidatePrivateAccountAccess(app core.App, user *core.Record) error {
	return waitExec(app, "UPDATE drop_private_keys SET seed=randomblob(32) WHERE app IN (SELECT id FROM apps WHERE owner={:id})", dbx.Params{"id": user.Id})
}
