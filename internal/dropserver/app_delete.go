package dropserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() { register("app.delete", deleteApp) }

func deletionPending(app core.App, id string) (bool, error) {
	var pending bool
	err := app.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_app_deletions WHERE app={:app})").Bind(dbx.Params{"app": id}).Row(&pending)
	return pending, err
}

func deleteApp(c *Ctx, in *dropapi.AppDeleteInput) (any, error) {
	// Record IDs keep an old confirmation from ever deleting a reused slug.
	if !recordID.MatchString(in.App) {
		return nil, Invalid("app", "Use the immutable app ID.")
	}
	params := dbx.Params{"app": in.App, "slug": in.ConfirmSlug, "owner": c.Auth.Id, "admin": c.Admin}
	var replay bool
	if err := c.App.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_app_deletions WHERE app={:app} AND slug={:slug} AND (owner={:owner} OR {:admin}))").Bind(params).Row(&replay); err != nil {
		return nil, err
	}
	if replay {
		return map[string]bool{"accepted": true}, nil
	}
	a, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	if in.ConfirmSlug != a.GetString("slug") {
		return nil, Invalid("confirm_slug", "Type the app slug exactly.")
	}
	params = dbx.Params{"app": a.Id, "owner": a.GetString("owner"), "slug": in.ConfirmSlug, "nib": a.GetString("nib_app_id")}
	if err := waitExec(c.App, "INSERT INTO drop_app_deletions(app,owner,slug,nib) VALUES ({:app},{:owner},{:slug},{:nib})", params); err != nil {
		return nil, err
	}
	// Include historical nodes. Moving or archiving a volume preserves it.
	if err := waitExec(c.App, `INSERT INTO drop_volume_deletions(node,nib)
 SELECT id, {:nib} FROM nodes WHERE id IN (
 SELECT node FROM placements WHERE app={:app}
 UNION SELECT node FROM node_reports WHERE app={:app}
 UNION SELECT json_extract(detail,'$.node') FROM events WHERE app={:app} AND action IN ('placement.set','placement.clear')
 UNION SELECT json_extract(detail,'$.previous_node') FROM events WHERE app={:app} AND action='placement.set')`, params); err != nil {
		return nil, err
	}
	// A top-up must never restart an app after its owner confirms deletion.
	if _, err := c.App.DB().Delete("drop_credit_holds", dbx.HashExp{"app": a.Id}).Execute(); err != nil {
		return nil, err
	}
	a.Set("archived", true)
	a.Set("archived_at", time.Now().UTC())
	a.Set("desired_state", "stopped")
	if err = c.App.Save(a); err != nil {
		return nil, err
	}
	c.planMetricsFilter(a)
	c.Event(Event{App: a.Id, SubjectUser: a.GetString("owner"), Detail: map[string]any{"slug": in.ConfirmSlug}})
	return map[string]bool{"accepted": true}, nil
}

var releaseBlobMu sync.Mutex

func runAppCleanup(ctx context.Context, app core.App) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		var ids []string
		err := app.DB().NewQuery(`SELECT app FROM drop_app_deletions d WHERE completed=0
 AND NOT EXISTS(SELECT 1 FROM drop_volume_deletions v WHERE v.nib=d.nib) ORDER BY app`).Column(&ids)
		if err != nil {
			app.Logger().Error("Could not read app deletion queue")
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			if err := finishAppDeletion(ctx, app, id); err != nil {
				app.Logger().Error("App deletion pending", "app", id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func finishAppDeletion(ctx context.Context, app core.App, id string) error {
	releaseBlobMu.Lock()
	defer releaseBlobMu.Unlock()
	return app.RunInTransaction(func(tx core.App) error {
		var eligible bool
		p := dbx.Params{"app": id}
		if err := tx.DB().NewQuery(`SELECT EXISTS(SELECT 1 FROM drop_app_deletions d WHERE app={:app} AND completed=0
  AND NOT EXISTS (SELECT 1 FROM drop_volume_deletions v WHERE v.nib=d.nib))`).Bind(p).Row(&eligible); err != nil || !eligible {
			return err
		}
		a, err := tx.FindRecordById("apps", id)
		if err != nil {
			return err
		}
		s := servicesOf(tx)
		purger, ok := s.Releases.(awsx.AppPurger)
		if !ok {
			return fmt.Errorf("app deletion store unavailable")
		}
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if s.CrewReplicaBucket != "" {
			if err := purger.PurgeReplica(call, s.CrewReplicaBucket, a.GetString("nib_app_id")); err != nil {
				return err
			}
		}
		var hashes []string
		if err := tx.DB().NewQuery(`SELECT DISTINCT sha256 FROM releases r WHERE app={:app}
   AND NOT EXISTS(SELECT 1 FROM releases other WHERE other.sha256=r.sha256 AND other.app!=r.app)`).Bind(p).Column(&hashes); err != nil {
			return err
		}
		for _, digest := range hashes {
			if err := purger.PurgeRelease(call, digest); err != nil {
				return err
			}
		}
		names, err := secretRecords(tx, a)
		if err != nil {
			return err
		}
		for _, name := range names {
			param, err := awsx.SecretParameter(s.SSMPrefix, a.GetString("nib_app_id"), name.GetString("name"))
			if err != nil {
				return err
			}
			if s.Secrets == nil {
				return secretsUnavailable()
			}
			if err = s.Secrets.Delete(call, param); err != nil {
				return err
			}
		}
		// Keep an immutable app ID in the event detail after PocketBase clears the relation.
		if err := waitExec(tx, "UPDATE events SET detail=json_set(detail,'$.deleted_app',{:app}) WHERE app={:app}", p); err != nil {
			return err
		}
		a.Set("current_release", "")
		if err := tx.Save(a); err != nil {
			return err
		}
		for _, collection := range []string{"upload_links", "placements", "node_reports", "app_usage_hourly", "app_usage_daily", "app_secret_names", "releases"} {
			records, err := tx.FindAllRecords(collection, dbx.HashExp{"app": id})
			if err != nil {
				return err
			}
			for _, r := range records {
				if err := tx.Delete(r); err != nil {
					return err
				}
			}
		}
		if err := tx.Delete(a); err != nil {
			return err
		}
		return waitExec(tx, "UPDATE drop_app_deletions SET completed=1 WHERE app={:app}", p)
	})
}
