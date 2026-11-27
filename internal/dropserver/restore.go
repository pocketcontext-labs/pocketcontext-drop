package dropserver

import (
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// InvalidateNodeTokens discards pending upload links and rotates every node's token key, so no node token
// issued before a restore works on the restored instance. Core calls it
// first when serving with --hosted-restore (Hooks.OnHostedRestore); core
// itself rotates only the users accounts' keys and removes their sign-in
// codes and MCP grants. Node passwords stay valid: each agent gets a 401,
// signs in again with its password and pulls its desired document. An
// operator who suspects the passwords were copied too rotates them with
// node.rotate_secret.
func InvalidateNodeTokens(app core.App) error {
	return app.RunInTransaction(func(tx core.App) error {
		if err := waitExec(tx, "UPDATE drop_private_keys SET seed=randomblob(32)", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "DELETE FROM drop_private_testers", nil); err != nil {
			return err
		}
		nodes, err := tx.FindAllRecords(dropapi.NodeCollection)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			node.RefreshTokenKey()
			if err := tx.Save(node); err != nil {
				return err
			}
		}
		// Guest setup credentials from the backup belong to the original
		// deployment. Fresh reports can supply the restored owner's output.
		if err := waitExec(tx, "UPDATE node_reports SET log_tail = '', first_run = 'null'", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "DELETE FROM drop_official_uploads", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "DELETE FROM drop_manifest_uploads", nil); err != nil {
			return err
		}
		if _, err := tx.DB().NewQuery("DELETE FROM upload_links WHERE used_at = ''").Execute(); err != nil {
			return err
		}
		// The restored mail queue and unused codes predate the rollback.
		// Retain the private key so verified members can still withdraw.
		if err := waitExec(tx, "DELETE FROM drop_waitlist WHERE verified_at=''", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "UPDATE drop_waitlist SET attempts=5", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "UPDATE drop_waitlist_mail SET state='cancelled' WHERE state='queued'", nil); err != nil {
			return err
		}
		// An old snapshot can predate a release revocation. Require the
		// operator to reconfirm admissions before resuming restored Beta apps.
		if _, err := tx.DB().NewQuery("UPDATE drop_beta_catalog SET approved=0").Execute(); err != nil {
			return err
		}
		if _, err := tx.DB().NewQuery("DELETE FROM drop_beta_probes WHERE state='running'").Execute(); err != nil {
			return err
		}
		apps, err := tx.FindRecordsByFilter("apps", "beta_digest != '' && desired_state != 'stopped'", "", 0, 0)
		if err != nil {
			return err
		}
		for _, a := range apps {
			if err := stopApp(tx, a); err != nil {
				return err
			}
		}
		if len(apps) > 0 {
			if err := syncDesiredSeqs(tx); err != nil {
				return err
			}
		}
		// Pause destructive jobs restored from history until the owner confirms again.
		if err := waitExec(tx, "DELETE FROM drop_volume_deletions", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "UPDATE apps SET deleting=false WHERE id IN (SELECT app FROM drop_app_deletions WHERE completed=0)", nil); err != nil {
			return err
		}
		if err := waitExec(tx, "DELETE FROM drop_app_deletions WHERE completed=0", nil); err != nil {
			return err
		}
		if err := syncDesiredSeqs(tx); err != nil {
			return err
		}
		// The restored queue describes SSM at backup time. A name may since
		// have been recreated. An operator reconciles these entries using the
		// backup and current store, without replaying an old destructive intent.
		_, err = tx.DB().NewQuery("DELETE FROM drop_secret_deletions").Execute()
		return err
	})
}
