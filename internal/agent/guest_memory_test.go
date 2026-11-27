package agent

import (
	"testing"
	"time"
)

func TestCompleteConcurrentMemory(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	used := uint64(1 << 19)
	reading := guestCompute{UsedBytes: &used, TotalBytes: 1 << 30, MeasuredAt: now.Format(time.RFC3339)}
	r := &nibReported{State: "ready", ReportedAt: now.Format(time.RFC3339), Instances: []nibInstance{
		{State: "running", Compute: &reading}, {State: "running", Compute: &reading}, {State: "idle", Compute: &reading},
	}}
	m := guestMemory(r, now)
	if m == nil || m.RunningVMs != 2 || m.UsedMiB != 1 || m.At != now.Unix() {
		t.Fatalf("aggregate: %+v", m)
	}
	old := reading
	old.MeasuredAt = now.Add(-time.Second).Format(time.RFC3339)
	r.Instances[0].Compute = &old
	if guestMemory(r, now) != nil {
		t.Fatal("combined different measurement passes")
	}
	r.Instances[0].Compute = nil
	if guestMemory(r, now) != nil {
		t.Fatal("lost guest counted as zero")
	}
	r.Instances = []nibInstance{}
	if m := guestMemory(r, now); m == nil || m.RunningVMs != 0 || m.UsedMiB != 0 {
		t.Fatalf("idle snapshot: %+v", m)
	}
	if guestMemory(r, now.Add(2*time.Minute)) != nil {
		t.Fatal("stale snapshot")
	}
	r.Instances = nil
	if guestMemory(r, now) != nil {
		t.Fatal("missing instances reported as idle")
	}
}
