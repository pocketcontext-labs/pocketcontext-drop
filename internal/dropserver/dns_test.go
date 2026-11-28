package dropserver_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dnsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

const tunnelA, tunnelB = "8cd529b8-e868-4fe0-bcf2-09283ea078af", "96f6de7c-950d-4fc4-8889-98ddc5afa598"

func nodeByName(t *testing.T, h *droptest.Hosted, admin, name string) map[string]any {
	t.Helper()
	for _, n := range h.MustOp(admin, "node.list", nil)["nodes"].([]any) {
		if node := n.(map[string]any); node["name"] == name {
			return node
		}
	}
	t.Fatalf("no node %s", name)
	return nil
}

func TestNodeTunnelAndPool(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, _, token := h.Node("node-one", hostA)
	r := reportOf(hostA)
	r["tunnel"] = "not-a-tunnel"
	if code, _ := report(t, h, token, r); code != http.StatusBadRequest {
		t.Fatalf("a malformed tunnel ID was accepted: %d", code)
	}
	r["tunnel"] = tunnelA
	if code, out := report(t, h, token, r); code != http.StatusOK {
		t.Fatal(code, out)
	}
	if n := nodeByName(t, h, admin, "node-one"); n["tunnel_id"] != tunnelA || n["pool"] != "" {
		t.Fatalf("node %v", n)
	}
	wantError(t, h.Op(admin, "node.set_pool", map[string]any{"node": "node-one", "pool": "everyone"}), 400, dropapi.CodeInvalidInput)
	if n := h.MustOp(admin, "node.set_pool", map[string]any{"node": "node-one", "pool": "beta"})["node"].(map[string]any); n["pool"] != "beta" {
		t.Fatalf("set_pool %v", n)
	}
	h.MustOp(admin, "node.set_pool", map[string]any{"node": "node-one", "pool": ""})
	if n := nodeByName(t, h, admin, "node-one"); n["pool"] != "" {
		t.Fatalf("pool not cleared: %v", n)
	}
	// A report without a tunnel clears it: the node no longer routes apps.
	if code, out := report(t, h, token, reportOf(hostA)); code != http.StatusOK {
		t.Fatal(code, out)
	}
	if n := nodeByName(t, h, admin, "node-one"); n["tunnel_id"] != "" {
		t.Fatalf("tunnel not cleared: %v", n)
	}
}

func TestDNSSyncPointsPlacedAppsAtTheirNodeTunnel(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	admin := h.Token(h.Owner())
	_, user := h.User("user@example.test", "developer")
	_, _, tokenA := h.Node("node-one", hostA)
	_, _, tokenB := h.Node("node-two", hostB)
	withTunnel := func(host, tunnel string) map[string]any {
		r := reportOf(host)
		if tunnel != "" {
			r["tunnel"] = tunnel
		}
		return r
	}
	report(t, h, tokenA, withTunnel(hostA, tunnelA))
	report(t, h, tokenB, withTunnel(hostB, ""))
	appOne := deployedApp(t, h, user, "app-one", 1)
	appTwo := deployedApp(t, h, user, "app-two", 2)
	h.MustOp(admin, "placement.set", map[string]any{"app": appOne, "node": "node-one"})
	h.MustOp(admin, "placement.set", map[string]any{"app": appTwo, "node": "node-two"})

	zone := dnsx.NewFake()
	foreign := "foreign." + droptest.AppDomain
	zone.Records["x"] = dnsx.Record{ID: "x", Name: foreign, Content: "old.cfargotunnel.com"}
	zone.Unmanaged["app-one."+droptest.AppDomain] = false
	sync := func() map[string]string {
		t.Helper()
		if err := dropserver.SyncDNSForTest(context.Background(), h.App, zone); err != nil {
			t.Fatal(err)
		}
		return zone.Names()
	}
	// Only the app on a node with a tunnel gets a record; a managed record
	// for a name no app holds is removed.
	got := sync()
	if len(got) != 1 || got["app-one."+droptest.AppDomain] != dnsx.Target(tunnelA) {
		t.Fatalf("records %v", got)
	}
	// A move follows the app; the other node gains a tunnel.
	report(t, h, tokenB, withTunnel(hostB, tunnelB))
	h.MustOp(admin, "placement.set", map[string]any{"app": appOne, "node": "node-two"})
	got = sync()
	if len(got) != 2 || got["app-one."+droptest.AppDomain] != dnsx.Target(tunnelB) || got["app-two."+droptest.AppDomain] != dnsx.Target(tunnelB) {
		t.Fatalf("records after move %v", got)
	}
	// A name with an unmanaged record is never taken over.
	h.MustOp(admin, "placement.clear", map[string]any{"app": appTwo})
	sync()
	zone.Unmanaged["app-two."+droptest.AppDomain] = true
	h.MustOp(admin, "placement.set", map[string]any{"app": appTwo, "node": "node-two"})
	if got = sync(); len(got) != 1 {
		t.Fatalf("an unmanaged name was taken over: %v", got)
	}
	// Archiving removes the record; a failing zone deletes nothing.
	h.MustOp(user, "app.archive", map[string]any{"app": appOne})
	zone.Fail = errors.New("zone unavailable")
	if err := dropserver.SyncDNSForTest(context.Background(), h.App, zone); err == nil {
		t.Fatal("a failing zone reported success")
	}
	zone.Fail = nil
	if len(zone.Names()) != 1 {
		t.Fatal("a failed sync changed records")
	}
	if got = sync(); len(got) != 0 {
		t.Fatalf("archived app kept its record: %v", got)
	}
}
