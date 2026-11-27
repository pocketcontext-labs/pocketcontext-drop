package dropserver_test

import (
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestNodeMemoryHistory(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, _, token := h.Node("memory-node", hostA)
	_, user := h.User("memory@example.test", "user")
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	dropserver.SetAgentClock(h.App, func() time.Time { return now })
	write := func(used, count int, at time.Time) {
		t.Helper()
		r := reportOf(hostA)
		host := r["nibrunner"].(map[string]any)
		host["reportedAt"] = now.Format(time.RFC3339)
		host["guestMemory"] = map[string]any{"used_mib": used, "running_vms": count, "at": at.Unix()}
		if code, out := report(t, h, token, r); code != 200 {
			t.Fatalf("report %d: %v", code, out)
		}
	}
	history := func() []any {
		t.Helper()
		return h.MustOp(admin, "node.list", nil)["nodes"].([]any)[0].(map[string]any)["memory_history"].([]any)
	}
	if rows := history(); len(rows) != 0 {
		t.Fatal(rows)
	}
	write(512, 2, now)
	now = now.Add(30 * time.Second)
	peakAt := now
	write(900, 3, now)
	now = now.Add(30 * time.Second)
	write(440, 5, now)
	now = now.Add(time.Hour)
	write(0, 0, now)
	write(9999, 1, now.Add(-4*time.Minute))
	rows := history()
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	peak := rows[0].(map[string]any)
	if num(peak["used_mib"]) != 900 || num(peak["running_vms"]) != 3 || int64(num(peak["at"])) != peakAt.Unix() {
		t.Fatal(peak)
	}
	wantError(t, h.Op(user, "node.list", nil), 403, "forbidden")
	now = peakAt.Add(24 * time.Hour)
	rows = history()
	if len(rows) != 2 || num(rows[0].(map[string]any)["used_mib"]) != 440 {
		t.Fatalf("rolling boundary: %v", rows)
	}
}
