// Package render turns Drop's resolved apps for one node into nibrunner's whole
// desired.json document (schema/desired.schema.json).
//
// Render is pure: no clock, no randomness, no file or network access. The same
// Input always gives byte-identical output. Drop owns the whole document
// (decision 12 in the Drop decisions log): an Input or a previous document that
// holds any appId not starting with "d-" is refused with a foreign_entries error
// and nothing is rendered. An explicit Deleted entry can remove a known local volume. An
// app that was in the previous document but is no longer in the input becomes a
// stopped tombstone with its volume kept present.
//
// Input is the contract the node agent (M9) fills from the server's
// drop.desired/v1 payload (M7) after staging artifacts and resolving secrets.
// The input types deliberately do not import Drop's models.
package render

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

// Input is everything Render needs. It is also the JSON form of the golden
// test fixtures.
type Input struct {
	// HostID is nibrunner's host identifier (reported.json hostId). It must
	// match the previous document's hostId when Previous is set.
	HostID string `json:"hostId"`
	// DesiredSeq is the node's desired_seq the payload was read at; it is the
	// first part of the revision.
	DesiredSeq int64 `json:"desiredSeq"`
	// Apps are the apps placed on this node, with staged artifacts and
	// resolved secret values.
	Apps []App `json:"apps"`
	// Keep lists appIds whose previous instance and volume entries are carried
	// over unchanged, for example because staging or secret resolution failed
	// this time. An id in Keep must not also be in Apps.
	Deleted []string `json:"deleted,omitempty"`
	Keep    []string `json:"keep,omitempty"`
	// Previous is the current desired.json, or empty when there is none. Its
	// apps that are not in Apps or Keep become stopped tombstones.
	Previous json.RawMessage `json:"previous,omitempty"`
	// Limits are the node-local bounds of the 7.3 validation.
	Limits Limits `json:"limits"`
}

// App is one resolved app as the agent hands it to Render.
type App struct {
	GenericBeta         bool   `json:"genericBeta,omitzero"`
	AppID               string `json:"appId"`        // ^d-[a-z0-9]{15}$
	DeploymentID        string `json:"deploymentId"` // <appId>-dep-<n>
	LocalOnly           bool   `json:"localOnly,omitempty"`
	ProxyOnly           bool   `json:"proxyOnly,omitempty"`
	ManagedCrew         bool   `json:"managedCrew,omitzero"`
	CrewFreshEnrollment bool   `json:"crewFreshEnrollment,omitzero"`
	CrewReplicaBucket   string `json:"crewReplicaBucket,omitempty"`
	AWSRegion           string `json:"awsRegion,omitempty"`
	// CrewOwnerEmail is the immutable address Drop verified at creation.
	CrewOwnerEmail     string `json:"crewOwnerEmail,omitempty"`
	SignInEmailEnabled bool   `json:"signInEmailEnabled,omitzero"`
	// ReplicaSyncSeconds is a managed Crew app's Litestream sync interval
	// (1-300 s), handed to the guest as DROP_REPLICA_SYNC_INTERVAL; zero
	// leaves the guest's own default.
	ReplicaSyncSeconds int `json:"replicaSyncSeconds,omitzero"`
	// HostS3 gives a managed Crew app the node's S3 proxy (nibrunner
	// hostServices.s3) instead of a workload identity token: the guest holds
	// no AWS credential. The node agent sets it (DROP_AGENT_HOST_SERVICES).
	HostS3 bool `json:"hostS3,omitzero"`
	// DesiredState is running, on-request or stopped.
	DesiredState string `json:"desiredState"`
	// IdleTimeoutMs is the traffic-idle sleep timeout of an on-request app,
	// 60000–86400000; ignored for running and stopped apps.
	IdleTimeoutMs  int64       `json:"idleTimeoutMs,omitempty"`
	Volume         Volume      `json:"volume"`
	Release        Release     `json:"release"`
	ExecutableName string      `json:"executableName"` // ^[a-z][a-z0-9-]{0,31}$
	HTTPPort       int         `json:"httpPort"`       // Guest HTTP listener port (1–65535).
	HookPort       int         `json:"hookPort,omitzero"`
	Args           []string    `json:"args"`
	Resources      Resources   `json:"resources"`
	HealthCheck    HealthCheck `json:"healthCheck"`
	Hostnames      []Hostname  `json:"hostnames"`
	// Env holds the app's non-secret environment.
	Env map[string]string `json:"env"`
	// Secrets carry resolved values. Values end up in desired.json only.
	Secrets []Secret `json:"secrets"`
}

// Volume is the app's data volume. VolumeID must be <appId>-vol.
type Volume struct {
	VolumeID  string `json:"volumeId"`
	SizeBytes int64  `json:"sizeBytes"` // whole MiB, 256–16384 MiB
}

// Release is the staged executable. The agent stores it under its sha256, so
// the layer's objectKey is the sha256 (nibrun's localArtifactReference).
type Release struct {
	SHA256    string `json:"sha256"`
	S3Key     string `json:"s3Key"` // releases/<sha256>
	SizeBytes int64  `json:"sizeBytes"`
}

// Resources are the microVM's size.
type Resources struct {
	CPUQuarters int `json:"cpuQuarters,omitempty"`
	VCPU        int `json:"vcpu"`      // 1–2
	MemoryMib   int `json:"memoryMib"` // 128–4096
}

// HealthCheck is tcp, or http with a Path. Timings are Drop's fixed defaults.
type HealthCheck struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
}

// Hostname is one HTTP hostname routed to the app (kind platform or custom).
type Hostname struct {
	Hostname string `json:"hostname"`
	Kind     string `json:"kind"`
}

// Secret is one resolved secret. Parameter must be
// <ssmPrefix>/apps/<appId without "d-">/<Name>.
type Secret struct {
	Name      string `json:"name"`
	Parameter string `json:"parameter"`
	Version   int64  `json:"version"`
	Value     string `json:"value"`
}

// Limits are the node-local bounds of the validation.
type Limits struct {
	CPUQuarterQuota bool `json:"cpuQuarterQuota,omitempty"`
	// AllowedHostSuffix, for example ".bigconfig.space"; every hostname must
	// end with it.
	AllowedHostSuffix string `json:"allowedHostSuffix"`
	// SSMPrefix, for example "/drop".
	SSMPrefix string `json:"ssmPrefix"`
	// AllocatableVCPU and AllocatableMemoryMib bound one app; 0 skips the check.
	AllocatableVCPU      int `json:"allocatableVcpu,omitempty"`
	AllocatableMemoryMib int `json:"allocatableMemoryMib,omitempty"`
}

// Output is a rendered document.
type Output struct {
	// Document is the canonical JSON of the whole desired.json, ending in a
	// newline: keys sorted, two-space indentation, no HTML escaping.
	Document []byte
	// Revision is drop-<desiredSeq>-<first 12 hex of ContentHash>.
	Revision string
	// ContentHash is the hex sha256 of the compact canonical document without
	// its revision; compare it with ContentHash(previous) to skip a write.
	ContentHash string
	// Rejected lists apps refused alone; their previous entries, if any, were
	// kept unchanged, otherwise they are left out.
	Rejected []AppError
	// Tombstoned lists apps rendered as stopped tombstones, sorted.
	Tombstoned []string
}

// Error codes of a whole-document refusal.
const (
	CodeForeignEntries     = "foreign_entries"
	CodeInvalidInput       = "invalid_input"
	CodeInvalidPrevious    = "invalid_previous"
	CodeHostMismatch       = "host_mismatch"
	CodeUnsupportedEntries = "unsupported_entries"
	CodeSchema             = "schema_violation"
)

// Error codes of one app's refusal.
const (
	CodeInvalidApp   = "invalid_app"
	CodeKeepMissing  = "keep_missing"
	CodeVolumeResize = "volume_resize"
)

// Error refuses the whole input; nothing is rendered.
type Error struct {
	Code    string
	Message string
	// IDs lists the offending appIds (foreign_entries) or other ids, sorted.
	IDs []string
}

func (e *Error) Error() string {
	if len(e.IDs) > 0 {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Message, strings.Join(e.IDs, ", "))
	}
	return e.Code + ": " + e.Message
}

// AppError refuses one app. Messages never contain secret or env values.
type AppError struct {
	AppID   string `json:"appId"`
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (e AppError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s %s: %s: %s", e.AppID, e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("%s %s: %s", e.AppID, e.Code, e.Message)
}

// Fixed parts of every rendered instance.
const (
	appDir     = "/app"
	dataDir    = "/app/data"
	envHost    = "NIBRUN_HOSTNAME"
	envHostRef = "${NIBRUN_HOSTNAME}"
)

// Drop's fixed health check timings and restart policy (the values crew-dev
// ran with on walter-aws).
var (
	healthTiming = healthCheck{IntervalMs: 2000, TimeoutMs: 2000, GracePeriodMs: 60000, HealthyThreshold: 1, UnhealthyThreshold: 3}
	restart      = restartPolicy{MaxRestarts: 3, InitialBackoffMs: 500, MaxBackoffMs: 30000, BackoffFactor: 2, ResetAfterMs: 60000}
)

// Render produces the whole desired.json for in. It returns an *Error when the
// input as a whole is refused; a bad app is refused alone and listed in
// Output.Rejected.
func Render(in Input) (*Output, error) {
	if err := checkForeign(in); err != nil {
		return nil, err
	}
	if err := checkInput(in); err != nil {
		return nil, err
	}
	prev, err := parsePrevious(in.Previous, in.HostID)
	if err != nil {
		return nil, err
	}

	out := &Output{}
	instances := map[string]instance{}
	volumes := map[string]volume{}
	keepPrevious := func(id string) {
		if inst, ok := prev.instances[id]; ok {
			instances[id] = inst
		}
		if vol, ok := prev.volumes[id]; ok && vol.DesiredState == "present" {
			volumes[id] = vol
		}
	}

	for _, app := range in.Apps {
		if appErr := validateApp(app, in.Limits); appErr != nil {
			out.Rejected = append(out.Rejected, *appErr)
			keepPrevious(app.AppID)
			continue
		}
		if old, ok := prev.volumes[app.AppID]; ok && old.SizeBytes != app.Volume.SizeBytes {
			out.Rejected = append(out.Rejected, AppError{AppID: app.AppID, Code: CodeVolumeResize, Field: "volume.sizeBytes", Message: "a placed volume cannot change size"})
			keepPrevious(app.AppID)
			continue
		}
		instances[app.AppID] = renderInstance(app)
		volumes[app.AppID] = volume{VolumeID: app.Volume.VolumeID, AppID: app.AppID, SizeBytes: app.Volume.SizeBytes, DesiredState: "present"}
	}
	for _, id := range in.Keep {
		_, hasInst := prev.instances[id]
		_, hasVol := prev.volumes[id]
		if !hasInst && !hasVol {
			out.Rejected = append(out.Rejected, AppError{AppID: id, Code: CodeKeepMissing, Message: "no previous entry to keep"})
			continue
		}
		keepPrevious(id)
	}
	// Tombstones: previous apps the input no longer names.
	for id, inst := range prev.instances {
		if _, ok := instances[id]; ok || inputNames(in, id) {
			continue
		}
		inst.DesiredState = "stopped"
		inst.Activation = nil
		inst.SignInEmail = nil
		inst.HostServices = nil
		instances[id] = inst
		out.Tombstoned = append(out.Tombstoned, id)
	}
	for id, vol := range prev.volumes {
		if _, ok := volumes[id]; !ok && !inputNames(in, id) {
			if vol.DesiredState == "present" {
				volumes[id] = vol
			}
		}
	}
	// A purge can only target a locally known volume with its canonical ID.
	// Re-emit absent only while the control plane explicitly authorizes it.
	for _, id := range in.Deleted {
		if vol, ok := prev.volumes[id]; ok {
			vol.DesiredState = "absent"
			volumes[id] = vol
			delete(instances, id)
		}
	}
	sort.Strings(out.Tombstoned)

	if err := checkHostnames(instances); err != nil {
		return nil, err
	}

	doc := document{
		HostID:      in.HostID,
		Volumes:     sortedValues(volumes),
		Instances:   sortedValues(instances),
		Checkpoints: []json.RawMessage{},
		Exports:     []json.RawMessage{},
	}
	hash, err := contentHashOf(doc)
	if err != nil {
		return nil, err
	}
	doc.Revision = fmt.Sprintf("drop-%d-%s", in.DesiredSeq, hash[:12])
	data, err := canonicalIndent(doc)
	if err != nil {
		return nil, err
	}
	if err := ValidateSchema(data); err != nil {
		return nil, &Error{Code: CodeSchema, Message: err.Error()}
	}
	out.Document = data
	out.Revision = doc.Revision
	out.ContentHash = hash
	return out, nil
}

// inputNames reports whether the input names id in Apps or Keep, so a
// rejected or unkeepable app is never tombstoned by mistake.
func inputNames(in Input, id string) bool {
	if slices.Contains(in.Keep, id) {
		return true
	}
	for _, app := range in.Apps {
		if app.AppID == id {
			return true
		}
	}
	return false
}

func renderEnvironment(app App) map[string]string {
	env := make(map[string]string, len(app.Env)+len(app.Secrets)+3)
	for k, v := range app.Env {
		env[k] = v
	}
	for _, s := range app.Secrets {
		env[s.Name] = s.Value
	}
	if app.CrewOwnerEmail != "" {
		env["DROP_OWNER_EMAIL"] = app.CrewOwnerEmail
	}
	if app.ManagedCrew {
		env["POCKETCONTEXT_DOMAIN"] = app.Hostnames[0].Hostname
		env["CREW_DROP_APP_ID"] = app.AppID
		if !app.HostS3 && !app.LocalOnly && !app.ProxyOnly {
			env["AWS_WORKLOAD_IDENTITY"] = "async"
		}
		if !app.LocalOnly {
			env["CREW_DROP_BUCKET"] = app.CrewReplicaBucket
		}
		env["AWS_REGION"] = app.AWSRegion
		if app.CrewFreshEnrollment {
			env["CREW_DROP_BOOTSTRAP"] = "1"
		}
		if app.ReplicaSyncSeconds != 0 {
			env[appspec.EnvReplicaSyncInterval] = appspec.ReplicaSyncInterval(app.ReplicaSyncSeconds)
		}
	}
	if mode := env["AWS_WORKLOAD_IDENTITY"]; mode == "1" || mode == "async" {
		env["AWS_ROLE_SESSION_NAME"] = app.AppID
	}
	env[envHost] = envHostRef
	env["NIBRUN_DATA_DIR"] = dataDir
	if !app.GenericBeta {
		env["HOME"] = dataDir
	}
	return env
}

func renderInstance(app App) instance {
	args := append([]string{}, app.Args...)
	program := appDir + "/" + app.ExecutableName

	hc := healthTiming
	hc.Kind = app.HealthCheck.Kind
	if hc.Kind == "http" {
		hc.Path = app.HealthCheck.Path
	}

	hostnames := toHostnames(app.Hostnames)
	sort.Slice(hostnames, func(i, j int) bool { return hostnames[i].Hostname < hostnames[j].Hostname })

	inst := instance{
		AppID:        app.AppID,
		DeploymentID: app.DeploymentID,
		VolumeID:     app.Volume.VolumeID,
		DesiredState: app.DesiredState,
		Layers: []layer{{
			Kind:            "executable",
			DestinationPath: program,
			Digest:          app.Release.SHA256,
			ObjectKey:       app.Release.SHA256,
		}},
		Config: config{
			HTTPPort: app.HTTPPort,
			Command: command{
				Program:          program,
				Args:             args,
				WorkingDirectory: appDir,
				Environment:      renderEnvironment(app),
			},
			Resources:     resources{CPUQuarters: app.Resources.CPUQuarters, VCPUCount: app.Resources.VCPU, MemoryMib: app.Resources.MemoryMib},
			HealthCheck:   hc,
			RestartPolicy: restart,
		},
		Hostnames: hostnames,
		Access:    access{Kind: "public"},
	}
	if app.HookPort != 0 {
		inst.Config.Hooks = &hooks{Port: app.HookPort}
	}
	if app.SignInEmailEnabled && app.DesiredState != appspec.StateStopped {
		inst.SignInEmail = &signInEmail{Recipient: app.CrewOwnerEmail}
	}
	if app.HostS3 && app.DesiredState != appspec.StateStopped {
		inst.HostServices = &hostServices{S3: true}
	}
	if app.DesiredState == "on-request" {
		inst.Activation = &activation{SleepWhen: sleepWhen{Kind: "traffic-idle", TimeoutMs: app.IdleTimeoutMs}}
	}
	return inst
}

func toHostnames(in []Hostname) []hostname {
	out := make([]hostname, len(in))
	for i, h := range in {
		out[i] = hostname{Hostname: h.Hostname, Kind: h.Kind}
	}
	return out
}

func sortedValues[T any](m map[string]T) []T {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// checkHostnames refuses a document in which two instances claim one hostname.
func checkHostnames(instances map[string]instance) error {
	owner := map[string]string{}
	var dup []string
	for _, id := range slices.Sorted(maps.Keys(instances)) {
		for _, h := range instances[id].Hostnames {
			if other, ok := owner[h.Hostname]; ok && other != id {
				dup = append(dup, h.Hostname)
				continue
			}
			owner[h.Hostname] = id
		}
	}
	if len(dup) > 0 {
		sort.Strings(dup)
		return &Error{Code: CodeInvalidInput, Message: "hostnames claimed by more than one app", IDs: slices.Compact(dup)}
	}
	return nil
}
