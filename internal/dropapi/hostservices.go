package dropapi

// Host services (docs/agent.md "Host services"): the node's S3 proxy for
// guests that hold no AWS credential. The desired document carries each
// app's limits; the agent's report carries the proxy's usage counters of the
// app's current deployment.

// HostLimits bound one app's S3 traffic through the node's proxy. Zero
// StoredBytesQuota or MonthlyBytesOut means no limit; zero RPS, Burst or
// MaxObjectBytes means the node's default (20 req/s, burst 200, 100 MiB).
type HostLimits struct {
	RPS              int   `json:"rps"`
	Burst            int   `json:"burst"`
	MaxObjectBytes   int64 `json:"maxObjectBytes"`
	StoredBytesQuota int64 `json:"storedBytesQuota"`
	MonthlyBytesOut  int64 `json:"monthlyBytesOut"`
}

// Bounds of HostLimits.
const (
	MaxHostRPS         = 1000
	MaxHostBurst       = 10000
	MinHostObjectBytes = 1 << 20
	MaxHostObjectBytes = 5 << 30
	MaxHostBytes       = 1 << 50
)

// Valid reports whether every limit is zero or within its bounds.
func (l *HostLimits) Valid() bool {
	if l == nil {
		return true
	}
	return l.RPS >= 0 && l.RPS <= MaxHostRPS && l.Burst >= 0 && l.Burst <= MaxHostBurst &&
		(l.MaxObjectBytes == 0 || (l.MaxObjectBytes >= MinHostObjectBytes && l.MaxObjectBytes <= MaxHostObjectBytes)) &&
		l.StoredBytesQuota >= 0 && l.StoredBytesQuota <= MaxHostBytes && l.MonthlyBytesOut >= 0 && l.MonthlyBytesOut <= MaxHostBytes
}

// HostUsage is the proxy's monotonic counters of one deployment. Seq goes up
// by one with every change the node persisted; the server takes a report
// only when Seq is above the one it stored for the deployment, and adds the
// difference to the app's running total.
type HostUsage struct {
	DeploymentID string        `json:"deploymentId"`
	Seq          int64         `json:"seq"`
	Counters     UsageCounters `json:"counters"`
}

// UsageCounters are the proxy's counters: requests by class and payload
// bytes in (from the guest) and out (to the guest).
type UsageCounters struct {
	ReadRequests     int64 `json:"readRequests"`
	WriteRequests    int64 `json:"writeRequests"`
	ListRequests     int64 `json:"listRequests"`
	DeleteRequests   int64 `json:"deleteRequests"`
	RejectedRequests int64 `json:"rejectedRequests"`
	BytesIn          int64 `json:"bytesIn"`
	BytesOut         int64 `json:"bytesOut"`
}

// MaxUsageCounter bounds each counter.
const MaxUsageCounter = 1 << 62

// Valid reports whether every counter is within bounds.
func (c UsageCounters) Valid() bool {
	for _, v := range c.values() {
		if v < 0 || v > MaxUsageCounter {
			return false
		}
	}
	return true
}

func (c UsageCounters) values() []int64 {
	return []int64{c.ReadRequests, c.WriteRequests, c.ListRequests, c.DeleteRequests, c.RejectedRequests, c.BytesIn, c.BytesOut}
}

// Minus returns c - prev, or ok false when any counter went down.
func (c UsageCounters) Minus(prev UsageCounters) (UsageCounters, bool) {
	d := UsageCounters{c.ReadRequests - prev.ReadRequests, c.WriteRequests - prev.WriteRequests, c.ListRequests - prev.ListRequests,
		c.DeleteRequests - prev.DeleteRequests, c.RejectedRequests - prev.RejectedRequests, c.BytesIn - prev.BytesIn, c.BytesOut - prev.BytesOut}
	for _, v := range d.values() {
		if v < 0 {
			return UsageCounters{}, false
		}
	}
	return d, true
}

// Plus returns c + d (each capped at MaxUsageCounter).
func (c UsageCounters) Plus(d UsageCounters) UsageCounters {
	add := func(a, b int64) int64 { return min(a+b, MaxUsageCounter) }
	return UsageCounters{add(c.ReadRequests, d.ReadRequests), add(c.WriteRequests, d.WriteRequests), add(c.ListRequests, d.ListRequests),
		add(c.DeleteRequests, d.DeleteRequests), add(c.RejectedRequests, d.RejectedRequests), add(c.BytesIn, d.BytesIn), add(c.BytesOut, d.BytesOut)}
}
