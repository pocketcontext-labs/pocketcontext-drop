package dropserver

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// Kept on the app in the same transaction as the desired revision. A restart
// resends the same ID to the same placement until its terminal receipt.
type localResize struct {
	ID         string `json:"id"`
	Node       string `json:"node"`
	Generation int    `json:"generation"`
	SourceMiB  int    `json:"source_mib"`
	Target     int    `json:"target"`
	Outcome    string `json:"outcome"`
}

func workspaceDiskRequest(a *core.Record) *localResize {
	var r *localResize
	_ = a.UnmarshalJSONField("local_resize", &r)
	return r
}

func pendingDiskRequest(a *core.Record) bool {
	r := workspaceDiskRequest(a)
	return r != nil && r.Outcome == "pending"
}

func resizeBusy() error {
	return Conflict(dropapi.CodeDiskLocked, "Wait for the current disk resize to finish.")
}

func nodeSupportsResize(app core.App, p *core.Record) (bool, error) {
	if p == nil {
		return false, nil
	}
	n, err := app.FindRecordById("nodes", p.GetString("node"))
	if err != nil {
		return false, err
	}
	return jsonMap(n, "versions")["localVolumeResize"] == true, nil
}

func startDiskResize(c *Ctx, a, p *core.Record, target int) (any, error) {
	if a.GetInt("deployment_seq") >= appspec.MaxDeploymentSeq {
		return nil, Conflict(dropapi.CodeDeploymentLimit, "This app has used every deployment number.")
	}
	if target*2048 < a.GetInt("disk_mib") {
		u, err := freshDiskUsage(c, a)
		if err != nil {
			return nil, err
		}
		if err = overDiskLimit(u, target*2048); err != nil {
			return nil, err
		}
	}
	preview, err := c.App.FindRecordById("apps", a.Id)
	if err != nil {
		return nil, err
	}
	preview.Set("memory_mib", target*256)
	preview.Set("vcpu", (target+3)/4)
	preview.Set("beta_digest", a.GetString("official_target"))
	if err = betaPlacement(c, preview); err != nil {
		return nil, err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	r := localResize{ID: hex.EncodeToString(id[:]), Node: p.GetString("node"), Generation: p.GetInt("generation"), SourceMiB: a.GetInt("disk_mib"), Target: target, Outcome: "pending"}
	a.Set("local_resize", r)
	if err = c.App.Save(a); err != nil {
		return nil, err
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"multiplier": target, "disk_resize": "pending"}})
	return dropapi.AppResult{App: c.appOut(a, nil)}, nil
}

func applyDiskReceipt(app core.App, a, p *core.Record, r *dropapi.ResizeObservation, apply bool) (bool, error) {
	if r == nil || r.DeploymentID != deploymentID(a) || r.Outcome == "pending" {
		return false, nil
	}
	req := workspaceDiskRequest(a)
	if req == nil || req.Outcome != "pending" || req.ID != r.RequestID || req.Node != p.GetString("node") || req.Generation != p.GetInt("generation") || req.SourceMiB != a.GetInt("disk_mib") {
		return false, nil
	}
	expected := req.SourceMiB
	if r.Outcome == "applied" {
		expected = req.Target * 2048
	}
	if int64(expected)*appspec.MiB != r.SizeBytes {
		return false, nil
	}
	if !apply {
		return true, nil
	}
	if r.Outcome == "applied" {
		a.Set("disk_mib", expected)
		a.Set("beta_digest", a.GetString("official_target"))
		a.Set("vcpu", (req.Target+3)/4)
		a.Set("memory_mib", req.Target*256)
		if err := bumpDeployment(a); err != nil {
			return false, err
		}
	}
	req.Outcome = r.Outcome
	a.Set("local_resize", req)
	if err := app.Save(a); err != nil {
		return false, err
	}
	if err := WriteEvent(app, EventRecord{ActorNode: req.Node, App: a.Id, Action: "workspace.resize." + req.Outcome, Detail: map[string]any{"multiplier": req.Target}}); err != nil {
		return false, err
	}
	return true, nil
}

func renderDiskRequest(a *core.Record, v *render.Volume, node string, generation int) error {
	r := workspaceDiskRequest(a)
	if r == nil || r.Outcome != "pending" {
		return nil
	}
	if r.Node != node || r.Generation != generation {
		return fmt.Errorf("resize placement changed for app %s", a.Id)
	}
	v.SizeBytes = int64(r.Target) * 2048 * appspec.MiB
	v.LocalResize = &render.LocalResize{RequestID: r.ID, FromBytes: int64(r.SourceMiB) * appspec.MiB}
	return nil
}
