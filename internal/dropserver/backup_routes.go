package dropserver

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

const authorizedBackupJobs = `SELECT j.id,j.kind,a.nib_app_id AS app_id,b.source_app,j.instance_id,j.backup_id,b.sha256,b.bytes
	FROM drop_backup_jobs j JOIN drop_backups b ON b.id=j.backup_id
	JOIN apps a ON a.id=j.target AND a.owner=b.owner
	JOIN placements p ON p.app=a.id AND p.node=j.node JOIN users u ON u.id=a.owner
	WHERE j.node={:node} AND j.state='pending' AND NOT a.deleting AND NOT a.archived AND NOT u.suspended
	AND a.desired_state!='stopped' AND j.instance_id=a.nib_app_id||'-dep-'||CAST(a.deployment_seq AS INTEGER)
	AND NOT EXISTS(SELECT 1 FROM drop_credit_holds h WHERE h.app=a.id)
	AND (j.kind='restore' OR EXISTS(SELECT 1 FROM drop_backup_policies policy JOIN drop_credit_accounts credit ON credit.owner=a.owner
		WHERE policy.app=a.id AND credit.balance>COALESCE((SELECT SUM(held) FROM drop_storage_reservations WHERE owner=a.owner AND held>0),0)))`

func registerBackupRoutes(e *core.ServeEvent) {
	e.Router.GET(dropapi.AgentBackupJobsRoute, func(re *core.RequestEvent) error {
		node, err := agentNode(re, agentStateOf(re.App))
		if err != nil {
			return writeError(re, err)
		}
		jobs := []dropapi.BackupJob{}
		if err := re.App.DB().NewQuery(authorizedBackupJobs + " ORDER BY j.created,j.id LIMIT 32").Bind(dbx.Params{"node": node.Id}).All(&jobs); err != nil {
			return writeError(re, err)
		}
		return re.JSON(200, dropapi.BackupJobsResult{Jobs: jobs})
	})
	archive := dropapi.AgentBackupJobsRoute + "/{job}/archive"
	e.Router.GET(archive, func(re *core.RequestEvent) error {
		job, owner, err := nodeBackupJob(re, re.App)
		if err != nil {
			return writeError(re, err)
		}
		if job.Kind != "restore" {
			return writeError(re, NotFound("Backup"))
		}
		return sendBackup(re, owner, job.BackupID, job.Bytes)
	})
	e.Router.PUT(archive, receiveBackup).Bind(apis.BodyLimit(dropapi.MaxBackupBytes))
	e.Router.POST(dropapi.AgentBackupJobsRoute+"/{job}/result", finishBackupJob).Bind(apis.BodyLimit(4096))
	e.Router.GET(dropapi.BackupDownloadPrefix+"{id}/download", func(re *core.RequestEvent) error {
		if _, err := caller(re); err != nil {
			return writeError(re, err)
		}
		spec, _ := dropapi.Lookup("backup.list")
		c, err := authorizeOp(re.App, re, spec)
		if err != nil {
			return writeError(re, err)
		}
		id := re.Request.PathValue("id")
		var size int64
		err = c.App.DB().NewQuery("SELECT bytes FROM drop_backups WHERE id={:b} AND owner={:o} AND state='ready' AND created > unixepoch()-604800").Bind(dbx.Params{"b": id, "o": c.Auth.Id}).Row(&size)
		if errors.Is(err, sql.ErrNoRows) {
			return writeError(re, NotFound("Backup"))
		}
		if err != nil {
			return writeError(re, err)
		}
		return sendBackup(re, c.Auth.Id, id, size)
	})
}

func nodeBackupJob(re *core.RequestEvent, app core.App) (dropapi.BackupJob, string, error) {
	var job dropapi.BackupJob
	node, err := agentNode(re, agentStateOf(re.App))
	if err != nil {
		return job, "", err
	}
	return readBackupJob(app, node.Id, re.Request.PathValue("job"))
}

func readBackupJob(app core.App, node, id string) (dropapi.BackupJob, string, error) {
	var job dropapi.BackupJob
	err := app.DB().NewQuery(authorizedBackupJobs + " AND j.id={:id}").Bind(dbx.Params{"node": node, "id": id}).One(&job)
	if errors.Is(err, sql.ErrNoRows) {
		return job, "", NotFound("Backup operation")
	}
	if err != nil {
		return job, "", err
	}
	var owner string
	err = app.DB().NewQuery("SELECT owner FROM drop_backups WHERE id={:b}").Bind(dbx.Params{"b": job.BackupID}).Row(&owner)
	return job, owner, err
}

func backupKey(owner, id string) string { return "backups/" + owner + "/" + id }

func sendBackup(re *core.RequestEvent, owner, id string, size int64) error {
	store := servicesOf(re.App).Backups
	if store == nil {
		return writeError(re, Conflict("backups_unavailable", "Backup storage is unavailable."))
	}
	reader, info, err := awsx.OpenBackup(re.Request.Context(), store, backupKey(owner, id))
	if err != nil {
		return writeError(re, err)
	}
	defer reader.Close()
	if info.Size != size {
		return writeError(re, Conflict("backup_corrupt", "The archive size does not match its receipt."))
	}
	h := re.Response.Header()
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", `attachment; filename="crew-`+id+`.zip"`)
	_, err = io.Copy(re.Response, reader)
	return err
}

func receiveBackup(re *core.RequestEvent) error {
	job, owner, err := nodeBackupJob(re, re.App)
	if err != nil {
		return writeError(re, err)
	}
	if job.Kind != "capture" {
		return writeError(re, NotFound("Backup operation"))
	}
	store := servicesOf(re.App).Backups
	if store == nil {
		return writeError(re, Conflict("backups_unavailable", "Backup storage is unavailable."))
	}
	f, err := os.CreateTemp("", "drop-backup-*")
	if err != nil {
		return writeError(re, err)
	}
	defer f.Close()
	if err := os.Remove(f.Name()); err != nil {
		return writeError(re, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(re.Request.Body, dropapi.MaxBackupBytes+1))
	if err != nil || n == 0 || n > dropapi.MaxBackupBytes {
		return writeError(re, Invalid("archive", "The archive is empty, incomplete or too large."))
	}
	digest := hex.EncodeToString(h.Sum(nil))
	ctx := re.Request.Context()
	key := backupKey(owner, job.BackupID)
	created, err := store.PutIfAbsent(ctx, key, f, n, digest)
	if err != nil {
		return writeError(re, err)
	}
	if !created {
		// A committed upload may have lost its response before the receipt was saved.
		// Hash the immutable first upload in that case, even if this retry sent a fresh capture.
		body, info, err := awsx.OpenBackup(ctx, store, key)
		if err != nil {
			return writeError(re, err)
		}
		defer body.Close()
		h.Reset()
		n, err = io.Copy(h, io.LimitReader(body, dropapi.MaxBackupBytes+1))
		if err != nil || n != info.Size || n > dropapi.MaxBackupBytes {
			return writeError(re, Conflict("backup_corrupt", "The stored archive failed verification."))
		}
		digest = hex.EncodeToString(h.Sum(nil))
	}
	err = re.App.RunInTransaction(func(tx core.App) error {
		if err := backupNodeStillActive(tx, re.Auth); err != nil {
			return err
		}
		if _, _, err := readBackupJob(tx, re.Auth.Id, job.ID); err != nil {
			return err
		}
		p := dbx.Params{"b": job.BackupID, "d": digest, "n": n, "j": job.ID}
		// Start the full retention window when the verified archive becomes available.
		if err := waitExec(tx, "UPDATE drop_backups SET state='ready',created=unixepoch(),sha256={:d},bytes={:n} WHERE id={:b}", p); err != nil {
			return err
		}
		return waitExec(tx, "UPDATE drop_backup_jobs SET state='complete' WHERE id={:j}", p)
	})
	if err != nil {
		return writeError(re, err)
	}
	return re.NoContent(http.StatusNoContent)
}

func finishBackupJob(re *core.RequestEvent) error {
	node, err := agentNode(re, agentStateOf(re.App))
	if err != nil {
		return writeError(re, err)
	}
	var in dropapi.BackupJobResult
	b, err := io.ReadAll(io.LimitReader(re.Request.Body, 4097))
	if err != nil || decodeStrict(b, &in) != nil {
		return writeError(re, Invalid("result", "Invalid backup result."))
	}
	err = re.App.RunInTransaction(func(tx core.App) error {
		if err := backupNodeStillActive(tx, re.Auth); err != nil {
			return err
		}
		job, _, err := readBackupJob(tx, node.Id, re.Request.PathValue("job"))
		if err != nil {
			return err
		}
		if in.Success && job.Kind == "capture" {
			return Invalid("archive", "Upload the archive to finish capturing a backup.")
		}
		state := "failed"
		if in.Success {
			state = "complete"
		}
		reason := ""
		if !in.Success {
			reason = "guest_rejected"
			if in.Error == "archive_too_large" {
				reason = in.Error
			}
		}
		params := dbx.Params{"j": job.ID, "s": state, "b": job.BackupID, "e": reason}
		if job.Kind == "capture" {
			if err := waitExec(tx, "UPDATE drop_backups SET state='failed' WHERE id={:b}", params); err != nil {
				return err
			}
		}
		return waitExec(tx, "UPDATE drop_backup_jobs SET state={:s},error={:e} WHERE id={:j}", params)
	})
	if err != nil {
		return writeError(re, err)
	}
	return re.NoContent(http.StatusNoContent)
}

func backupNodeStillActive(app core.App, auth *core.Record) error {
	node, err := app.FindRecordById(dropapi.NodeCollection, auth.Id)
	if err != nil || node.TokenKey() != auth.TokenKey() || node.GetString("state") == dropapi.NodeDisabled {
		return Forbidden("The backup node is no longer authorized.")
	}
	return nil
}
