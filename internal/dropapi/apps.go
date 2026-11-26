package dropapi

// App and secret operations (milestone M5a). The value rules (ranges, names,
// the environment) are internal/appspec's, shared with the node's renderer.

// Name and slug rules of an app.
const (
	MaxAppName = 80
	// SlugRule describes SlugPattern for people.
	SlugRule = "3-32 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen"
	// MaxEnvJSONBytes bounds an app's plain environment as stored JSON.
	MaxEnvJSONBytes = 32 << 10
	// MaxSecretBytes is the largest secret value (SSM's standard tier).
	MaxSecretBytes = 4096
)

// SlugPattern is the slug of an app: its hostname is <slug>.<app domain>.
const SlugPattern = `^[a-z][a-z0-9-]{1,30}[a-z0-9]$`

// ReservedSlugs are slugs nobody may take (Drop's own and infrastructure names).
var ReservedSlugs = []string{"drop", "drop-dev", "crew-dev", "www", "api", "mail", "canary"}

// Where a secret value was set: through the reader or the CLI ("ui"), or
// through the MCP tool drop_write ("mcp"; its value passed through an
// assistant's conversation, so the reader marks it with Replace).
const (
	SetViaUI  = "ui"
	SetViaMCP = "mcp"
)

// App is one app as its owner (or an admin) sees it. DeploymentID is
// <nib_app_id>-dep-<deployment_seq>; the deployment number goes up whenever
// the instance configuration changes (see docs/ops.md). Env holds plain values
// only: secrets are listed by name with secret.list and never returned.
type App struct {
	BetaSHA256          string `json:"beta_sha256,omitempty"`
	BetaManaged         bool   `json:"beta_managed,omitempty"`
	Kind                string `json:"kind"`
	CrewOwnerEmail      string `json:"crew_owner_email,omitempty"`
	SignInEmailEnabled  bool   `json:"sign_in_email_enabled"`
	CrewFreshEnrollment bool   `json:"crew_fresh_enrollment,omitempty"`
	// ReplicaSyncInterval is a Crew app's Litestream sync interval ("10s");
	// empty on ordinary apps and on Crew apps that keep the guest's default.
	ReplicaSyncInterval string `json:"replica_sync_interval,omitempty"`

	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Slug           string            `json:"slug"`
	Owner          string            `json:"owner"`
	OwnerEmail     string            `json:"owner_email"`
	NibAppID       string            `json:"nib_app_id"`
	VolumeID       string            `json:"volume_id"`
	Hostname       string            `json:"hostname"`
	DesiredState   string            `json:"desired_state"`
	IdleTimeoutMs  int               `json:"idle_timeout_ms"`
	VCPU           int               `json:"vcpu"`
	MemoryMiB      int               `json:"memory_mib"`
	DiskMiB        int               `json:"disk_mib"`
	HTTPPort       int               `json:"http_port"`
	HookPort       int               `json:"hook_port"`
	ExecutableName string            `json:"executable_name"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	HealthKind     string            `json:"health_kind"`
	HealthPath     string            `json:"health_path"`
	DeploymentSeq  int               `json:"deployment_seq"`
	DeploymentID   string            `json:"deployment_id"`
	// CurrentRelease is the id of the release the app runs (release.deploy),
	// empty before its first deploy.
	CurrentRelease string `json:"current_release"`
	// DiskLocked is true once the app has been placed on a node: its disk
	// size can no longer change.
	DiskLocked bool   `json:"disk_locked"`
	Archived   bool   `json:"archived"`
	ArchivedAt string `json:"archived_at"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
}

// AppRef names one app by id or slug.
type AppRef struct {
	App string `json:"app"`
}

// AppListInput selects one page of apps, newest first: the caller's own, or
// with All (admins only) everyone's. Archived lists archived apps instead.
type AppListInput struct {
	All      bool `json:"all,omitempty"`
	Archived bool `json:"archived,omitempty"`
	Limit    int  `json:"limit,omitempty"`
	Offset   int  `json:"offset,omitempty"`
}

// Quota is the caller's app quota: Used non-archived apps of Limit. Admins
// are Unlimited.
type Quota struct {
	Used      int  `json:"used"`
	Limit     int  `json:"limit"`
	Unlimited bool `json:"unlimited"`
}

// AppList is a page of apps and the caller's quota.
type AppList struct {
	Apps    []App `json:"apps"`
	HasMore bool  `json:"has_more"`
	Quota   Quota `json:"quota"`
}

// AppGetInput names the app to read.
type AppGetInput = AppRef

// AppDetail is one app with the names of its secrets.
type AppDetail struct {
	App     App      `json:"app"`
	Secrets []Secret `json:"secrets"`
}

// AppCreateInput creates an app. Absent settings take the defaults:
// on-request, 300000 ms idle timeout, 1 vCPU, 512 MiB memory, 1024 MiB disk,
// port 8080, executable "server", no arguments or environment, a tcp health
// check.
type AppCreateInput struct {
	Name           string            `json:"name"`
	Slug           string            `json:"slug"`
	Kind           string            `json:"kind,omitempty"`
	DesiredState   string            `json:"desired_state,omitempty"`
	IdleTimeoutMs  *int              `json:"idle_timeout_ms,omitempty"`
	VCPU           *int              `json:"vcpu,omitempty"`
	MemoryMiB      *int              `json:"memory_mib,omitempty"`
	DiskMiB        *int              `json:"disk_mib,omitempty"`
	HTTPPort       *int              `json:"http_port,omitempty"`
	HookPort       *int              `json:"hook_port,omitempty"`
	ExecutableName string            `json:"executable_name,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	HealthKind     string            `json:"health_kind,omitempty"`
	HealthPath     string            `json:"health_path,omitempty"`
}

// EnvChange sets and removes plain environment variables. A name may not be
// in both.
type EnvChange struct {
	Set   map[string]string `json:"set,omitempty"`
	Unset []string          `json:"unset,omitempty"`
}

// AppSetInput changes an app's settings. Absent fields stay; Args replaces
// the arguments. The slug cannot change.
type AppSetInput struct {
	App            string     `json:"app"`
	Name           *string    `json:"name,omitempty"`
	DesiredState   *string    `json:"desired_state,omitempty"`
	IdleTimeoutMs  *int       `json:"idle_timeout_ms,omitempty"`
	VCPU           *int       `json:"vcpu,omitempty"`
	MemoryMiB      *int       `json:"memory_mib,omitempty"`
	DiskMiB        *int       `json:"disk_mib,omitempty"`
	HTTPPort       *int       `json:"http_port,omitempty"`
	HookPort       *int       `json:"hook_port,omitempty"`
	ExecutableName *string    `json:"executable_name,omitempty"`
	Args           *[]string  `json:"args,omitempty"`
	Env            *EnvChange `json:"env,omitempty"`
	HealthKind     *string    `json:"health_kind,omitempty"`
	HealthPath     *string    `json:"health_path,omitempty"`

	FinishEnrollment   bool  `json:"finish_enrollment,omitempty"`
	SignInEmailEnabled *bool `json:"sign_in_email_enabled,omitempty"`
	// ReplicaSyncInterval is a Crew app's Litestream sync interval, a Go
	// duration of whole seconds from "1s" to "300s" (default "10s"), handed
	// to the guest as DROP_REPLICA_SYNC_INTERVAL.
	ReplicaSyncInterval *string `json:"replica_sync_interval,omitempty"`
}

// AppResult is a created or changed app.
type AppResult struct {
	App App `json:"app"`
}

// AppSetResult is the changed app and whether its deployment number went up.
type AppSetResult struct {
	App              App  `json:"app"`
	DeploymentBumped bool `json:"deployment_bumped"`
}

// Inputs of the operations that only name an app.
type (
	AppStartInput   = AppRef
	AppStopInput    = AppRef
	AppArchiveInput = AppRef
	AppRestoreInput = AppRef
	SecretListInput = AppRef
)

// Secret is one secret's name and metadata. The value is never returned.
type Secret struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Length    int    `json:"length"`
	SetVia    string `json:"set_via"`
	UpdatedBy string `json:"updated_by"`
	Updated   string `json:"updated"`
}

// SecretList is an app's secrets, by name.
type SecretList struct {
	Secrets []Secret `json:"secrets"`
}

// SecretSetInput stores a secret value (1-4096 bytes) as a new version.
type SecretSetInput struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SecretSetResult is the secret's metadata and the app's new deployment.
type SecretSetResult struct {
	Secret           Secret `json:"secret"`
	DeploymentBumped bool   `json:"deployment_bumped"`
	DeploymentID     string `json:"deployment_id"`
}

// SecretDeleteInput removes a secret and all its versions.
type SecretDeleteInput struct {
	App  string `json:"app"`
	Name string `json:"name"`
}

// SecretDeleteResult names the removed secret and the app's new deployment.
type SecretDeleteResult struct {
	Deleted          string `json:"deleted"`
	DeploymentBumped bool   `json:"deployment_bumped"`
	DeploymentID     string `json:"deployment_id"`
}
