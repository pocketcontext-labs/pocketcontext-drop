package dropapi

import "regexp"

// Node, placement and status operations (milestone M7). Nodes are the hosts
// that run apps; each runs one Drop agent that signs in as its node (an auth
// record of the nodes collection, never a users account) and talks only to
// the agent routes (agent.go).

// Node states. An active node takes placements; a draining node keeps its
// apps but takes no new ones; a disabled node cannot sign in or reach the
// agent routes (its apps stay placed until an admin moves them).
const (
	NodeActive   = "active"
	NodeDraining = "draining"
	NodeDisabled = "disabled"
)

// NodeNamePattern is a node's name, which is also its sign-in identity.
var NodeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

// HostIDPattern is nibrunner's host identifier (reported.json hostId).
var HostIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// NodePasswordLength is the length of a generated node password.
const NodePasswordLength = 40

// NodeOnlineSeconds: a node that reported within this many seconds is online.
const NodeOnlineSeconds = 90

// Node is a node as admins see it. Online is true when the agent reported in
// the last NodeOnlineSeconds. Capacity, Allocatable and Versions are
// nibrunner's, from the agent's reports; Apps counts the apps placed on it.
type Node struct {
	MemoryHistory    []GuestMemory  `json:"memory_history"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	HostID           string         `json:"host_id"`
	EC2InstanceID    string         `json:"ec2_instance_id"`
	PrivateIP        string         `json:"private_ip"`
	State            string         `json:"state"`
	DesiredSeq       int            `json:"desired_seq"`
	Online           bool           `json:"online"`
	LastHeartbeat    string         `json:"last_heartbeat"`
	LastReportAt     string         `json:"last_report_at"`
	HostState        string         `json:"host_state"`
	AgentVersion     string         `json:"agent_version"`
	AcceptedRevision string         `json:"accepted_revision"`
	Capacity         map[string]any `json:"capacity"`
	Allocatable      map[string]any `json:"allocatable"`
	Versions         map[string]any `json:"versions"`
	Apps             int            `json:"apps"`
	Created          string         `json:"created"`
}

// NodeListInput selects one page of nodes, by name.
type NodeListInput struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// NodeList is a page of nodes.
type NodeList struct {
	Nodes   []Node `json:"nodes"`
	HasMore bool   `json:"has_more"`
}

// NodeCreateInput adds a node: its name (the agent's sign-in identity) and
// nibrunner's host id, optionally its EC2 instance id and private address
// (for people; Drop does not use them).
type NodeCreateInput struct {
	Name          string `json:"name"`
	HostID        string `json:"host_id"`
	EC2InstanceID string `json:"ec2_instance_id,omitempty"`
	PrivateIP     string `json:"private_ip,omitempty"`
}

// NodeRef names one node by id or name.
type NodeRef struct {
	Node string `json:"node"`
}

// NodeRotateSecretInput replaces a node's password; the old one and every
// token issued with it stop working.
type NodeRotateSecretInput NodeRef

// NodeSetStateInput changes a node's state (active, draining or disabled).
// Disabling also ends its current tokens.
type NodeSetStateInput struct {
	Node  string `json:"node"`
	State string `json:"state"`
}

// NodeSecret is the answer of node.create and node.rotate_secret. Password
// is shown once and never stored in clear: give it to the agent as its
// node-secret credential (docs/agent-api.md). Identity and Collection are how
// the agent signs in (POST /api/collections/nodes/auth-with-password).
type NodeSecret struct {
	Node       Node   `json:"node"`
	Password   string `json:"password"`
	Identity   string `json:"identity"`
	Collection string `json:"collection"`
}

// NodeResult is the answer of node.set_state.
type NodeResult struct {
	Node Node `json:"node"`
}

// Placement is where one app runs.
type Placement struct {
	App        string `json:"app"`
	Node       string `json:"node"`
	NodeName   string `json:"node_name"`
	Generation int    `json:"generation"`
	Updated    string `json:"updated"`
}

// PlacementSetInput places an app (any account's, by id or slug) on a node
// (by id or name). Moving it to another node leaves its data on the old one
// (no volume moves in v0).
type PlacementSetInput struct {
	App  string `json:"app"`
	Node string `json:"node"`
}

// PlacementClearInput removes an app's placement: its node stops it and
// keeps its data.
type PlacementClearInput struct {
	App string `json:"app"`
}

// PlacementResult is the answer of placement.set: Moved is true when the app
// left another node (PreviousNode).
type PlacementResult struct {
	Placement    Placement `json:"placement"`
	Moved        bool      `json:"moved"`
	PreviousNode string    `json:"previous_node,omitempty"`
}

// PlacementClearResult is the answer of placement.clear.
type PlacementClearResult struct {
	App         string `json:"app"`
	ClearedNode string `json:"cleared_node"`
}

// StatusInput names one app.
type StatusInput struct {
	App       string `json:"app"`
	VerifyURL bool   `json:"verify_url,omitempty"`
}

// Phases of an app. The agent reports the node phases (ReportPhases); the
// status operation adds archived, no_release and unplaced, and turns a
// report into deploying while the node still runs an older deployment and
// into unknown when the node stopped reporting.
const (
	PhaseArchived  = "archived"
	PhaseNoRelease = "no_release"
	PhaseUnplaced  = "unplaced"
	PhasePending   = "pending"
	PhaseDeploying = "deploying"
	PhaseUnknown   = "unknown"
)

// Status is an app's runtime status. Node is the node's name, for admins
// only. ReportedDeploymentID is the deployment the node last reported, which
// differs from DeploymentID while a new deployment rolls out. ObservedAt is
// when the node first reported this phase; LastSeen when it last reported at
// all.
type Status struct {
	FirstRun             *FirstRun   `json:"first_run,omitempty"`
	LogTail              string      `json:"log_tail,omitempty"`
	URLReady             bool        `json:"url_ready,omitempty"`
	App                  string      `json:"app"`
	Phase                string      `json:"phase"`
	Message              string      `json:"message"`
	DesiredState         string      `json:"desired_state"`
	DeploymentID         string      `json:"deployment_id"`
	ReportedDeploymentID string      `json:"reported_deployment_id"`
	Hostname             string      `json:"hostname"`
	Placed               bool        `json:"placed"`
	Node                 string      `json:"node,omitempty"`
	RestartCount         int         `json:"restart_count"`
	LastExitCode         *int        `json:"last_exit_code"`
	LastHook             *HookResult `json:"last_hook,omitempty"`
	ConvergedAt          string      `json:"converged_at"`
	ObservedAt           string      `json:"observed_at"`
	LastSeen             string      `json:"last_seen"`
}

// Error codes of nodes and placements.
const (
	CodeNodeExists      = "node_exists"
	CodeNodeUnavailable = "node_unavailable"
	CodeNodeTooSmall    = "node_too_small"
	CodeDiskLocked      = "disk_locked"
	CodeNodeDisabled    = "node_disabled"
	CodeHostMismatch    = "host_mismatch"
	CodeDesiredFailed   = "desired_unavailable"
)
