package dropserver

import (
	"database/sql"
	"errors"
	"slices"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() {
	register("backup.list", backupList)
	register("backup.configure", backupConfigure)
	register("backup.create", backupCreate)
	register("backup.restore", backupRestore)
}

type backupInfo struct {
	ReadyAt  int64   `db:"ready_at" json:"ready_at,omitempty"`
	Rate     float64 `db:"-" json:"microcredits_per_gib_month,omitempty"`
	Markup   int64   `db:"markup" json:"-"`
	Exchange int64   `db:"exchange" json:"-"`
	Price    int64   `db:"price" json:"-"`

	ID      string `db:"id" json:"id"`
	App     string `db:"app" json:"app"`
	Name    string `db:"name" json:"name"`
	State   string `db:"state" json:"state"`
	Created int64  `db:"created" json:"created"`
	SHA256  string `db:"sha256" json:"sha256,omitempty"`
	Bytes   int64  `db:"bytes" json:"bytes"`
}

type backupPolicy struct {
	App    string `db:"app" json:"app"`
	NextAt int64  `db:"next_at" json:"next_at"`
}

type backupActivity struct {
	Error  string `db:"error" json:"error,omitempty"`
	ID     string `db:"id" json:"id"`
	Backup string `db:"backup_id" json:"backup"`
	App    string `db:"target" json:"app"`
	Kind   string `db:"kind" json:"kind"`
	State  string `db:"state" json:"state"`
}

func backupList(c *Ctx, _ *dropapi.MeInput) (any, error) {
	p := dbx.Params{"o": c.Auth.Id}
	backups := []backupInfo{}
	if err := c.App.DB().NewQuery(`SELECT b.id,b.app,b.name,b.state,b.created,b.sha256,b.bytes,COALESCE(m.ready_at,0) AS ready_at,
      COALESCE(m.markup,0) AS markup, COALESCE(m.exchange,0) AS exchange, COALESCE(m.price,0) AS price
      FROM drop_backups b LEFT JOIN drop_backup_meter m ON m.backup=b.id WHERE b.owner={:o} ORDER BY b.created DESC,b.id DESC LIMIT 500`).Bind(p).All(&backups); err != nil {
		return nil, err
	}
	for i := range backups {
		b := &backups[i]
		if b.Price > 0 {
			b.Rate = backupMonthlyRate(dropapi.CreditSettings{MarkupPPM: b.Markup, EURPerUSDPPM: b.Exchange, EuroMicrosPerCredit: b.Price})
		}
	}
	policies := []backupPolicy{}
	if err := c.App.DB().NewQuery("SELECT p.app,p.next_at FROM drop_backup_policies p JOIN apps a ON a.id=p.app WHERE a.owner={:o}").Bind(p).All(&policies); err != nil {
		return nil, err
	}
	jobs := []backupActivity{}
	if err := c.App.DB().NewQuery("SELECT j.id,j.backup_id,j.target,j.kind,j.state,j.error FROM drop_backup_jobs j JOIN drop_backups b ON b.id=j.backup_id WHERE b.owner={:o} ORDER BY j.created DESC,j.id DESC LIMIT 500").Bind(p).All(&jobs); err != nil {
		return nil, err
	}
	type eligibleApp struct {
		ID   string `db:"id" json:"id"`
		Name string `db:"name" json:"name"`
		Slug string `db:"slug" json:"slug"`
	}
	eligible := []eligibleApp{}
	if servicesOf(c.App).Backups != nil {
		err := c.App.DB().NewQuery(`SELECT a.id,a.name,a.slug FROM apps a JOIN drop_beta_probes probe ON probe.digest=a.official_target AND probe.contract={:contract} AND probe.state='valid'
		WHERE a.owner={:o} AND NOT a.archived AND NOT a.deleting AND a.official_product='crew'
		AND NOT EXISTS(SELECT 1 FROM drop_beta_catalog cat WHERE cat.digest=probe.digest AND NOT cat.approved)
		AND EXISTS(SELECT 1 FROM json_each(probe.manifest,'$.optional') WHERE value='platform_backup_v1') ORDER BY a.name,a.id`).
			Bind(dbx.Params{"o": c.Auth.Id, "contract": betaProbeContract}).All(&eligible)
		if err != nil {
			return nil, err
		}
	}
	var pricing backupQuote
	if !hostedAdmin(c.App, c.Auth) {
		var err error
		pricing, err = backupPricing(c.App, c.Auth.Id)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"pricing": pricing, "eligible_apps": eligible, "backups": backups, "policies": policies, "jobs": jobs}, nil
}

func backupSupported(app core.App, a *core.Record) error {
	if servicesOf(app).Backups == nil {
		return Conflict("backups_unavailable", "Backup storage is unavailable.")
	}
	if a.GetString("official_product") == "crew" && !a.GetBool("archived") && !a.GetBool("deleting") {
		m, err := betaRelease(app, a.GetString("official_target"))
		if err == nil && slices.Contains(m.Optional, "platform_backup_v1") {
			return nil
		}
	}
	return Conflict("backups_unsupported", "Choose an active Crew app with platform backup support.")
}

func backupConfigure(c *Ctx, in *dropapi.BackupPolicyInput) (any, error) {
	a, err := c.LoadOwned("apps", in.App, "owner", "App")
	if err != nil {
		return nil, err
	}
	p := dbx.Params{"a": a.Id, "t": time.Now().Add(24 * time.Hour).Unix()}
	if in.Enabled {
		if err = requireBackupFunds(c.App, a.GetString("owner")); err != nil {
			return nil, err
		}
		if err = backupSupported(c.App, a); err != nil {
			return nil, err
		}
		err = waitExec(c.App, "INSERT INTO drop_backup_policies(app,next_at) VALUES({:a},{:t}) ON CONFLICT(app) DO NOTHING", p)
	} else {
		err = waitExec(c.App, "DELETE FROM drop_backup_policies WHERE app={:a}", p)
		if err == nil {
			err = waitExec(c.App, "UPDATE drop_backup_jobs SET state='failed' WHERE target={:a} AND kind='capture' AND state='pending'", p)
		}
		if err == nil {
			err = waitExec(c.App, "UPDATE drop_backups SET state='failed' WHERE app={:a} AND state='pending'", p)
		}
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"enabled": in.Enabled}})
	return in, err
}

func backupCreate(c *Ctx, in *dropapi.BackupAppInput) (any, error) {
	a, err := c.LoadOwned("apps", in.App, "owner", "App")
	if err != nil {
		return nil, err
	}
	result, err := queueCapture(c.App, a)
	c.Event(Event{App: a.Id})
	return result, err
}

func queueCapture(app core.App, a *core.Record) (map[string]string, error) {
	if err := backupSupported(app, a); err != nil {
		return nil, err
	}
	digest := a.GetString("official_installed")
	if digest == "" || digest != a.GetString("official_target") {
		return nil, Conflict("app_updating", "Wait for the current Crew release to become healthy.")
	}
	var enabled int
	if err := app.DB().NewQuery("SELECT COUNT(*) FROM drop_backup_policies WHERE app={:a}").Bind(dbx.Params{"a": a.Id}).Row(&enabled); err != nil {
		return nil, err
	}
	if enabled == 0 {
		return nil, Conflict("backups_disabled", "Enable backups for this app first.")
	}
	if err := requireBackupFunds(app, a.GetString("owner")); err != nil {
		return nil, err
	}
	id := core.GenerateDefaultRandomId()
	err := waitExec(app, `INSERT INTO drop_backups(id,owner,app,source_app,name,release_sha,multiplier,state,created)
		VALUES({:b},{:o},{:a},{:s},{:n},{:r},{:m},'pending',unixepoch())`, dbx.Params{"b": id, "o": a.GetString("owner"), "a": a.Id, "s": a.GetString("nib_app_id"), "n": a.GetString("name"), "r": digest, "m": a.GetInt("memory_mib") / 256})
	if err != nil {
		return nil, err
	}
	return queueBackupJob(app, a, id, "capture")
}

func queueBackupJob(app core.App, a *core.Record, backup, kind string) (map[string]string, error) {
	p, err := placementOf(app, a.Id)
	if err != nil {
		return nil, err
	}
	if p == nil || a.GetString("desired_state") == "stopped" {
		return nil, Conflict("backup_app_unavailable", "This app must have an active placement.")
	}
	job := core.GenerateDefaultRandomId()
	r, err := app.DB().NewQuery(`INSERT INTO drop_backup_jobs(id,backup_id,kind,target,node,instance_id,state,created)
	VALUES({:j},{:b},{:k},{:a},{:n},{:d},'pending',unixepoch()) ON CONFLICT DO NOTHING`).Bind(dbx.Params{"j": job, "b": backup, "k": kind, "a": a.Id, "n": p.GetString("node"), "d": deploymentID(a)}).Execute()
	if err != nil {
		return nil, err
	}
	rows, err := r.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, Conflict("backup_busy", "Another backup operation is in progress.")
	}
	return map[string]string{"backup": backup, "job": job, "app": a.Id}, nil
}

func backupRestore(c *Ctx, in *dropapi.BackupRestoreInput) (any, error) {
	if (in.SameApp == "") == (in.NewName == "") {
		return nil, Invalid("backup", "Provide the original slug for replacement or the name for a new app.")
	}
	var b struct {
		App        string `db:"app"`
		Release    string `db:"release_sha"`
		Multiplier int    `db:"multiplier"`
	}
	err := c.App.DB().NewQuery("SELECT app,release_sha,multiplier FROM drop_backups WHERE id={:b} AND owner={:o} AND state='ready' AND created > unixepoch()-604800").Bind(dbx.Params{"b": in.Backup, "o": c.Auth.Id}).One(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFound("Backup")
	}
	if err != nil {
		return nil, err
	}
	target := b.App
	if in.NewName != "" {
		out, err := workspaceCreate(c, &dropapi.WorkspaceCreate{Product: "crew", SHA256: b.Release, Name: in.NewName, Multiplier: b.Multiplier, AllowArchived: true})
		if err != nil {
			return nil, err
		}
		target = out.(dropapi.AppResult).App.ID
	}
	a, err := c.LoadOwned("apps", target, "owner", "App")
	if err != nil {
		return nil, err
	}
	if in.SameApp != "" && a.GetString("slug") != in.SameApp {
		return nil, Invalid("same_app", "Enter the original app slug to confirm replacement.")
	}
	if err = backupSupported(c.App, a); err != nil {
		return nil, err
	}
	if a.GetString("official_target") != b.Release {
		return nil, Conflict("backup_release_mismatch", "Restore into a new app with the matching Crew release.")
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"backup": in.Backup}})
	return queueBackupJob(c.App, a, in.Backup, "restore")
}
