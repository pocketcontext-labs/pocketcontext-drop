package dropserver

import (
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/privateblob"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// The desired document of one node (drop.desired/v1, dropapi.Desired), which
// the agent pulls from GET /api/drop/agent/desired (agent_routes.go).
//
// It maps the node's placements, their apps, current releases and secret
// names (never values: the server holds no secret reader) into render.App and
// runs render.Render over them as a check, with the server's limits and no
// secret values: an app the renderer refuses is moved to Rejected, so the
// server never sends what the node's renderer would refuse. The node renders
// again with its own limits, the resolved secret values and its previous
// document.
//
// Left out: apps that are archived or have no current release. The agent
// turns an app it ran before and no longer receives into a stopped tombstone.

// fallbackHostSuffix is the host suffix of the server's check when it has no
// app domain (apps then have no hostname, so it never matches anything).
const fallbackHostSuffix = ".invalid"

// buildDesired returns the desired document of node at its current
// desired_seq. A whole-document refusal by the renderer (which the
// operations' validation should make impossible) is an error.
func buildDesired(app core.App, node *core.Record) (*dropapi.Desired, error) {
	services := servicesOf(app)
	placements, err := app.FindRecordsByFilter("placements", "node = {:node}", "", 0, 0, dbx.Params{"node": node.Id})
	if err != nil {
		return nil, err
	}
	versions := jsonMap(node, "versions")
	deferSupported := versions["deferUntilRequest"] == true
	apps := []dropapi.DesiredApp{}
	for _, p := range placements {
		record, err := app.FindRecordById("apps", p.GetString("app"))
		if err != nil {
			return nil, err
		}
		if record.GetBool("archived") {
			continue
		}
		desired, ok, err := desiredApp(app, record, services)
		if err != nil {
			return nil, err
		}
		if ok {
			if desired.DesiredState == "on-request" && record.GetInt("billing_deferred_seq") == record.GetInt("deployment_seq") {
				if deferSupported {
					desired.DeferUntilRequest = true
				} else {
					// Older runners eagerly boot a new revision. Retain the volume
					// and route in stopped state until the node reports support.
					desired.DesiredState = appspec.StateStopped
				}
			}
			if err := renderDiskRequest(record, &desired.Volume, node.Id, p.GetInt("generation")); err != nil {
				return nil, err
			}
			apps = append(apps, desired)
		}
	}
	slices.SortFunc(apps, func(a, b dropapi.DesiredApp) int { return strings.Compare(a.AppID, b.AppID) })

	limits := render.Limits{DeferUntilRequest: deferSupported, LocalVolumeResize: versions["localVolumeResize"] == true, CPUQuarterQuota: versions["cpuQuarterQuota"] == true, AllowedHostSuffix: fallbackHostSuffix, SSMPrefix: services.SSMPrefix}
	if ValidAppDomain(services.AppDomain) {
		limits.AllowedHostSuffix = "." + services.AppDomain
	}
	vcpu, memory := allocatable(node)
	limits.AllocatableVCPU, limits.AllocatableMemoryMib = max(0, vcpu), max(0, memory)
	check := render.Input{HostID: node.GetString("host_id"), Limits: limits}
	for _, a := range apps {
		check.Apps = append(check.Apps, a.RenderApp(nil))
	}
	out, err := render.Render(check)
	if err != nil {
		return nil, err
	}
	rejected := map[string]bool{}
	for _, r := range out.Rejected {
		rejected[r.AppID] = true
	}
	d := &dropapi.Desired{
		Schema: dropapi.DesiredSchema, Node: node.GetString("name"), NodeID: node.Id, HostID: node.GetString("host_id"),
		Apps: []dropapi.DesiredApp{}, Rejected: []render.AppError{},
	}
	for _, a := range apps {
		if !rejected[a.AppID] {
			d.Apps = append(d.Apps, a)
		}
	}
	if err := app.DB().NewQuery("SELECT nib FROM drop_volume_deletions WHERE node={:node} ORDER BY nib").Bind(dbx.Params{"node": node.Id}).Column(&d.Deleted); err != nil {
		return nil, err
	}
	d.Rejected = append(d.Rejected, out.Rejected...)
	slices.SortFunc(d.Rejected, func(a, b render.AppError) int { return strings.Compare(a.AppID, b.AppID) })
	if d.ContentHash, err = d.ComputeContentHash(); err != nil {
		return nil, err
	}
	d.State = node.GetString("state")
	d.DesiredSeq = int64(node.GetInt("desired_seq"))
	return d, nil
}

// desiredApp maps one placed app; ok is false when it has no current release.
func desiredApp(app core.App, record *core.Record, services Services) (dropapi.DesiredApp, bool, error) {
	releaseID := record.GetString("current_release")
	if releaseID == "" {
		return dropapi.DesiredApp{}, false, nil
	}
	release, err := app.FindRecordById("releases", releaseID)
	if err != nil {
		return dropapi.DesiredApp{}, false, err
	}
	if release.GetString("app") != record.Id {
		return dropapi.DesiredApp{}, false, errors.New("current release belongs to a different app")
	}
	nibAppID := record.GetString("nib_app_id")
	a := dropapi.DesiredApp{App: render.App{
		GenericBeta:        record.GetBool("generic_beta"),
		CrewOwnerEmail:     record.GetString("crew_owner_email"),
		PrivateOwnerEmail:  record.GetBool("private_owner_email"),
		SignInEmailEnabled: record.GetBool("sign_in_email_enabled"),

		AppID:          nibAppID,
		DeploymentID:   deploymentID(record),
		DesiredState:   record.GetString("desired_state"),
		IdleTimeoutMs:  int64(record.GetInt("idle_timeout_ms")),
		Volume:         render.Volume{VolumeID: record.GetString("volume_id"), SizeBytes: int64(record.GetInt("disk_mib")) * appspec.MiB},
		Release:        render.Release{SHA256: release.GetString("sha256"), S3Key: release.GetString("s3_key"), SizeBytes: int64(release.GetInt("size_bytes"))},
		ExecutableName: record.GetString("executable_name"),
		HTTPPort:       record.GetInt("http_port"),
		HookPort:       record.GetInt("hook_port"),
		Args:           appArgs(record),
		Resources:      render.Resources{CPUQuarters: betaFractionalQuota(record), VCPU: record.GetInt("vcpu"), MemoryMib: record.GetInt("memory_mib")},
		HealthCheck:    render.HealthCheck{Kind: record.GetString("health_kind"), Path: record.GetString("health_path")},
		Hostnames:      []render.Hostname{},
		Env:            appEnv(record),
	}, Secrets: []dropapi.DesiredSecret{}}
	if record.GetBool("developer_private") {
		a.Release.PrivateKey, err = privateblob.Key(record.GetString("private_release_master"), a.Release.SHA256)
		if err != nil {
			return a, false, err
		}
	}
	for name := range a.Env {
		if appspec.PlatformIdentityEnv(name) {
			delete(a.Env, name)
		}
	}
	if state := record.GetString("crew_enrollment"); state != "" {
		a.ManagedCrew = true
		eligible, err := replicaEligible(app, record)
		if err != nil {
			return a, false, err
		}
		if !eligible && record.GetBool("required_s3") {
			a.DesiredState = appspec.StateStopped
		}
		a.LocalOnly = !eligible
		a.ProxyOnly = eligible
		if eligible {
			a.CrewReplicaBucket, a.AWSRegion = services.CrewReplicaBucket, services.AWSRegion
		}
		a.CrewFreshEnrollment = state == "fresh"
		a.ReplicaSyncSeconds = record.GetInt("replica_sync_seconds")
		a.Limits = hostLimits(record)
	}
	if a.HealthCheck.Kind != appspec.HealthHTTP {
		a.HealthCheck.Path = ""
	}
	if record.GetBool("developer_private") {
		owner, err := app.FindRecordById("users", record.GetString("owner"))
		if err != nil {
			return a, false, err
		}
		if !isDeveloper(owner) || owner.GetBool("suspended") {
			a.DesiredState = appspec.StateStopped
		}
		key, err := privateAppKey(app, record.Id)
		if err != nil {
			return a, false, err
		}
		a.PrivateRoute = &render.PrivateRoute{Hostname: appHostname(record, services.AppDomain), PublicKey: key.Public().(ed25519.PublicKey)}
	} else if host := record.GetString("hostname"); host != "" {
		a.Hostnames = append(a.Hostnames, render.Hostname{Hostname: host, Kind: "platform"})
	}
	secrets, err := secretRecords(app, record)
	if err != nil {
		return a, false, err
	}
	for _, s := range secrets {
		if appspec.PlatformIdentityEnv(s.GetString("name")) {
			continue
		}
		parameter, err := awsx.SecretParameter(services.SSMPrefix, nibAppID, s.GetString("name"))
		if err != nil {
			return a, false, err
		}
		a.Secrets = append(a.Secrets, dropapi.DesiredSecret{Name: s.GetString("name"), Parameter: parameter, Version: int64(s.GetInt("ssm_version"))})
	}
	return a, true, nil
}

// A missing measurement permits initial placement before the first report.
// A reported zero is known and leaves no room for an additional placement.
func allocatable(node *core.Record) (vcpu, memoryMib int) {
	return nodeResources(node, "allocatable")
}

func nodeResources(node *core.Record, field string) (vcpu, memoryMib int) {
	var a struct {
		VCPUCount *int `json:"vcpuCount"`
		MemoryMib *int `json:"memoryMib"`
	}
	_ = node.UnmarshalJSONField(field, &a)
	vcpu, memoryMib = -1, -1
	if a.VCPUCount != nil && *a.VCPUCount >= 0 && *a.VCPUCount < 1<<20 {
		vcpu = *a.VCPUCount
	}
	if a.MemoryMib != nil && *a.MemoryMib >= 0 && *a.MemoryMib < 1<<40 {
		memoryMib = *a.MemoryMib
	}
	return
}

// replicaEligible reports whether this deployment may use the replica bucket.
// A funded account's last local-only deployment stays local until redeploy.
func replicaEligible(app core.App, record *core.Record) (bool, error) {
	var eligible bool
	err := app.DB().NewQuery(`SELECT balance>0 AND NOT EXISTS(SELECT 1 FROM drop_credit_local_deploy WHERE app={:app} AND seq={:seq})
 FROM drop_credit_accounts WHERE owner={:owner}`).Bind(dbx.Params{"app": record.Id, "seq": record.GetInt("deployment_seq"), "owner": record.GetString("owner")}).Row(&eligible)
	return eligible, err
}
