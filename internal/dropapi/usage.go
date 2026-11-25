package dropapi

import "github.com/pocketcontext-labs/pocketcontext-drop/internal/usage"

// Usage operations: an app's S3 usage (usage.get), its limit overrides
// (usage.set_limits, admins) and a one-off collector run (usage.collect,
// admins). The collector and the limits are internal/dropserver/usage.go.

// UsageGetInput names the app whose usage to read.
type UsageGetInput struct {
	App string `json:"app"`
}

// UsageHour is one app's usage in one UTC hour.
type UsageHour struct {
	Hour string `json:"hour"`
	usage.Counts
}

// UsageStored is one app's storage from the latest inventory report.
type UsageStored struct {
	Day string `json:"day"`
	usage.Stored
}

// UsageLimit is one monthly limit and where the app stands against it.
type UsageLimit struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Value  int64  `json:"value"`
	Soft   int64  `json:"soft"`
	Hard   int64  `json:"hard"`
	Level  string `json:"level"` // ok, soft or hard
}

// UsageExceeded is one app.usage_exceeded event of the month.
type UsageExceeded struct {
	Metric  string `json:"metric"`
	Level   string `json:"level"`
	Stopped bool   `json:"stopped"`
	Message string `json:"message"`
	Created string `json:"created"`
}

// Usage is the answer of usage.get: the last 24 hours by hour (oldest
// first, hours without requests left out), the month to date, the latest
// storage, the limits and an estimated cost (USD, an estimate, never a
// bill).
type Usage struct {
	App            string          `json:"app"`
	NibAppID       string          `json:"nib_app_id"`
	Month          string          `json:"month"` // YYYY-MM, UTC
	Hourly         []UsageHour     `json:"hourly"`
	MonthToDate    usage.Counts    `json:"month_to_date"`
	Stored         *UsageStored    `json:"stored"`
	Limits         []UsageLimit    `json:"limits"`
	LimitsOverride usage.Override  `json:"limits_override"`
	Estimate       usage.Estimate  `json:"estimate"`
	EstimateNote   string          `json:"estimate_note"`
	Exceeded       []UsageExceeded `json:"exceeded"`
	Metered        bool            `json:"metered"` // the server runs the usage collector
}

// UsageSetLimitsInput overrides an app's monthly soft limits (hard limits are
// ten times the soft ones). A zero or absent field keeps the current value;
// reset:true returns every limit to the server default first.
type UsageSetLimitsInput struct {
	App         string `json:"app"`
	RequestsA   *int64 `json:"requests_a,omitempty"`
	RequestsB   *int64 `json:"requests_b,omitempty"`
	BytesOut    *int64 `json:"bytes_out,omitempty"`
	StoredBytes *int64 `json:"stored_bytes,omitempty"`
	Reset       bool   `json:"reset,omitempty"`
}

// UsageSetLimitsResult is the app's override and effective limits.
type UsageSetLimitsResult struct {
	App      string         `json:"app"`
	Override usage.Override `json:"override"`
	Limits   usage.Limits   `json:"limits"`
}

// UsageCollectInput runs the collector once.
type UsageCollectInput struct{}

// UsageCollectResult is what one collector run did.
type UsageCollectResult struct {
	ObjectsListed    int      `json:"objects_listed"`
	ObjectsProcessed int      `json:"objects_processed"`
	ObjectsFailed    int      `json:"objects_failed"`
	Lines            int      `json:"lines"`
	Malformed        int      `json:"malformed"`
	Unattributed     int      `json:"unattributed"`
	Ignored          int      `json:"ignored"`
	UnknownApps      int      `json:"unknown_apps"`
	HoursUpdated     int      `json:"hours_updated"`
	Inventory        string   `json:"inventory,omitempty"` // the manifest read in this run
	InventoryDay     string   `json:"inventory_day,omitempty"`
	InventoryApps    int      `json:"inventory_apps"`
	FiltersCreated   int      `json:"filters_created"`
	FiltersDeleted   int      `json:"filters_deleted"`
	FiltersFailed    int      `json:"filters_failed"`
	Alerts           int      `json:"alerts"`
	Stopped          []string `json:"stopped"`
	Errors           []string `json:"errors"`
}

// Usage error codes.
const (
	CodeUsageUnavailable = "usage_unavailable"
)
