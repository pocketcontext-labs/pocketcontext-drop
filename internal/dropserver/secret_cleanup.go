package dropserver

import (
	"context"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// clearSecretDeletion runs inside the same writer transaction as a cleanup or
// a subsequent secret.set. A stale worker can never delete a recreated value.
// Delete in the store is idempotent, including after a crash before commit.
func clearSecretDeletion(ctx context.Context, tx core.App, parameter string) error {
	var pending bool
	params := dbx.Params{"parameter": parameter}
	if err := tx.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_secret_deletions WHERE parameter = {:parameter})").Bind(params).Row(&pending); err != nil || !pending {
		return err
	}
	store := servicesOf(tx).Secrets
	if store == nil {
		return secretsUnavailable()
	}
	call, cancel := context.WithTimeout(ctx, secretStoreTimeout)
	defer cancel()
	if err := store.Delete(call, parameter); err != nil {
		return err
	}
	_, err := tx.DB().Delete("drop_secret_deletions", dbx.HashExp{"parameter": parameter}).Execute()
	return err
}

func finishSecretDeletion(ctx context.Context, app core.App, parameter string) error {
	err := app.RunInTransaction(func(tx core.App) error { return clearSecretDeletion(ctx, tx, parameter) })
	if err != nil && ctx.Err() == nil {
		app.Logger().Error("Drop secret deletion pending", "action", "delete")
	}
	return err
}

func runSecretCleanup(ctx context.Context, app core.App) {
	if servicesOf(app).Secrets == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		var parameters []string
		if err := app.DB().NewQuery("SELECT parameter FROM drop_secret_deletions ORDER BY parameter").Column(&parameters); err != nil {
			app.Logger().Error("Could not read pending Drop secret deletions")
		}
		for _, parameter := range parameters {
			if ctx.Err() != nil {
				return
			}
			_ = finishSecretDeletion(ctx, app, parameter)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
