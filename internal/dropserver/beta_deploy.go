package dropserver

import (
	"errors"
	"slices"
	"strconv"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Each unit buys 256 MiB, 2 GiB of disk and one quarter of a core.
var crewBetaSizes = []int{1, 2, 3, 4, 5, 6, 7, 8}

func suggestedCrewSize(m dropapi.BetaManifest) int {
	for _, size := range crewBetaSizes {
		if size >= m.Minimum.CPUQuarters && size*256 >= m.Minimum.MemoryMiB && size*2048 >= m.Minimum.DiskMiB {
			return size
		}
	}
	return crewBetaSizes[len(crewBetaSizes)-1]
}

func betaDisplayName(m dropapi.BetaManifest) string {
	if m.App == "crew" {
		return "PocketContext Crew"
	}
	if m.App == "" {
		return "Generic Linux app"
	}
	return m.App
}

func betaInspect(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	m, err := betaRelease(c.App, in.SHA256)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": betaDisplayName(m), "generic": m.Schema == "", "version": m.Version, "requires": m.Requires, "optional": m.Optional, "http_port": m.HTTPPort, "multipliers": crewBetaSizes, "suggested_multiplier": suggestedCrewSize(m)}, nil
}

func betaPrepare(c *Ctx, in *dropapi.BetaPrepareInput) (any, error) {
	m, err := betaRelease(c.App, in.SHA256)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(crewBetaSizes, in.Multiplier) {
		return nil, Invalid("multiplier", "Choose an available whole-number size for this release.")
	}
	cpu, mem, disk := (in.Multiplier+3)/4, in.Multiplier*256, in.Multiplier*2048
	if servicesOf(c.App).Releases == nil {
		return nil, releasesUnavailable()
	}
	generic := m.Schema == ""
	features := slices.Concat(m.Requires, m.Optional)
	kind := "ordinary"
	if slices.Contains(features, "verified_owner_email") {
		kind = "crew"
	}
	port := m.HTTPPort
	if generic {
		port = in.HTTPPort
	} else if in.HTTPPort != 0 && in.HTTPPort != port {
		return nil, Invalid("http_port", "The port is declared by the manifest.")
	}
	slug := m.App
	if slug == "" {
		slug = "app"
	}
	if len(slug) > 12 {
		slug = slug[:12]
	}
	result, err := appCreate(c, &dropapi.AppCreateInput{
		Name: betaDisplayName(m), Slug: slug + "-" + core.GenerateDefaultRandomId(), Kind: kind, ExecutableName: m.App,
		VCPU: &cpu, MemoryMiB: &mem, DiskMiB: &disk, Env: map[string]string{"PORT": strconv.Itoa(port)},
		HTTPPort: &port, HookPort: &m.HookPort, HealthKind: "tcp",
	})
	if err != nil {
		return nil, err
	}
	app, err := c.App.FindRecordById("apps", result.(dropapi.AppResult).App.ID)
	if err != nil {
		return nil, err
	}
	app.Set("generic_beta", generic)
	app.Set("beta_digest", in.SHA256)
	app.Set("sign_in_email_enabled", slices.Contains(features, "sign_in_email"))
	if !slices.Contains(features, "s3_replica") {
		app.Set("crew_enrollment", "")
		app.Set("replica_sync_seconds", 0)
	}
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"sha256": in.SHA256, "multiplier": in.Multiplier}})
	return dropapi.AppResult{App: c.appOut(app, nil)}, nil
}

func checkBetaUpload(app core.App, a *core.Record, digest string, activate bool) error {
	expected := a.GetString("beta_digest")
	if expected == "" {
		return nil
	}
	if expected != digest {
		return Conflict("unsupported_binary", "The uploaded bytes do not match the selected release.")
	}
	if activate {
		return Forbidden("Deploy this app through Drop Beta after upload.")
	}
	_, err := betaRelease(app, digest)
	return err
}

func betaCapacityError() error {
	return Conflict("beta_no_capacity", "There is no capacity for this deployment right now. Retry this app in a few minutes.")
}

func betaPlacement(c *Ctx, a *core.Record) error {
	p, err := placementOf(c.App, a.Id)
	if err != nil {
		return err
	}
	services := servicesOf(c.App)
	names := services.BetaNodes
	if c.Owner && c.Auth.Id == a.GetString("owner") {
		names = slices.Concat(services.BetaOwnerNodes, names)
	}
	state := agentStateOf(c.App)
	for _, name := range names {
		node, err := c.findNode(name)
		if err != nil {
			continue
		}
		// A retry must keep any existing volume on its original node.
		if p != nil && p.GetString("node") != node.Id {
			continue
		}
		if betaFractionalQuota(a) != 0 && jsonMap(node, "versions")["cpuQuarterQuota"] != true {
			continue
		}
		cpu, mem := allocatable(node)
		if node.GetString("host_state") != "ready" || node.GetString("state") != dropapi.NodeActive || !online(state, node, state.clock()) || cpu < 1 || mem < 1 {
			continue
		}
		if err := checkNodeRoom(c.App, node, a); err != nil {
			var conflict *dropapi.Error
			if errors.As(err, &conflict) && conflict.Code == dropapi.CodeNodeTooSmall {
				continue
			}
			return err
		}
		if p != nil {
			return nil
		}
		_, err = placementSet(c, &dropapi.PlacementSetInput{App: a.Id, Node: node.Id})
		return err
	}
	return betaCapacityError()
}

func betaDeploy(c *Ctx, in *dropapi.ReleaseDeployInput) (any, error) {
	a, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	digest := a.GetString("beta_digest")
	if digest == "" {
		return nil, Invalid("app", "Select an app prepared through Drop Beta.")
	}
	_, err = betaRelease(c.App, digest)
	if err != nil {
		return nil, err
	}
	size := a.GetInt("memory_mib") / 256
	if !slices.Contains(crewBetaSizes, size) || a.GetInt("vcpu") != (size+3)/4 || a.GetInt("memory_mib") != size*256 || a.GetInt("disk_mib") != size*2048 {
		return nil, Conflict("invalid_size", "The app size is unavailable for this release.")
	}
	quota, err := c.quota()
	if err != nil {
		return nil, err
	}
	if !quota.Unlimited && quota.Used > quota.Limit {
		return nil, Conflict(dropapi.CodeQuotaExceeded, "Your retained disk exceeds the 10 GiB allowance. Delete an unused volume before deploying.")
	}
	if !recordID.MatchString(in.Release) {
		return nil, NotFound("Release")
	}
	r, err := c.App.FindRecordById("releases", in.Release)
	if err != nil || r.GetString("app") != a.Id || r.GetString("sha256") != digest {
		return nil, NotFound("Release")
	}
	if err := betaPlacement(c, a); err != nil {
		return nil, err
	}
	// placementSet fixes the volume size on the saved record. Reload before
	// updating the release so the original copy cannot overwrite that field.
	a, err = c.App.FindRecordById("apps", a.Id)
	if err != nil {
		return nil, err
	}
	bumped, err := setCurrentRelease(c.App, a, r)
	if err != nil {
		return nil, err
	}
	if !bumped {
		raw, err := appStatus(c, &dropapi.StatusInput{App: a.Id})
		if err != nil {
			return nil, err
		}
		status := raw.(dropapi.Status)
		if status.ReportedDeploymentID == status.DeploymentID && failurePhases[status.Phase] {
			if err := bumpDeployment(a); err != nil {
				return nil, err
			}
			bumped = true
		}
	}
	restart := a.GetString("desired_state") == "stopped"
	if restart {
		a.Set("desired_state", a.GetString("start_state"))
	}
	if bumped || restart {
		if err := c.App.Save(a); err != nil {
			return nil, err
		}
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"sha256": digest, "release": r.Id, "deployment_seq": a.GetInt("deployment_seq")}})
	return dropapi.ReleaseDeployResult{App: c.appOut(a, nil), Release: releaseOut(r, r.Id), DeploymentBumped: bumped, DeploymentID: deploymentID(a)}, nil
}

// Whole-core configurations can also run on older nodes.
func betaFractionalQuota(a *core.Record) int {
	units := a.GetInt("memory_mib") / 256
	if a.GetString("beta_digest") != "" && units%4 != 0 {
		return units
	}
	return 0
}
