package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// nibReported is the part of nibrunner's reported.json the agent reads.
// Everything else (cron with its tenant environment, checkpoints, exports,
// compute, guest addresses, restart details) is never decoded, so it can
// never reach a report.
type nibReported struct {
	HostID           string          `json:"hostId"`
	State            string          `json:"state"`
	Message          *string         `json:"message"`
	ReportedAt       string          `json:"reportedAt"`
	AcceptedRevision *string         `json:"acceptedRevision"`
	AcceptedDigest   *string         `json:"acceptedDigest"`
	Capacity         json.RawMessage `json:"capacity"`
	Allocatable      json.RawMessage `json:"allocatable"`
	Versions         json.RawMessage `json:"versions"`
	Instances        []nibInstance   `json:"instances"`
}

type nibInstance struct {
	AppID        string  `json:"appId"`
	DeploymentID string  `json:"deploymentId"`
	State        string  `json:"state"`
	Message      *string `json:"message"`
	RestartCount int     `json:"restartCount"`
	LastExitCode *int    `json:"lastExitCode"`
	ConvergedAt  *string `json:"convergedAt"`
}

type nibCapacity struct {
	VCPUCount  *int64 `json:"vcpuCount"`
	MemoryMib  *int64 `json:"memoryMib"`
	CacheBytes *int64 `json:"cacheBytes"`
}

// readReported reads reported.json. It returns nil (no error) when the file
// is missing.
func readReported(path string) (*nibReported, error) {
	data, err := readOptional(path)
	if err != nil || data == nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("%s is larger than 16 MiB", path)
	}
	var r nibReported
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s is not a nibrunner report", path)
	}
	return &r, nil
}

// allocatable returns nibrunner's allocatable vCPU and memory, or zeros
// (which skip the check) when unknown.
func (r *nibReported) allocatable() (vcpu, memoryMib int) {
	if r == nil {
		return 0, 0
	}
	var c nibCapacity
	if json.Unmarshal(r.Allocatable, &c) != nil || c.VCPUCount == nil || c.MemoryMib == nil {
		return 0, 0
	}
	return int(max(*c.VCPUCount, 0)), int(max(*c.MemoryMib, 0))
}

// sanitizeCapacity keeps only the three numbers of a capacity object.
func sanitizeCapacity(raw json.RawMessage) map[string]any {
	var c nibCapacity
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	out := map[string]any{}
	if c.VCPUCount != nil {
		out["vcpuCount"] = *c.VCPUCount
	}
	if c.MemoryMib != nil {
		out["memoryMib"] = *c.MemoryMib
	}
	if c.CacheBytes != nil {
		out["cacheBytes"] = *c.CacheBytes
	}
	return out
}

// sanitizeVersions keeps only the four version strings, cut to 128 bytes.
func sanitizeVersions(raw json.RawMessage) map[string]any {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out := map[string]any{}
	for _, k := range []string{"agent", "firecracker", "guestImage", "zerofs"} {
		if s, ok := v[k].(string); ok {
			out[k] = cut(s, 128)
		}
	}
	return out
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// hostReport maps nibrunner's host part: state, message, reportedAt,
// capacity, allocatable and versions only.
func hostReport(r *nibReported, scrub func(string) string) *dropapi.HostReport {
	if r == nil {
		return nil
	}
	return &dropapi.HostReport{
		State:       cut(r.State, 64),
		Message:     cut(scrub(deref(r.Message)), dropapi.MaxReportMessage),
		ReportedAt:  r.ReportedAt,
		Capacity:    sanitizeCapacity(r.Capacity),
		Allocatable: sanitizeCapacity(r.Allocatable),
		Versions:    sanitizeVersions(r.Versions),
	}
}

// appIntent is what the agent wanted for one app at the last reconcile.
type appIntent struct {
	AppID        string
	DeploymentID string
	// AgentPhase is set when the agent itself failed the app
	// (staging_failed, secret_failed, failed); Message explains it.
	AgentPhase string
	Message    string
}

// nibPhase maps a nibrunner instance state to a report phase (plan 7.7).
func nibPhase(state string) string {
	switch state {
	case "pending", "starting":
		return "starting"
	case "running":
		return "running"
	case "idle":
		return "sleeping"
	case "unhealthy":
		return "unhealthy"
	case "stopping", "stopped":
		return "stopped"
	case "failed":
		return "failed"
	}
	return "unknown"
}

// appReports builds the per-app part of a report from the agent's intents
// and nibrunner's report. writtenRevision is the revision of the
// desired.json the agent last wrote or confirmed.
func appReports(intents []appIntent, r *nibReported, writtenRevision string, now time.Time, scrub func(string) string) []dropapi.AppReport {
	byApp := map[string]nibInstance{}
	stale := true
	accepted := ""
	if r != nil {
		for _, inst := range r.Instances {
			byApp[inst.AppID] = inst
		}
		if at, err := time.Parse(time.RFC3339Nano, r.ReportedAt); err == nil && now.Sub(at) <= StaleReport {
			stale = false
		}
		accepted = deref(r.AcceptedRevision)
	}
	out := make([]dropapi.AppReport, 0, len(intents))
	for _, in := range intents {
		// deploymentId is the deployment nibrunner runs (empty until it runs one).
		rep := dropapi.AppReport{AppID: in.AppID}
		inst, seen := byApp[in.AppID]
		switch {
		case in.AgentPhase != "":
			rep.Phase, rep.Message = in.AgentPhase, in.Message
		case r == nil:
			rep.Phase, rep.Message = "unknown", "nibrunner report unavailable"
		case stale:
			rep.Phase, rep.Message = "unknown", "nibrunner report stale"
		case !seen:
			rep.Phase = "pending"
		case inst.DeploymentID != in.DeploymentID || (writtenRevision != "" && accepted != writtenRevision):
			rep.Phase = "deploying"
		default:
			rep.Phase = nibPhase(inst.State)
			rep.Message = deref(inst.Message)
		}
		if seen {
			rep.RestartCount = max(inst.RestartCount, 0)
			rep.LastExitCode = inst.LastExitCode
			if c := deref(inst.ConvergedAt); c != "" {
				if _, err := time.Parse(time.RFC3339Nano, c); err == nil {
					rep.ConvergedAt = c
				}
			}
			if appDeploymentOK(in.AppID, inst.DeploymentID) {
				rep.DeploymentID = inst.DeploymentID
			}
		}
		rep.Message = cut(scrub(rep.Message), dropapi.MaxReportMessage)
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	if len(out) > dropapi.MaxReportApps {
		out = out[:dropapi.MaxReportApps]
	}
	return out
}

func appDeploymentOK(appID, id string) bool { return appspec.DeploymentIDOK(appID, id) }

// buildReport assembles one drop.report/v1.
func buildReport(hostID, version string, seq int64, intents []appIntent, r *nibReported, writtenRevision string, now time.Time, scrub func(string) string) dropapi.Report {
	rep := dropapi.Report{
		Schema:       dropapi.ReportSchema,
		HostID:       hostID,
		AgentVersion: version,
		DesiredSeq:   seq,
	}
	if r != nil && r.HostID != hostID {
		r = nil // another host's report says nothing about this one
	}
	rep.Apps = appReports(intents, r, writtenRevision, now, scrub)
	if r != nil {
		rep.AcceptedRevision = cut(deref(r.AcceptedRevision), 128)
		rep.Nibrunner = hostReport(r, scrub)
	}
	return rep
}
