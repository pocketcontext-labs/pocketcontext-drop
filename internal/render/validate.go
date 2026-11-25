package render

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

// The 7.3 validation. The server never controls paths or commands: every value
// that reaches a path, a program or the environment is checked here, on the
// node, before it is rendered. Messages name fields, never values of env or
// secrets.

var (
	reAppID      = appspec.AppIDPattern
	reSHA256     = appspec.SHA256Pattern
	reExecutable = appspec.ExecutablePattern
	reHostID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	reHostname   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	reSSMPrefix  = regexp.MustCompile(`^(/[A-Za-z0-9_.-]+)+$`)
)

// Bounds from the data model (plan section 3) and nibrunner's schema; the
// app rules are shared with the server's operations (internal/appspec).
const (
	mib             = appspec.MiB
	minDiskMib      = appspec.MinDiskMiB
	maxDiskMib      = appspec.MaxDiskMiB
	maxReleaseBytes = appspec.MaxReleaseBytes
	minIdleMs       = appspec.MinIdleMs
	maxIdleMs       = appspec.MaxIdleMs
	minPort         = appspec.MinPort
	maxPort         = appspec.MaxPort
	minVCPU         = appspec.MinVCPU
	maxVCPU         = appspec.MaxVCPU
	minMemoryMib    = appspec.MinMemoryMiB
	maxMemoryMib    = appspec.MaxMemoryMiB
	maxEnvEntries   = appspec.MaxEnvEntries
	maxHostnames    = 16
	maxSeq          = 999999999
)

func deploymentIDOK(appID, deploymentID string) bool {
	return appspec.DeploymentIDOK(appID, deploymentID)
}

// checkInput refuses a malformed input as a whole.
func checkInput(in Input) error {
	bad := func(format string, args ...any) error {
		return &Error{Code: CodeInvalidInput, Message: fmt.Sprintf(format, args...)}
	}
	if !reHostID.MatchString(in.HostID) {
		return bad("hostId is not a nibrunner identifier")
	}
	if in.DesiredSeq < 0 || in.DesiredSeq > maxSeq {
		return bad("desiredSeq out of range")
	}
	suffix := in.Limits.AllowedHostSuffix
	if !strings.HasPrefix(suffix, ".") || !reHostname.MatchString("x"+suffix) {
		return bad("limits.allowedHostSuffix must be a dot followed by a hostname")
	}
	if !reSSMPrefix.MatchString(in.Limits.SSMPrefix) {
		return bad("limits.ssmPrefix must be an absolute parameter path without a trailing slash")
	}
	if in.Limits.AllocatableVCPU < 0 || in.Limits.AllocatableMemoryMib < 0 {
		return bad("limits.allocatable values must not be negative")
	}
	seen := map[string]bool{}
	var dup []string
	for _, app := range in.Apps {
		if seen[app.AppID] {
			dup = append(dup, app.AppID)
		}
		seen[app.AppID] = true
	}
	for _, id := range in.Keep {
		if seen[id] {
			dup = append(dup, id)
		}
		seen[id] = true
	}
	if len(dup) > 0 {
		slices.Sort(dup)
		return &Error{Code: CodeInvalidInput, Message: "appIds named more than once in apps and keep", IDs: slices.Compact(dup)}
	}
	return nil
}

// validateApp checks one app; nil means it can be rendered.
func validateApp(app App, lim Limits) *AppError {
	fail := func(field, format string, args ...any) *AppError {
		return &AppError{AppID: app.AppID, Code: CodeInvalidApp, Field: field, Message: fmt.Sprintf(format, args...)}
	}
	if !reAppID.MatchString(app.AppID) {
		return fail("appId", "must match ^d-[a-z0-9]{15}$")
	}
	if !deploymentIDOK(app.AppID, app.DeploymentID) {
		return fail("deploymentId", "must be <appId>-dep-<1 to 9 digits>")
	}
	if (app.CrewOwnerEmail != "" || app.SignInEmailEnabled) && (!appspec.ValidSignInMailbox(app.CrewOwnerEmail) || strings.ToLower(app.CrewOwnerEmail) != app.CrewOwnerEmail) {
		return fail("crewOwnerEmail", "must be a canonical verified mailbox for a Crew app")
	}
	switch app.DesiredState {
	case "running", "stopped":
	case "on-request":
		if app.IdleTimeoutMs < minIdleMs || app.IdleTimeoutMs > maxIdleMs {
			return fail("idleTimeoutMs", "must be %d–%d for an on-request app", minIdleMs, maxIdleMs)
		}
	default:
		return fail("desiredState", "must be running, on-request or stopped")
	}
	if app.Volume.VolumeID != app.AppID+"-vol" {
		return fail("volume.volumeId", "must be <appId>-vol")
	}
	if s := app.Volume.SizeBytes; s%mib != 0 || s < minDiskMib*mib || s > maxDiskMib*mib {
		return fail("volume.sizeBytes", "must be whole MiB, %d–%d MiB", minDiskMib, maxDiskMib)
	}
	if !reSHA256.MatchString(app.Release.SHA256) {
		return fail("release.sha256", "must be 64 lowercase hex")
	}
	if app.Release.S3Key != "releases/"+app.Release.SHA256 {
		return fail("release.s3Key", "must be releases/<sha256>")
	}
	if app.Release.SizeBytes < 1 || app.Release.SizeBytes > maxReleaseBytes {
		return fail("release.sizeBytes", "must be 1–%d", maxReleaseBytes)
	}
	if !reExecutable.MatchString(app.ExecutableName) {
		return fail("executableName", "must match ^[a-z][a-z0-9-]{0,31}$")
	}
	if app.HTTPPort < minPort || app.HTTPPort > maxPort {
		return fail("httpPort", "must be %d–%d", minPort, maxPort)
	}
	if !appspec.ValidHookPort(app.HookPort, app.HTTPPort) {
		return fail("hookPort", "must be zero or a guest port different from httpPort")
	}
	if app.ManagedCrew && (app.CrewOwnerEmail == "" || len(app.Hostnames) != 1 || app.Hostnames[0].Kind != "platform") {
		return fail("managedCrew", "requires the verified owner and one platform hostname")
	}
	if app.ManagedCrew && (!crewBucket.MatchString(app.CrewReplicaBucket) || strings.Contains(app.CrewReplicaBucket, "..")) {
		return fail("crewReplicaBucket", "must be an S3 bucket name")
	}
	if app.ManagedCrew && !crewRegion.MatchString(app.AWSRegion) {
		return fail("awsRegion", "must be an AWS region name")
	}
	if !appspec.ValidReplicaSyncSeconds(app.ReplicaSyncSeconds) || (app.ReplicaSyncSeconds != 0 && !app.ManagedCrew) {
		return fail("replicaSyncSeconds", "must be zero, or %d–%d on a managed Crew app", appspec.MinReplicaSyncSeconds, appspec.MaxReplicaSyncSeconds)
	}
	if field, reason := appspec.CheckArgs(app.Args); field != "" {
		return fail(field, "%s", reason)
	}
	r := app.Resources
	if r.VCPU < minVCPU || r.VCPU > maxVCPU || (lim.AllocatableVCPU > 0 && r.VCPU > lim.AllocatableVCPU) {
		return fail("resources.vcpu", "must be %d–%d and within the node's allocatable", minVCPU, maxVCPU)
	}
	if r.MemoryMib < minMemoryMib || r.MemoryMib > maxMemoryMib || (lim.AllocatableMemoryMib > 0 && r.MemoryMib > lim.AllocatableMemoryMib) {
		return fail("resources.memoryMib", "must be %d–%d and within the node's allocatable", minMemoryMib, maxMemoryMib)
	}
	if field, reason := appspec.CheckHealth(app.HealthCheck.Kind, app.HealthCheck.Path); field != "" {
		return fail("healthCheck."+field, "%s", reason)
	}
	if len(app.Hostnames) > maxHostnames {
		return fail("hostnames", "at most %d", maxHostnames)
	}
	hosts := map[string]bool{}
	for i, h := range app.Hostnames {
		field := fmt.Sprintf("hostnames[%d]", i)
		if len(h.Hostname) > 253 || !reHostname.MatchString(h.Hostname) {
			return fail(field, "not a lowercase hostname")
		}
		if !strings.HasSuffix(h.Hostname, lim.AllowedHostSuffix) {
			return fail(field, "must end with %s", lim.AllowedHostSuffix)
		}
		if h.Kind != "platform" && h.Kind != "custom" {
			return fail(field, "kind must be platform or custom")
		}
		if hosts[h.Hostname] {
			return fail(field, "duplicate hostname")
		}
		hosts[h.Hostname] = true
	}
	if len(app.Env)+len(app.Secrets) > maxEnvEntries {
		return fail("env", "at most %d entries with secrets", maxEnvEntries)
	}
	for _, name := range slices.Sorted(maps.Keys(app.Env)) {
		if e := checkEnvEntry(app, "env", name, app.Env[name]); e != nil {
			return e
		}
	}
	names := map[string]bool{}
	ssmApp := strings.TrimPrefix(app.AppID, "d-")
	for i, s := range app.Secrets {
		field := fmt.Sprintf("secrets[%d]", i)
		if e := checkEnvEntry(app, field, s.Name, s.Value); e != nil {
			return e
		}
		if _, clash := app.Env[s.Name]; clash || names[s.Name] {
			return fail(field, "%s is set twice", s.Name)
		}
		names[s.Name] = true
		if s.Parameter != lim.SSMPrefix+"/apps/"+ssmApp+"/"+s.Name {
			return fail(field+".parameter", "must be <ssmPrefix>/apps/<appId without d->/<name>")
		}
		if s.Version < 1 {
			return fail(field+".version", "must be at least 1")
		}
	}
	return nil
}

var crewBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var crewRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`)

func checkEnvEntry(app App, field, name, value string) *AppError {
	fail := func(msg string) *AppError {
		return &AppError{AppID: app.AppID, Code: CodeInvalidApp, Field: field, Message: msg}
	}
	if reason := appspec.CheckAppEnvName(name, app.ManagedCrew); reason != "" {
		return fail(reason)
	}
	if reason := appspec.CheckEnvValue(name, value); reason != "" {
		return fail(reason)
	}
	return nil
}
