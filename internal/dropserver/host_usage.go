package dropserver

import (
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/usage"
)

// Host services: limits sent to the node and usage taken from its reports
// (docs/agent-api.md "Host services").

// hostLimits maps a managed Crew app's limits for the node's S3 proxy: the
// stored-bytes quota and monthly download budget are the app's effective
// hard limits (usage.Effective of its usage_limits override, times
// usage.HardFactor), which the collector otherwise enforces only once the
// access logs arrive. Rate, burst and object size stay the node's defaults.
func hostLimits(record *core.Record) *dropapi.HostLimits {
	l := usage.Effective(appLimitsOverride(record))
	out := &dropapi.HostLimits{
		StoredBytesQuota: min(l.Hard(usage.MetricStored), dropapi.MaxHostBytes),
		MonthlyBytesOut:  min(l.Hard(usage.MetricBytesOut), dropapi.MaxHostBytes),
	}
	if !out.Valid() {
		return nil
	}
	return out
}

// checkHostUsage validates an app report's usage.
func checkHostUsage(a *dropapi.AppReport, field string) error {
	u := a.Usage
	if u == nil {
		return nil
	}
	if u.DeploymentID == "" || u.DeploymentID != a.DeploymentID || !appspec.DeploymentIDOK(a.AppID, u.DeploymentID) {
		return reportInvalid(field+".usage.deploymentId", "usage.deploymentId must be the app report's deploymentId.")
	}
	if u.Seq < 1 || u.Seq > dropapi.MaxUsageCounter || !u.Counters.Valid() {
		return reportInvalid(field+".usage", "usage seq and counters are out of range.")
	}
	return nil
}

func storedUsage(row *core.Record, field string) dropapi.UsageCounters {
	var c dropapi.UsageCounters
	_ = row.UnmarshalJSONField(field, &c)
	return c
}

// hostUsageNews reports whether r carries usage the row has not taken: a
// new deployment, or a higher sequence of the same one.
func hostUsageNews(row *core.Record, r dropapi.AppReport) bool {
	if r.Usage == nil {
		return false
	}
	if row == nil {
		return true
	}
	return row.GetString("host_usage_deployment") != r.Usage.DeploymentID || int64(row.GetInt("host_usage_seq")) < r.Usage.Seq
}

// hostUsageDue reports whether usage news alone justifies a write: a new
// deployment at once, more of the same one every HeartbeatPersistEvery.
func hostUsageDue(row *core.Record, r dropapi.AppReport, now time.Time) bool {
	if !hostUsageNews(row, r) {
		return false
	}
	if row == nil || row.GetString("host_usage_deployment") != r.Usage.DeploymentID {
		return true
	}
	return now.Sub(row.GetDateTime("host_usage_at").Time()) >= HeartbeatPersistEvery
}

// applyHostUsage takes r's usage into row: the difference from the counters
// stored for the same deployment (all of them for a new deployment) is added
// to the running total. Counters that went down (a node that lost its usage
// file) restart the deployment's baseline without adding anything.
func applyHostUsage(row *core.Record, r dropapi.AppReport, now time.Time) {
	if !hostUsageNews(row, r) {
		return
	}
	u := r.Usage
	delta := u.Counters
	if row.GetString("host_usage_deployment") == u.DeploymentID {
		d, ok := u.Counters.Minus(storedUsage(row, "host_usage"))
		if !ok {
			d = dropapi.UsageCounters{}
		}
		delta = d
	}
	row.Set("host_usage_total", storedUsage(row, "host_usage_total").Plus(delta))
	row.Set("host_usage_deployment", u.DeploymentID)
	row.Set("host_usage_seq", u.Seq)
	row.Set("host_usage", u.Counters)
	row.Set("host_usage_at", now)
}
