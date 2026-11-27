package dropapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// The agent API (milestone M7; manual docs/agent-api.md): what a node's
// agent reads and sends. The agent signs in as its node, pulls its whole
// desired document from AgentDesiredRoute (drop.desired/v1), turns it into
// render.Input after staging releases and resolving secret values, and sends
// its report to AgentReportRoute (drop.report/v1).

// Agent routes and the node collection.
const (
	AgentDesiredRoute = "/api/drop/agent/desired"
	AgentReportRoute  = "/api/drop/agent/report"
	NodeCollection    = "nodes"
	NodeAuthRoute     = "/api/collections/nodes/auth-with-password"
	NodeRefreshRoute  = "/api/collections/nodes/auth-refresh"
)

// Schemas of the two documents.
const (
	DesiredSchema = "drop.desired/v1"
	ReportSchema  = "drop.report/v1"
)

// MaxReportBytes bounds one report body; MaxReportApps the apps in it;
// MaxReportMessage a message (longer ones are cut).
const (
	MaxReportBytes   = 1 << 20
	MaxReportApps    = 256
	MaxReportMessage = 2000
)

// Desired is the drop.desired/v1 document: everything the node's
// desired.json must hold, for one node at one desiredSeq.
//
// Apps are the apps placed on the node that are not archived and have a
// current release, sorted by appId, in render.App form, except that secrets
// carry no value: the agent reads each one from SSM (parameter at version)
// and fills it in. An app the node ran before and that is not in Apps or
// Rejected is gone (archived, unplaced, moved or without a release): the
// agent renders it as a stopped tombstone. Rejected apps failed the server's
// own render check (the same validation as the node's); the agent keeps
// their previous entries unchanged (render.Input.Keep) and never tombstones
// them.
//
// ContentHash is the hex sha256 of this document's canonical JSON with
// desiredSeq, state and contentHash empty; desiredSeq goes up exactly when it
// changes. Limits are not sent: the agent applies its own (DROP_AGENT_*).
type Desired struct {
	Schema      string            `json:"schema"`
	Node        string            `json:"node"`
	NodeID      string            `json:"nodeId"`
	HostID      string            `json:"hostId"`
	State       string            `json:"state"`
	DesiredSeq  int64             `json:"desiredSeq"`
	ContentHash string            `json:"contentHash"`
	Apps        []DesiredApp      `json:"apps"`
	Deleted     []string          `json:"deleted,omitempty"`
	Rejected    []render.AppError `json:"rejected"`
}

// ComputeContentHash identifies the versioned content of this node's desired document.
// Keep these exclusions compatible with drop.desired/v1.
func (d Desired) ComputeContentHash() (string, error) {
	d.DesiredSeq, d.State, d.ContentHash = 0, "", ""
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// DesiredApp is one app of a desired document: render.App whose secrets
// have no value field.
type DesiredApp struct {
	render.App
	Secrets []DesiredSecret `json:"secrets"`
	// Limits bound the app's S3 traffic through the node's host services
	// proxy (hostservices.go); nil gives the node's defaults.
	Limits *HostLimits `json:"limits,omitempty"`
}

// DesiredSecret names one secret version; the agent reads its value from SSM
// at Parameter (awsx.SecretParameter's form) and Version.
type DesiredSecret struct {
	Name      string `json:"name"`
	Parameter string `json:"parameter"`
	Version   int64  `json:"version"`
}

// RenderApp returns the app as render.Input takes it, with the secret values
// the agent resolved (by name; a missing name gets an empty value, so the
// agent must resolve every secret first or keep the app's previous entry).
func (a DesiredApp) RenderApp(values map[string]string) render.App {
	out := a.App
	out.Secrets = make([]render.Secret, len(a.Secrets))
	for i, s := range a.Secrets {
		out.Secrets[i] = render.Secret{Name: s.Name, Parameter: s.Parameter, Version: s.Version, Value: values[s.Name]}
	}
	return out
}

// ReportPhases are the phases an agent may report for an app.
var ReportPhases = []string{
	"pending", "staging", "staging_failed", "secret_failed", "deploying", "starting",
	"running", "sleeping", "unhealthy", "stopped", "failed", "unknown",
}

// Report is the drop.report/v1 document an agent POSTs to AgentReportRoute
// every 30 seconds and after each write of desired.json. Unknown fields are
// refused.
//
// HostID must be the node's host id (409 host_mismatch otherwise: the agent
// is on the wrong host and must stop). DesiredSeq is the seq of the last
// desired document the agent applied; AcceptedRevision nibrunner's
// acceptedRevision. Nibrunner holds the host part of reported.json (nil when
// the agent could not read it); its cron, checkpoints and exports are never
// sent (cron holds tenant environment values). Apps are the agent's phase of
// each app it was given or still runs; apps not placed on this node are
// ignored.
type Report struct {
	Schema           string      `json:"schema"`
	HostID           string      `json:"hostId"`
	AgentVersion     string      `json:"agentVersion"`
	DesiredSeq       int64       `json:"desiredSeq"`
	AcceptedRevision string      `json:"acceptedRevision"`
	Nibrunner        *HostReport `json:"nibrunner"`
	Deleted          []string    `json:"deleted,omitempty"`
	Apps             []AppReport `json:"apps"`
}

// HostReport is the host part of nibrunner's reported.json. Capacity and
// Allocatable are {vcpuCount, memoryMib, cacheBytes}; Versions
// {agent, firecracker, guestImage, zerofs}.
// GuestMemory is one complete node-wide measurement. The timestamp identifies
// the guest sampling pass; usedMiB is rounded up after summing all guests.
type GuestMemory struct {
	At         int64 `json:"at" db:"at"`
	UsedMiB    int   `json:"used_mib" db:"used_mib"`
	RunningVMs int   `json:"running_vms" db:"running_vms"`
}

type HostReport struct {
	GuestMemory *GuestMemory   `json:"guestMemory,omitempty"`
	State       string         `json:"state"`
	Message     string         `json:"message"`
	ReportedAt  string         `json:"reportedAt"`
	Capacity    map[string]any `json:"capacity"`
	Allocatable map[string]any `json:"allocatable"`
	Versions    map[string]any `json:"versions"`
}

// HookResult contains only bounded lifecycle metadata. Guest response bodies never leave the node.
type HookResult struct {
	Event      string `json:"event"`
	Outcome    string `json:"outcome"`
	Time       string `json:"time"`
	DurationMs int64  `json:"durationMs"`
}

func (h *HookResult) Valid() bool {
	if h == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, h.Time)
	return err == nil && len(h.Time) <= 40 && h.DurationMs >= 0 &&
		slices.Contains([]string{"suspend", "resume", "terminate"}, h.Event) &&
		slices.Contains([]string{"ok", "error", "timeout", "unreachable"}, h.Outcome)
}

// AppReport is the agent's view of one app: its phase (one of ReportPhases),
// a message for people (no secret or environment values), the deployment
// nibrunner runs, and nibrunner's restart count, last exit code and
// convergence time (RFC 3339, or empty).
type AwakeCounter struct {
	MeasuredSeconds int64 `json:"measuredSeconds"`
	BillableSeconds int64 `json:"billableSeconds"`
}

func (c *AwakeCounter) Valid() bool {
	return c == nil || c.MeasuredSeconds >= 0 && c.MeasuredSeconds <= 1<<32 && c.BillableSeconds >= 0 && c.BillableSeconds <= c.MeasuredSeconds
}

type AppReport struct {
	VolumeUsage  *VolumeUsage  `json:"volumeUsage,omitempty"`
	FirstRun     *FirstRun     `json:"firstRun,omitempty"`
	LogTail      string        `json:"logTail,omitempty"`
	AwakeCounter *AwakeCounter `json:"awakeCounter,omitempty"`
	LastHook     *HookResult   `json:"lastHook,omitempty"`
	AppID        string        `json:"appId"`
	DeploymentID string        `json:"deploymentId"`
	Phase        string        `json:"phase"`
	Message      string        `json:"message"`
	RestartCount int           `json:"restartCount"`
	LastExitCode *int          `json:"lastExitCode"`
	ConvergedAt  string        `json:"convergedAt"`

	// Usage is the host services proxy's counters of the app's deployment
	// (hostservices.go), when the app used it.
	Usage *HostUsage `json:"usage,omitempty"`
}

// ReportResult answers a report: the node's current desiredSeq and state, so
// an agent that missed a realtime event pulls again when desiredSeq is above
// the one it applied; Accepted counts the app reports taken and Ignored
// lists the appIds not placed on this node.
type ReportResult struct {
	DesiredSeq int64    `json:"desiredSeq"`
	State      string   `json:"state"`
	Accepted   int      `json:"accepted"`
	Ignored    []string `json:"ignored"`
}

// AgentVersionPattern bounds the agent version string.
var AgentVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
