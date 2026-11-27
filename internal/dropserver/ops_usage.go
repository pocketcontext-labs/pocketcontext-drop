package dropserver

import (
	"encoding/json"
	"errors"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/usage"
)

// Usage operations: usage.get (an account's own apps, admins any),
// usage.set_limits (admins, any app) and usage.collect (admins).

func init() {
	register("usage.get", usageGet)
	register("usage.set_limits", usageSetLimits)
	register("usage.collect", usageCollect)
}

func usageGet(c *Ctx, in *dropapi.UsageGetInput) (any, error) {
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	month, monthStart, _ := monthBounds(now)
	out := dropapi.Usage{App: app.Id, NibAppID: app.GetString("nib_app_id"), Month: month,
		Hourly: []dropapi.UsageHour{}, Limits: []dropapi.UsageLimit{}, Exceeded: []dropapi.UsageExceeded{},
		Metered:      servicesOf(c.App).metering(),
		EstimateNote: "Estimated S3 cost of the month to date at list prices (Class A $0.005 per 1,000, Class B $0.0004 per 1,000, egress $0.09 per GB, storage $0.023 per GB-month). An estimate, not a bill."}
	reported, err := c.App.FindRecordsByFilter("node_reports", "app={:app}", "", 0, 0, dbx.Params{"app": app.Id})
	if err != nil {
		return nil, err
	}
	for _, row := range reported {
		out.BudgetRefusals = min(dropapi.MaxUsageCounter, out.BudgetRefusals+storedUsage(row, "host_usage_total").BudgetRefusals)
	}
	since := usage.HourOf(now.Add(-23 * time.Hour))
	hours, err := c.App.FindRecordsByFilter("app_usage_hourly", "app = {:app} && hour >= {:since}", "hour", 0, 0,
		dbx.Params{"app": app.Id, "since": since})
	if err != nil {
		return nil, err
	}
	for _, h := range hours {
		out.Hourly = append(out.Hourly, dropapi.UsageHour{Hour: h.GetString("hour"), Counts: hourlyCounts(h)})
	}
	periods, err := monthUsage(c.App, now, app.Id)
	if err != nil {
		return nil, err
	}
	period := usage.Period{}
	if p := periods[app.Id]; p != nil {
		period = *p
	}
	out.MonthToDate = period.Counts
	if daily, err := c.App.FindRecordsByFilter("app_usage_daily", "app = {:app}", "-day", 1, 0, dbx.Params{"app": app.Id}); err == nil && len(daily) == 1 {
		d := daily[0]
		out.Stored = &dropapi.UsageStored{Day: d.GetString("day"), Stored: usage.Stored{
			CurrentBytes: int64(d.GetFloat("current_bytes")), NoncurrentBytes: int64(d.GetFloat("noncurrent_bytes")),
			Objects: int64(d.GetFloat("objects")), Versions: int64(d.GetFloat("versions"))}}
	}
	out.LimitsOverride = appLimitsOverride(app)
	limits := usage.Effective(out.LimitsOverride)
	for _, m := range usage.Metrics {
		v := period.Value(m)
		out.Limits = append(out.Limits, dropapi.UsageLimit{Metric: m, Label: usage.Label(m), Value: v,
			Soft: limits.Soft(m), Hard: limits.Hard(m), Level: usage.Level(v, limits, m)})
	}
	out.Estimate = usage.EstimateCost(period)
	events, err := c.App.FindRecordsByFilter("events", "app = {:app} && action = 'app.usage_exceeded' && created >= {:start}", "-created", 50, 0,
		dbx.Params{"app": app.Id, "start": monthStartDate(monthStart)})
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		var d struct {
			Metric, Level, Message string
			Stopped                bool
		}
		_ = json.Unmarshal([]byte(e.GetString("detail")), &d)
		out.Exceeded = append(out.Exceeded, dropapi.UsageExceeded{Metric: d.Metric, Level: d.Level, Stopped: d.Stopped, Message: d.Message, Created: dateString(e, "created")})
	}
	return out, nil
}

// monthStartDate turns an hour key (2006-01-02T15:00:00Z) into PocketBase's
// stored date form for comparisons with autodate fields.
func monthStartDate(hour string) string {
	t, err := time.Parse(time.RFC3339, hour)
	if err != nil {
		return hour
	}
	return t.UTC().Format("2006-01-02 15:04:05.000Z")
}

func usageSetLimits(c *Ctx, in *dropapi.UsageSetLimitsInput) (any, error) {
	app, err := c.findApp(in.App, false) // admins only (the spec's role): any app
	if err != nil {
		return nil, err
	}
	before := appLimitsOverride(app)
	next := before
	if in.Reset {
		next = usage.Override{}
	}
	fields := []struct {
		name  string
		value *int64
		into  *int64
	}{
		{"requests_a", in.RequestsA, &next.RequestsA}, {"requests_b", in.RequestsB, &next.RequestsB},
		{"bytes_out", in.BytesOut, &next.BytesOut}, {"stored_bytes", in.StoredBytes, &next.StoredBytes},
	}
	changed := in.Reset
	for _, f := range fields {
		if f.value == nil {
			continue
		}
		if *f.value < 1 || *f.value > usage.MaxLimit {
			return nil, Invalid(f.name, f.name+" must be a soft limit from 1 to 2^50.")
		}
		*f.into = *f.value
		changed = true
	}
	if !changed {
		return nil, Invalid("app", "Give at least one limit, or reset:true.")
	}
	app.Set("usage_limits", next)
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"before": before, "after": next, "reset": in.Reset}})
	return dropapi.UsageSetLimitsResult{App: app.Id, Override: next, Limits: usage.Effective(next)}, nil
}

// usageCollect runs the collector once (Spec.Standalone: outside the
// framework's transaction; the collector commits per log object) and writes
// one usage.collect event with its counts.
func usageCollect(c *Ctx, _ *dropapi.UsageCollectInput) (any, error) {
	if !servicesOf(c.App).metering() {
		return nil, errUsageUnavailable()
	}
	r, err := CollectUsage(c.Request.Context(), c.App, time.Now())
	if err != nil {
		var e *dropapi.Error
		if errors.As(err, &e) {
			return nil, e
		}
		c.App.Logger().Error("Drop usage collection failed", "error", awsx.ErrorCode(err))
		return nil, &dropapi.Error{Status: 502, Code: dropapi.CodeUsageCollectFailed, Message: "Reading the usage logs failed (" + awsx.ErrorCode(err) + "). Nothing was counted twice; retry later."}
	}
	if err := WriteEvent(c.App, EventRecord{Actor: c.Auth.Id, Action: "usage.collect", Detail: map[string]any{
		"objects_processed": r.ObjectsProcessed, "objects_failed": r.ObjectsFailed, "lines": r.Lines, "malformed": r.Malformed,
		"hours_updated": r.HoursUpdated, "inventory_day": r.InventoryDay, "alerts": r.Alerts, "stopped": len(r.Stopped),
	}}); err != nil {
		return nil, err
	}
	return r, nil
}
