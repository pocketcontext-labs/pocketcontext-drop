package dropserver

import (
	"fmt"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func storedVolumeUsage(row *core.Record) *dropapi.VolumeUsage {
	var u *dropapi.VolumeUsage
	if row != nil {
		_ = row.UnmarshalJSONField("volume_usage", &u)
	}
	return u
}

func newVolumeMeasurement(row *core.Record, u *dropapi.VolumeUsage, now time.Time, sampleAt int64) bool {
	if !u.Fresh(now) {
		return false
	}
	measured, _ := time.Parse(time.RFC3339Nano, u.MeasuredAt)
	if measured.Unix() > sampleAt {
		return false
	}
	prior := storedVolumeUsage(row)
	if !prior.Valid() {
		return true
	}
	previous, _ := time.Parse(time.RFC3339Nano, prior.MeasuredAt)
	return measured.After(previous)
}

func volumeDowngradeError(c *Ctx, a *core.Record, targetMiB int) error {
	rows, err := c.App.FindRecordsByFilter("node_reports", "app = {:app}", "", 1, 0, dbx.Params{"app": a.Id})
	if err != nil {
		return err
	}
	if len(rows) > 0 && rows[0].GetString("deployment_id") == deploymentID(a) {
		u := storedVolumeUsage(rows[0])
		if u.Fresh(agentStateOf(c.App).clock()) {
			if *u.UsedBytes > int64(targetMiB)<<20 {
				usedMiB := (*u.UsedBytes + (1 << 20) - 1) >> 20
				return Conflict(dropapi.CodeDiskLocked, fmt.Sprintf("Your files use %d MiB. This size provides %d MiB of disk. Choose a larger size.", usedMiB, targetMiB))
			}
			return Conflict(dropapi.CodeDiskLocked, "Measured disk usage is within this tier. This node cannot yet shrink an existing disk.")
		}
	}
	return Conflict(dropapi.CodeDiskLocked, "Current disk usage is unavailable. This node cannot yet shrink an existing disk.")
}
