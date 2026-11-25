package dropserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/usage"
)

// The usage collector (S3 metering, Phase 1). Every 15 minutes, and on
// usage.collect, it:
//
//  1. lists the replica bucket's server access-log objects of the last three
//     UTC days (partitioned keys <prefix>YYYY/MM/DD/...), skips the objects
//     already in the private table drop_usage_log_objects (the cursor) and
//     adds each new object's records to app_usage_hourly per app and UTC
//     hour, together with its cursor row in one transaction: a crash or a
//     second run never counts an object twice;
//  2. reads the newest S3 Inventory report into app_usage_daily (replacing
//     that day's rows, so a rerun is harmless);
//  3. reconciles the per-app request-metrics filters of the replica bucket
//     (one per Crew app that is not archived);
//  4. checks each app's month against its limits: one app.usage_exceeded
//     event per limit, level and month (drop_usage_alerts), and at a hard
//     limit the app is stopped once through the ordinary stop path.
//
// Access-log keys carry the day of their records but not an order within
// it, which is why the cursor is the set of processed keys rather than a
// position. Rows older than the listing window are pruned.

const (
	usageInterval      = 15 * time.Minute
	usageLookbackDays  = 3
	usageKeepProcessed = 10 * 24 * time.Hour
	usageStoreTimeout  = 2 * time.Minute
	// usageStoredMaxAge is how old the latest inventory may be to count
	// against the storage limit.
	usageStoredMaxAge = 7 * 24 * time.Hour
	usageSource       = "s3log"
)

// usageRunMu keeps one collector run at a time per process.
var usageRunMu sync.Mutex

func runUsageCollector(ctx context.Context, app core.App) {
	if !servicesOf(app).metering() {
		return
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r, err := CollectUsage(ctx, app, time.Now())
		if err != nil && ctx.Err() == nil {
			app.Logger().Error("Drop usage collection failed", "error", awsx.ErrorCode(err))
		} else if err == nil {
			app.Logger().Info("Drop usage collected", "objects", r.ObjectsProcessed, "failed", r.ObjectsFailed,
				"lines", r.Lines, "malformed", r.Malformed, "alerts", r.Alerts, "stopped", len(r.Stopped))
		}
		timer.Reset(usageInterval)
	}
}

// CollectUsage runs the collector once at now. It fails only when the log
// listing fails; a failing object, inventory or filter is counted in the
// result and retried on the next run.
func CollectUsage(ctx context.Context, app core.App, now time.Time) (dropapi.UsageCollectResult, error) {
	usageRunMu.Lock()
	defer usageRunMu.Unlock()
	r := dropapi.UsageCollectResult{Stopped: []string{}, Errors: []string{}}
	s := servicesOf(app)
	if !s.metering() {
		return r, errUsageUnavailable()
	}
	now = now.UTC()
	apps, err := appIDs(app)
	if err != nil {
		return r, err
	}
	for d := usageLookbackDays - 1; d >= 0; d-- {
		prefix := s.UsageConfig.LogPrefix + now.AddDate(0, 0, -d).Format("2006/01/02/")
		call, cancel := context.WithTimeout(ctx, usageStoreTimeout)
		objects, err := s.Usage.ListObjects(call, prefix)
		cancel()
		if err != nil {
			return r, err
		}
		r.ObjectsListed += len(objects)
		for _, o := range objects {
			if ctx.Err() != nil {
				return r, ctx.Err()
			}
			if err := collectLogObject(ctx, app, s.Usage, o.Key, apps, now, &r); err != nil {
				r.ObjectsFailed++
				r.Errors = appendError(r.Errors, "log object: "+awsx.ErrorCode(err))
				app.Logger().Error("Drop usage log object failed", "error", awsx.ErrorCode(err))
			}
		}
	}
	if s.UsageConfig.InventoryPrefix != "" {
		if err := collectInventory(ctx, app, s, apps, &r); err != nil {
			r.Errors = appendError(r.Errors, "inventory: "+awsx.ErrorCode(err))
			app.Logger().Error("Drop usage inventory failed", "error", awsx.ErrorCode(err))
		}
	}
	if s.metricsFilters() {
		if err := reconcileMetricsFilters(ctx, app, s.Usage, &r); err != nil {
			r.Errors = appendError(r.Errors, "metrics filters: "+awsx.ErrorCode(err))
		}
	}
	if err := checkUsageLimits(app, now, &r); err != nil {
		return r, err
	}
	cutoff := now.Add(-usageKeepProcessed).Format(time.RFC3339)
	if _, err := app.DB().NewQuery("DELETE FROM drop_usage_log_objects WHERE processed_at < {:cutoff}").Bind(dbx.Params{"cutoff": cutoff}).Execute(); err != nil {
		return r, err
	}
	return r, nil
}

func appendError(errs []string, e string) []string {
	if len(errs) < 20 && !slices.Contains(errs, e) {
		errs = append(errs, e)
	}
	return errs
}

func errUsageUnavailable() *dropapi.Error {
	return &dropapi.Error{Status: 503, Code: dropapi.CodeUsageUnavailable,
		Message: "This server does not meter usage (set " + awsx.EnvUsageLogBucket + " and " + awsx.EnvUsageLogPrefix + ")."}
}

// appIDs maps nibrunner app ids to apps record ids.
func appIDs(app core.App) (map[string]string, error) {
	var rows []struct {
		ID  string `db:"id"`
		Nib string `db:"nib_app_id"`
	}
	if err := app.DB().NewQuery("SELECT id, nib_app_id FROM apps").All(&rows); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Nib] = r.ID
	}
	return out, nil
}

func logObjectProcessed(app core.App, key string) (bool, error) {
	var done bool
	err := app.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_usage_log_objects WHERE key = {:key})").Bind(dbx.Params{"key": key}).Row(&done)
	return done, err
}

// collectLogObject adds one new log object to the hourly rows.
func collectLogObject(ctx context.Context, app core.App, store awsx.LogReader, key string, apps map[string]string, now time.Time, r *dropapi.UsageCollectResult) error {
	if done, err := logObjectProcessed(app, key); err != nil || done {
		return err
	}
	call, cancel := context.WithTimeout(ctx, usageStoreTimeout)
	defer cancel()
	body, err := store.OpenObject(call, key)
	if err != nil {
		return err
	}
	summary, err := usage.ParseLog(body)
	body.Close()
	if err != nil {
		return err
	}
	applied := false
	hours := 0
	unknown := 0
	err = app.RunInTransaction(func(tx core.App) error {
		// Checked again inside the write transaction: a concurrent run that
		// committed the object first wins.
		if done, err := logObjectProcessed(tx, key); err != nil || done {
			return err
		}
		if _, err := tx.DB().Insert("drop_usage_log_objects", dbx.Params{
			"key": key, "processed_at": now.Format(time.RFC3339), "lines": summary.Lines, "malformed": summary.Malformed,
		}).Execute(); err != nil {
			return err
		}
		for k, c := range summary.Hours {
			id, ok := apps[k.App]
			if !ok {
				unknown++
				continue
			}
			if err := addHourly(tx, id, k.Hour, *c); err != nil {
				return err
			}
			hours++
		}
		applied = true
		return nil
	})
	if err != nil || !applied {
		return err
	}
	r.ObjectsProcessed++
	r.Lines += summary.Lines
	r.Malformed += summary.Malformed
	r.Unattributed += summary.Unattributed
	r.Ignored += summary.Ignored
	r.UnknownApps += unknown
	r.HoursUpdated += hours
	return nil
}

func addHourly(tx core.App, appID, hour string, c usage.Counts) error {
	record, err := tx.FindFirstRecordByFilter("app_usage_hourly", "app = {:app} && hour = {:hour} && source = {:source}",
		dbx.Params{"app": appID, "hour": hour, "source": usageSource})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		collection, err := tx.FindCachedCollectionByNameOrId("app_usage_hourly")
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("app", appID)
		record.Set("hour", hour)
		record.Set("source", usageSource)
	}
	c.Add(hourlyCounts(record))
	setHourly(record, c)
	return tx.Save(record)
}

func hourlyCounts(r *core.Record) usage.Counts {
	n := func(f string) int64 { return int64(r.GetFloat(f)) }
	return usage.Counts{ClassA: n("class_a"), ClassB: n("class_b"), Deletes: n("deletes"), BytesIn: n("bytes_in"),
		BytesOut: n("bytes_out"), Status4xx: n("status_4xx"), Status5xx: n("status_5xx"), Denied: n("denied")}
}

func setHourly(r *core.Record, c usage.Counts) {
	r.Set("class_a", c.ClassA)
	r.Set("class_b", c.ClassB)
	r.Set("deletes", c.Deletes)
	r.Set("bytes_in", c.BytesIn)
	r.Set("bytes_out", c.BytesOut)
	r.Set("status_4xx", c.Status4xx)
	r.Set("status_5xx", c.Status5xx)
	r.Set("denied", c.Denied)
}

func usageState(app core.App, name string) (string, error) {
	var value string
	err := app.DB().NewQuery("SELECT value FROM drop_usage_state WHERE name = {:name}").Bind(dbx.Params{"name": name}).Row(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// collectInventory reads the newest inventory report once.
func collectInventory(ctx context.Context, app core.App, s Services, apps map[string]string, r *dropapi.UsageCollectResult) error {
	call, cancel := context.WithTimeout(ctx, usageStoreTimeout)
	defer cancel()
	objects, err := s.Usage.ListObjects(call, s.UsageConfig.InventoryPrefix)
	if err != nil {
		return err
	}
	newest, day := "", ""
	for _, o := range objects {
		if d := usage.ManifestDay(o.Key); d != "" && o.Key > newest {
			newest, day = o.Key, d
		}
	}
	if newest == "" {
		return nil
	}
	if seen, err := usageState(app, "inventory_manifest"); err != nil || seen == newest {
		return err
	}
	body, err := s.Usage.OpenObject(call, newest)
	if err != nil {
		return err
	}
	manifest, err := usage.ParseManifest(body)
	body.Close()
	if err != nil {
		return err
	}
	inv := usage.NewInventory()
	for _, f := range manifest.Files {
		if !strings.HasPrefix(f.Key, s.UsageConfig.InventoryPrefix) {
			return fmt.Errorf("%w: inventory data file outside the inventory prefix", awsx.ErrInvalid)
		}
		data, err := s.Usage.OpenObject(call, f.Key)
		if err != nil {
			return err
		}
		err = inv.AddFile(io.LimitReader(data, 4<<30), manifest.FileSchema)
		data.Close()
		if err != nil {
			return err
		}
	}
	written := 0
	err = app.RunInTransaction(func(tx core.App) error {
		var previous []string
		if err := tx.DB().NewQuery("SELECT DISTINCT app FROM app_usage_daily").Column(&previous); err != nil {
			return err
		}
		ids := map[string]usage.Stored{}
		for nib, stored := range inv.Apps {
			if id, ok := apps[nib]; ok {
				ids[id] = *stored
			}
		}
		// An app that held objects before and none now stores nothing.
		for _, id := range previous {
			if _, ok := ids[id]; !ok {
				ids[id] = usage.Stored{}
			}
		}
		for id, stored := range ids {
			if err := setDaily(tx, id, day, stored); err != nil {
				return err
			}
			written++
		}
		_, err := tx.DB().NewQuery("INSERT INTO drop_usage_state (name, value) VALUES ('inventory_manifest', {:v}) ON CONFLICT(name) DO UPDATE SET value = excluded.value").
			Bind(dbx.Params{"v": newest}).Execute()
		return err
	})
	if err != nil {
		return err
	}
	r.Inventory, r.InventoryDay, r.InventoryApps = newest, day, written
	return nil
}

func setDaily(tx core.App, appID, day string, s usage.Stored) error {
	record, err := tx.FindFirstRecordByFilter("app_usage_daily", "app = {:app} && day = {:day}", dbx.Params{"app": appID, "day": day})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		collection, err := tx.FindCachedCollectionByNameOrId("app_usage_daily")
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("app", appID)
		record.Set("day", day)
	}
	record.Set("current_bytes", s.CurrentBytes)
	record.Set("noncurrent_bytes", s.NoncurrentBytes)
	record.Set("objects", s.Objects)
	record.Set("versions", s.Versions)
	return tx.Save(record)
}

// Per-app request-metrics filters.

// metricsFilterAction is a filter to create or remove after an operation
// commits.
type metricsFilterAction struct {
	App, ID string
	Create  bool
}

// planMetricsFilter asks for an app's filter after commit: created for a Crew
// app that is not archived, removed otherwise. Only when the server manages
// filters.
func (c *Ctx) planMetricsFilter(app *core.Record) {
	if !servicesOf(c.App).metricsFilters() || app.GetString("crew_enrollment") == "" {
		return
	}
	c.metricsFilter = &metricsFilterAction{App: app.Id, ID: app.GetString("nib_app_id"), Create: !app.GetBool("archived")}
}

// applyMetricsFilter applies one filter change, best effort, and records it
// as an app.metrics_filter event. A failure never undoes the operation; the
// collector retries on its next run.
func applyMetricsFilter(ctx context.Context, app core.App, actor string, a metricsFilterAction) {
	s := servicesOf(app)
	if !s.metricsFilters() || !awsx.ValidMetricsID(a.ID) {
		return
	}
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var err error
	action := "delete"
	if a.Create {
		action = "create"
		err = s.Usage.PutPrefixFilter(call, a.ID)
	} else {
		err = s.Usage.DeleteFilter(call, a.ID)
	}
	detail := map[string]any{"action": action, "id": a.ID, "prefix": a.ID + "/", "ok": err == nil}
	if err != nil {
		detail["error"] = awsx.ErrorCode(err)
		app.Logger().Error("Drop metrics filter failed", "action", action, "error", awsx.ErrorCode(err))
	}
	if werr := WriteEvent(app, EventRecord{Actor: actor, App: a.App, Action: "app.metrics_filter", Detail: detail}); werr != nil {
		app.Logger().Error("Drop metrics filter event failed")
	}
}

// reconcileMetricsFilters makes the bucket's app filters match the Crew apps:
// one per Crew app that is not archived; an archived Crew app's filter is
// removed. Filters whose id is no Drop app are left alone. Changes are
// recorded as events; failures are counted and logged, and retried next run.
func reconcileMetricsFilters(ctx context.Context, app core.App, store awsx.MetricsFilters, r *dropapi.UsageCollectResult) error {
	call, cancel := context.WithTimeout(ctx, usageStoreTimeout)
	defer cancel()
	existing, err := store.ListFilters(call)
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, f := range existing {
		have[f.ID] = f.Prefix
	}
	crew, err := app.FindRecordsByFilter("apps", "crew_enrollment != ''", "nib_app_id", 0, 0)
	if err != nil {
		return err
	}
	for _, a := range crew {
		id := a.GetString("nib_app_id")
		prefix, exists := have[id]
		want := !a.GetBool("archived")
		var action string
		switch {
		case want && (!exists || prefix != id+"/"):
			action, err = "create", store.PutPrefixFilter(call, id)
		case !want && exists:
			action, err = "delete", store.DeleteFilter(call, id)
		default:
			continue
		}
		if err != nil {
			r.FiltersFailed++
			app.Logger().Error("Drop metrics filter failed", "action", action, "error", awsx.ErrorCode(err))
			continue
		}
		if action == "create" {
			r.FiltersCreated++
		} else {
			r.FiltersDeleted++
		}
		if err := WriteEvent(app, EventRecord{App: a.Id, Action: "app.metrics_filter",
			Detail: map[string]any{"action": action, "id": id, "prefix": id + "/", "ok": true, "reconciled": true}}); err != nil {
			return err
		}
	}
	return nil
}

// Limits.

// appLimitsOverride reads an app's usage_limits override.
func appLimitsOverride(r *core.Record) usage.Override {
	var o usage.Override
	_ = r.UnmarshalJSONField("usage_limits", &o)
	return o
}

func monthBounds(now time.Time) (month, start, end string) {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first.Format("2006-01"), usage.HourOf(first), usage.HourOf(first.AddDate(0, 1, 0))
}

// monthUsage sums each app's hourly rows of the month and adds its latest
// stored bytes (an inventory at most a week old). appID limits it to one app.
func monthUsage(app core.App, now time.Time, appID string) (map[string]*usage.Period, error) {
	_, start, end := monthBounds(now)
	var rows []struct {
		App      string `db:"app"`
		ClassA   int64  `db:"class_a"`
		ClassB   int64  `db:"class_b"`
		Deletes  int64  `db:"deletes"`
		BytesIn  int64  `db:"bytes_in"`
		BytesOut int64  `db:"bytes_out"`
		S4       int64  `db:"s4"`
		S5       int64  `db:"s5"`
		Denied   int64  `db:"denied"`
	}
	params := dbx.Params{"start": start, "end": end, "app": appID}
	q := `SELECT app, SUM(class_a) AS class_a, SUM(class_b) AS class_b, SUM(deletes) AS deletes, SUM(bytes_in) AS bytes_in,
		SUM(bytes_out) AS bytes_out, SUM(status_4xx) AS s4, SUM(status_5xx) AS s5, SUM(denied) AS denied
		FROM app_usage_hourly WHERE hour >= {:start} AND hour < {:end} AND ({:app} = '' OR app = {:app}) GROUP BY app`
	if err := app.DB().NewQuery(q).Bind(params).All(&rows); err != nil {
		return nil, err
	}
	out := map[string]*usage.Period{}
	for _, r := range rows {
		out[r.App] = &usage.Period{Counts: usage.Counts{ClassA: r.ClassA, ClassB: r.ClassB, Deletes: r.Deletes, BytesIn: r.BytesIn,
			BytesOut: r.BytesOut, Status4xx: r.S4, Status5xx: r.S5, Denied: r.Denied}}
	}
	var stored []struct {
		App   string `db:"app"`
		Bytes int64  `db:"bytes"`
	}
	params["since"] = now.UTC().Add(-usageStoredMaxAge).Format("2006-01-02")
	q = `SELECT d.app AS app, d.current_bytes + d.noncurrent_bytes AS bytes FROM app_usage_daily d
		WHERE d.day >= {:since} AND ({:app} = '' OR d.app = {:app}) AND d.day = (SELECT MAX(day) FROM app_usage_daily WHERE app = d.app)`
	if err := app.DB().NewQuery(q).Bind(params).All(&stored); err != nil {
		return nil, err
	}
	for _, s := range stored {
		p := out[s.App]
		if p == nil {
			p = &usage.Period{}
			out[s.App] = p
		}
		p.Stored = s.Bytes
	}
	return out, nil
}

// checkUsageLimits raises the month's alerts: one app.usage_exceeded event
// per app, limit and level; at a hard limit the app stops, once.
func checkUsageLimits(app core.App, now time.Time, r *dropapi.UsageCollectResult) error {
	periods, err := monthUsage(app, now, "")
	if err != nil {
		return err
	}
	month, _, _ := monthBounds(now)
	ids := make([]string, 0, len(periods))
	for id := range periods {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := periods[id]
		record, err := app.FindRecordById("apps", id)
		if err != nil {
			continue
		}
		limits := usage.Effective(appLimitsOverride(record))
		for _, metric := range usage.Metrics {
			value := p.Value(metric)
			for _, level := range []string{usage.LevelSoft, usage.LevelHard} {
				limit := limits.Soft(metric)
				if level == usage.LevelHard {
					limit = limits.Hard(metric)
				}
				if value < limit {
					continue
				}
				stopped, raised, err := raiseUsageAlert(app, id, month, metric, level, value, limits)
				if err != nil {
					return err
				}
				if raised {
					r.Alerts++
				}
				if stopped {
					r.Stopped = append(r.Stopped, id)
				}
			}
		}
	}
	return nil
}

// raiseUsageAlert writes one alert and its event unless it was raised this
// month; at the hard level it stops the app in the same transaction.
func raiseUsageAlert(app core.App, appID, month, metric, level string, value int64, limits usage.Limits) (stopped, raised bool, err error) {
	err = app.RunInTransaction(func(tx core.App) error {
		res, err := tx.DB().NewQuery(`INSERT OR IGNORE INTO drop_usage_alerts (app, month, metric, level, created)
			VALUES ({:app}, {:month}, {:metric}, {:level}, {:created})`).Bind(dbx.Params{
			"app": appID, "month": month, "metric": metric, "level": level, "created": time.Now().UTC().Format(time.RFC3339),
		}).Execute()
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		raised = true
		record, err := tx.FindRecordById("apps", appID)
		if err != nil {
			return err
		}
		limit := limits.Soft(metric)
		message := fmt.Sprintf("%s reached %s in %s, past the soft limit of %s (the hard limit is %s).",
			usage.Label(metric), usage.Format(metric, value), month, usage.Format(metric, limit), usage.Format(metric, limits.Hard(metric)))
		if level == usage.LevelHard {
			limit = limits.Hard(metric)
			message = fmt.Sprintf("%s reached %s in %s, past the hard limit of %s.", usage.Label(metric), usage.Format(metric, value), month, usage.Format(metric, limit))
			if !record.GetBool("archived") && record.GetString("desired_state") != appspec.StateStopped {
				if err := stopApp(tx, record); err != nil {
					return err
				}
				stopped = true
				message += " Drop stopped the app automatically. Its data stays; start it again with app.start, or ask an admin to raise its limits. It will not be stopped again for this limit this month."
			}
		}
		if err := WriteEvent(tx, EventRecord{App: appID, Action: "app.usage_exceeded", Detail: map[string]any{
			"metric": metric, "level": level, "month": month, "value": value, "limit": limit,
			"soft": limits.Soft(metric), "hard": limits.Hard(metric), "stopped": stopped, "message": message,
		}}); err != nil {
			return err
		}
		return syncDesiredSeqs(tx)
	})
	return stopped, raised, err
}
