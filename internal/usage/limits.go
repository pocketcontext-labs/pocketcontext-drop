package usage

import (
	"fmt"
	"math"
)

// GB is the gigabyte S3 bills by (2^30 bytes).
const GB = int64(1) << 30

// Metric names of the monthly limits.
const (
	MetricRequestsA = "requests_a" // Class A + DELETE requests
	MetricRequestsB = "requests_b" // Class B requests
	MetricBytesOut  = "bytes_out"  // bytes sent
	MetricStored    = "stored"     // current + noncurrent bytes, latest inventory
)

// Metrics lists the limit metrics in display order.
var Metrics = []string{MetricRequestsA, MetricRequestsB, MetricBytesOut, MetricStored}

// HardFactor is how many times the soft limit the hard limit is.
const HardFactor = 10

// Limits are an app's soft limits per calendar month (decision "Standard").
// The hard limit of each is HardFactor times its soft limit.
type Limits struct {
	RequestsA   int64 `json:"requests_a"`
	RequestsB   int64 `json:"requests_b"`
	BytesOut    int64 `json:"bytes_out"`
	StoredBytes int64 `json:"stored_bytes"`
}

// DefaultLimits are the server-wide soft limits.
var DefaultLimits = Limits{RequestsA: 100_000, RequestsB: 1_000_000, BytesOut: 2 * GB, StoredBytes: 1 * GB}

// MaxLimit bounds an override (a petabyte, or 10^15 requests).
const MaxLimit = int64(1) << 50

// Soft returns the soft limit of a metric.
func (l Limits) Soft(metric string) int64 {
	switch metric {
	case MetricRequestsA:
		return l.RequestsA
	case MetricRequestsB:
		return l.RequestsB
	case MetricBytesOut:
		return l.BytesOut
	case MetricStored:
		return l.StoredBytes
	}
	return 0
}

// Hard returns the hard limit of a metric.
func (l Limits) Hard(metric string) int64 {
	soft := l.Soft(metric)
	if soft > math.MaxInt64/HardFactor {
		return math.MaxInt64
	}
	return soft * HardFactor
}

// Override is an app's per-metric soft limit overrides; zero keeps the
// default.
type Override = Limits

// Effective applies an app's overrides to the defaults.
func Effective(o Override) Limits {
	l := DefaultLimits
	if o.RequestsA > 0 {
		l.RequestsA = o.RequestsA
	}
	if o.RequestsB > 0 {
		l.RequestsB = o.RequestsB
	}
	if o.BytesOut > 0 {
		l.BytesOut = o.BytesOut
	}
	if o.StoredBytes > 0 {
		l.StoredBytes = o.StoredBytes
	}
	return l
}

// Period is an app's usage in one month: the summed hourly counts and the
// storage of the latest inventory.
type Period struct {
	Counts Counts
	Stored int64 // current + noncurrent bytes
}

// Value returns a metric's value in the period.
func (p Period) Value(metric string) int64 {
	switch metric {
	case MetricRequestsA:
		return p.Counts.ClassA + p.Counts.Deletes
	case MetricRequestsB:
		return p.Counts.ClassB
	case MetricBytesOut:
		return p.Counts.BytesOut
	case MetricStored:
		return p.Stored
	}
	return 0
}

// Levels of a metric against its limits.
const (
	LevelOK   = "ok"
	LevelSoft = "soft"
	LevelHard = "hard"
)

// Level returns ok, soft (at or past the soft limit) or hard (at or past the
// hard limit).
func Level(value int64, l Limits, metric string) string {
	switch {
	case value >= l.Hard(metric):
		return LevelHard
	case value >= l.Soft(metric):
		return LevelSoft
	}
	return LevelOK
}

// Label names a metric for people.
func Label(metric string) string {
	switch metric {
	case MetricRequestsA:
		return "Class A and DELETE requests"
	case MetricRequestsB:
		return "Class B requests"
	case MetricBytesOut:
		return "Bytes sent"
	case MetricStored:
		return "Stored bytes"
	}
	return metric
}

// Format renders a metric value for messages.
func Format(metric string, v int64) string {
	if metric == MetricBytesOut || metric == MetricStored {
		return fmt.Sprintf("%.2f GB", float64(v)/float64(GB))
	}
	return fmt.Sprintf("%d", v)
}

// Prices in USD (S3 Standard, eu-north-1 list prices, rounded): estimates
// only, never a bill.
const (
	PriceClassAPer1000 = 0.005
	PriceClassBPer1000 = 0.0004
	PriceEgressPerGB   = 0.09
	PriceStoragePerGB  = 0.023 // per GB-month
)

// Estimate is a period's estimated S3 cost in USD.
type Estimate struct {
	ClassA  float64 `json:"class_a_usd"`
	ClassB  float64 `json:"class_b_usd"`
	Egress  float64 `json:"egress_usd"`
	Storage float64 `json:"storage_usd"`
	Total   float64 `json:"total_usd"`
}

func round(v float64) float64 { return math.Round(v*10000) / 10000 }

// EstimateCost estimates a period's cost: DELETE is free, storage is the
// latest stored bytes for a whole month.
func EstimateCost(p Period) Estimate {
	e := Estimate{
		ClassA:  round(float64(p.Counts.ClassA) / 1000 * PriceClassAPer1000),
		ClassB:  round(float64(p.Counts.ClassB) / 1000 * PriceClassBPer1000),
		Egress:  round(float64(p.Counts.BytesOut) / float64(GB) * PriceEgressPerGB),
		Storage: round(float64(p.Stored) / float64(GB) * PriceStoragePerGB),
	}
	e.Total = round(e.ClassA + e.ClassB + e.Egress + e.Storage)
	return e
}
