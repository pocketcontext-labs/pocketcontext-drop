package dropserver

import (
	"context"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// Daily capture with seven days of retention. Scope each database transaction
// to one workspace. Object deletion may take multiple passes on a versioned bucket.
func runBackupMaintenance(ctx context.Context, app core.App) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if servicesOf(app).Backups != nil {
			if err := maintainBackups(ctx, app, time.Now()); err != nil && ctx.Err() == nil {
				app.Logger().Error("Backup maintenance failed", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func maintainBackups(ctx context.Context, app core.App, now time.Time) error {
	if err := maintainBackupBilling(ctx, app, now); err != nil {
		return err
	}
	p := dbx.Params{"now": now.Unix(), "expiry": now.Add(-24 * time.Hour).Unix(), "retention": now.Add(-7 * 24 * time.Hour).Unix()}
	if err := app.RunInTransaction(func(tx core.App) error {
		if err := waitExec(tx, `UPDATE drop_backup_jobs SET state='failed',error='target_unavailable' WHERE state='pending' AND
			(created < {:expiry} OR NOT EXISTS(SELECT 1 FROM apps a JOIN placements p ON p.app=a.id WHERE
			a.id=drop_backup_jobs.target AND p.node=drop_backup_jobs.node AND NOT a.deleting AND NOT a.archived
			AND drop_backup_jobs.instance_id=a.nib_app_id||'-dep-'||CAST(a.deployment_seq AS INTEGER)))`, p); err != nil {
			return err
		}
		return waitExec(tx, `UPDATE drop_backups SET state='failed' WHERE state='pending' AND EXISTS
			(SELECT 1 FROM drop_backup_jobs j WHERE j.backup_id=drop_backups.id AND j.kind='capture' AND j.state='failed')`, p)
	}); err != nil {
		return err
	}
	var due []string
	err := app.DB().NewQuery(`SELECT p.app FROM drop_backup_policies p JOIN apps a ON a.id=p.app JOIN users u ON u.id=a.owner
	WHERE next_at<={:now} AND NOT u.suspended AND NOT a.archived AND NOT a.deleting
	ORDER BY next_at LIMIT 64`).Bind(p).Column(&due)
	if err != nil {
		return err
	}
	for _, id := range due {
		err = app.RunInTransaction(func(tx core.App) error {
			r, err := tx.DB().NewQuery("UPDATE drop_backup_policies SET next_at={:next} WHERE app={:a} AND next_at<={:now}").Bind(dbx.Params{"a": id, "next": now.Add(24 * time.Hour).Unix(), "now": now.Unix()}).Execute()
			if err != nil {
				return err
			}
			changed, err := r.RowsAffected()
			if err != nil || changed == 0 {
				return err
			}
			a, err := tx.FindRecordById("apps", id)
			if err != nil {
				return err
			}
			_, err = queueCapture(tx, a)
			return err
		})
		if err != nil {
			// Retry temporary app unavailability after peers have had their turn.
			if err := waitExec(app, "UPDATE drop_backup_policies SET next_at={:retry} WHERE app={:a} AND next_at<={:now}", dbx.Params{"a": id, "now": now.Unix(), "retry": now.Add(time.Hour).Unix()}); err != nil {
				return err
			}
		}
	}
	if err := waitExec(app, `UPDATE drop_backups SET state='expiring' WHERE state IN ('ready','failed') AND created<{:retention}
		AND NOT EXISTS(SELECT 1 FROM drop_backup_jobs j WHERE j.backup_id=drop_backups.id AND j.state='pending')`, p); err != nil {
		return err
	}
	var expired []struct {
		ID    string `db:"id"`
		Owner string `db:"owner"`
	}
	if err := app.DB().NewQuery("SELECT id,owner FROM drop_backups WHERE state='expiring' ORDER BY created LIMIT 32").All(&expired); err != nil {
		return err
	}
	for _, b := range expired {
		if err := servicesOf(app).Backups.PurgeBackup(ctx, backupKey(b.Owner, b.ID)); err != nil {
			return err
		}
		if err := waitExec(app, "DELETE FROM drop_backups WHERE id={:b} AND state='expiring'", dbx.Params{"b": b.ID}); err != nil {
			return err
		}
	}
	return nil
}
