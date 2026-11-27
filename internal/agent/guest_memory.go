package agent

import (
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"time"
)

// NibRunner uses one measurement timestamp for all guests probed in a pass,
// but retains previous readings when a probe fails. Omit incomplete or mixed
// passes rather than interpreting unmeasured guests as unused memory.
func guestMemory(r *nibReported, now time.Time) *dropapi.GuestMemory {
	reported, err := time.Parse(time.RFC3339Nano, r.ReportedAt)
	if err != nil || reported.After(now) || now.Sub(reported) > StaleReport || r.State != "ready" || r.Instances == nil {
		return nil
	}
	var measuredAt time.Time
	var bytes uint64
	var count int
	for _, inst := range r.Instances {
		switch inst.State {
		case "idle", "stopped", "failed":
			continue
		case "running", "starting", "stopping", "unhealthy", "pending":
		default:
			return nil
		}
		c := inst.Compute
		if c == nil || c.UsedBytes == nil || *c.UsedBytes > c.TotalBytes || c.TotalBytes == 0 || c.TotalBytes > 1<<48 || count >= dropapi.MaxReportApps {
			return nil
		}
		at, err := time.Parse(time.RFC3339Nano, c.MeasuredAt)
		if err != nil || at.After(reported) || now.Sub(at) > StaleReport || (!measuredAt.IsZero() && !at.Equal(measuredAt)) {
			return nil
		}
		measuredAt = at
		bytes += *c.UsedBytes
		count++
	}
	if measuredAt.IsZero() {
		measuredAt = reported
	}
	return &dropapi.GuestMemory{At: measuredAt.Unix(), UsedMiB: int((bytes + (1 << 20) - 1) >> 20), RunningVMs: count}
}
