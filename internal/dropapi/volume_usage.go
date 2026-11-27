package dropapi

import (
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

// VolumeUsage is NibRunner's last reading of the guest data filesystem.
type VolumeUsage struct {
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  *int64 `json:"usedBytes"`
	MeasuredAt string `json:"measuredAt"`
}

func (u *VolumeUsage) Valid() bool {
	if u == nil || u.TotalBytes <= 0 || u.TotalBytes > int64(appspec.MaxDiskMiB)<<20 || u.UsedBytes == nil || *u.UsedBytes < 0 || *u.UsedBytes > u.TotalBytes || len(u.MeasuredAt) > 40 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, u.MeasuredAt)
	return err == nil
}

func (u *VolumeUsage) Fresh(now time.Time) bool {
	if !u.Valid() {
		return false
	}
	measured, _ := time.Parse(time.RFC3339Nano, u.MeasuredAt)
	return !measured.After(now) && now.Sub(measured) <= 5*time.Minute
}
