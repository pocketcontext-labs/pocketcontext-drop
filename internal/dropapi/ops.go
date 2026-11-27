// Package dropapi is the wire contract of Drop's operations, shared by the
// server (internal/dropserver), the CLI, the MCP tools and the reader's
// contract test: the request, result and error shapes of POST /api/drop/op and
// the registry of operation names, roles and inputs. It holds no server code.
//
// Every write in Drop goes through one route, POST /api/drop/op, with
// {op, input, idempotency_key?}. The server looks the name up in Specs, checks
// the caller's role, decodes input into the spec's input type (unknown fields
// are refused) and runs the operation in one transaction that writes one events
// row. MCP's drop_write tool generates its op enum and table from Specs.
package dropapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// Route is the one operation route; MeRoute answers the "me" operation to GET.
const (
	Route   = "/api/drop/op"
	MeRoute = "/api/drop/me"
)

// Role identifies a hosted account or the required role for an operation.
type Role string

const (
	RoleUser      Role = "user"
	RoleDeveloper Role = "developer"
	RoleAdmin     Role = "admin"
)

// Request is the body of POST /api/drop/op. Input is the operation's own
// object (empty or absent means {}). IdempotencyKey makes a mutating
// operation safe to retry: the same key and input within 24 hours replay the
// first result, a different input under the same key is refused.
type Request struct {
	Op             string          `json:"op"`
	Input          json.RawMessage `json:"input,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

// Response is the body of a successful call.
type Response struct {
	Result json.RawMessage `json:"result"`
	// Replayed is true when an idempotency key replayed an earlier result.
	Replayed bool `json:"replayed,omitempty"`
}

// MaxRequestBytes bounds one operation request body.
const MaxRequestBytes = 256 << 10

// IdempotencyKeyPattern is the form of an idempotency key.
var IdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Spec describes one operation. Input is a pointer to the zero value of its
// input type (the server decodes into a fresh copy; MCP derives its schema).
type Spec struct {
	Name    string
	Role    Role
	Summary string
	// Read operations change nothing: they run outside a transaction and
	// write no event; an idempotency key is ignored.
	Read  bool
	Input any
	// OnceResult marks a result shown only once (for example a generated
	// credential): it is never stored for idempotent replay, and a replayed
	// key answers 409 idempotency_result_unavailable instead.
	OnceResult bool
	// SecretInput marks an input that holds a secret value. Its idempotency
	// key is ignored, so not even a hash of the input is stored; calling the
	// operation again is safe (secret.set stores the same value as a new
	// version).
	SecretInput bool
	// MCP offers the operation through the MCP tool drop_write: its op enum
	// and op table are generated for the account role from specs with MCP set. Only mutating operations take
	// it; reads reach MCP through drop_read's own table (internal/client).
	MCP bool
	// Standalone marks a mutating operation that does slow outside work (the
	// usage collector reads S3): it runs outside the framework's transaction,
	// makes its own atomic writes and writes its own event; an idempotency
	// key is ignored.
	Standalone bool
}

var opName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// specs is the registry, in the order the documentation and MCP list them.
// Later milestones add their operations here (and their handlers in
// internal/dropserver); a test checks that both sides agree.
var specs = []Spec{
	{Name: "catalog.list", Role: RoleUser, Input: &CatalogListInput{}, Read: true, Summary: "List current official workspace releases."},
	{Name: "catalog.import_url", Role: RoleAdmin, Standalone: true, Input: &OfficialImportURL{}, Summary: "Download and check a catalog candidate from a public HTTPS URL."},
	{Name: "catalog.upload", Role: RoleAdmin, Input: &OfficialUpload{}, OnceResult: true, Summary: "Upload a candidate for product and version checks."},
	{Name: "catalog.rehearse", Role: RoleAdmin, Input: &OfficialRehearsal{}, Summary: "Record fixture evidence for an exact candidate and baseline."},
	{Name: "catalog.archive", Role: RoleAdmin, Input: &BetaDigest{}, Summary: "Archive a published release and retain installed copies."},
	{Name: "catalog.submissions", Role: RoleAdmin, Read: true, Input: &MeInput{}, Summary: "Review private releases explicitly submitted for the catalog."},
	{Name: "catalog.accept", Role: RoleAdmin, Input: &AcceptSubmission{}, Summary: "Stage the reviewed private release digest for rehearsal and publication."},
	{Name: "developer.submit", Role: RoleDeveloper, Input: &ReleaseDeployInput{}, MCP: true, Summary: "Submit the currently deployed private release of your app for official catalog review."},
	{Name: "catalog.publish", Role: RoleAdmin, Input: &OfficialPublish{}, Summary: "Publish a checked candidate for new workspaces."},
	{Name: "catalog.activate", Role: RoleAdmin, Input: &MeInput{}, Summary: "Switch hosted customer deployment to the official catalog."},
	{Name: "workspace.create", Role: RoleUser, Input: &WorkspaceCreate{}, MCP: true, Summary: "Provision an official workspace at a selected size."},
	{Name: "workspace.resize", Role: RoleUser, Input: &WorkspaceResize{}, MCP: true, Summary: "Change workspace resources and resize the disk on a supported node."},
	{Name: "workspace.upgrade", Role: RoleUser, Input: &WorkspaceUpgrade{}, MCP: true, Summary: "Install the reviewed current official release on one workspace."},
	{Name: "user.directory", Role: RoleAdmin, Input: &DirectoryInput{}, Read: true, Summary: "Search users and access requests with filters and pagination."},

	{Name: "credits.accounts", Role: RoleAdmin, Read: true, Input: &CreditAccountListInput{}, Summary: "List account balances and simulated purchases for the administrator."},
	{Name: "credits.get", Role: RoleUser, Read: true, Input: &CreditViewInput{}, Summary: "Read your balance and ledger. Administrators can select an account."},
	{Name: "developer.build_upload", Role: RoleDeveloper, MCP: true, OnceResult: true, Input: &MeInput{}, Summary: "Get a one-time PUT URL to inspect a custom executable."},
	{Name: "developer.build_import", Role: RoleDeveloper, MCP: true, Standalone: true, Input: &ImportURLInput{}, Summary: "Download and inspect a custom executable from a public HTTPS URL."},
	{Name: "developer.build_create", Role: RoleDeveloper, MCP: true, Input: &PrivateBuildCreate{}, Summary: "Create a custom app from your inspected executable and selected multiplier. Its hostname opens directly."},
	{Name: "developer.create", Role: RoleDeveloper, MCP: true, Input: &AppCreateInput{}, Summary: "Create your own custom app. Supply its name, unique slug and runtime settings; kind crew enables owner sign-in and recovery services."},
	{Name: "developer.upload_link", Role: RoleDeveloper, MCP: true, OnceResult: true, Input: &ReleaseUploadLinkInput{}, Summary: "Create a one-time PUT URL for a custom app you own. Upload your Linux executable directly, then use developer.deploy."},
	{Name: "developer.deploy", Role: RoleDeveloper, MCP: true, Input: &ReleaseDeployInput{}, Summary: "Place your custom app and deploy one of its uploaded releases by immutable release ID."},
	{Name: "credits.buy", Role: RoleUser, Input: &MeInput{}, Summary: "Buy one simulated credit pack."},
	{Name: "credits.adjust", Role: RoleAdmin, Input: &CreditAdjustment{}, Summary: "Record a signed adjustment to an account balance."},
	{Name: "credits.configure", Role: RoleAdmin, Input: &CreditSettings{}, Summary: "Set the simulated pack, cap, credit value, starting grant and S3 pricing conversions."},
	{Name: "me", Role: RoleUser, Read: true, Input: &MeInput{},
		Summary: "The signed-in account: id, email, role, developer access, app quota and whether it is the owner."},
	{Name: "event.list", Role: RoleUser, Read: true, Input: &EventListInput{},
		Summary: "Events (who changed what, newest first) of your apps, or of one app; all:true (admins) lists every event. Details never hold secret values."},
	{Name: "user.list", Role: RoleAdmin, Read: true, Input: &UserListInput{},
		Summary: "Every account, newest first, with role, developer access, quota, suspension and whether it has signed in."},
	{Name: "user.set", Role: RoleAdmin, MCP: true, Input: &UserSetInput{},
		Summary: "Change an account's role, app quota or suspension. Nobody changes their own role or suspension, or the owner's."},
	{Name: "waitlist.list", Role: RoleAdmin, Read: true, Input: &WaitlistListInput{}, Summary: "List verified waitlist entries, oldest first."},
	{Name: "waitlist.invite", Role: RoleAdmin, Input: &WaitlistInvite{}, Summary: "Invite a verified waitlist member and queue a welcome email."},
	{Name: "waitlist.resend", Role: RoleAdmin, Input: &WaitlistID{}, Summary: "Resend an open waitlist invitation."},
	{Name: "waitlist.revoke", Role: RoleAdmin, Input: &WaitlistID{}, Summary: "Revoke an open waitlist invitation."},
	{Name: "invite.create", Role: RoleAdmin, MCP: true, Input: &InviteCreateInput{},
		Summary: "Invite an email address (role user, developer or admin, app quota, 1-30 days, default 7). Returns the invite and a sign-in URL to share; no email is sent."},
	{Name: "invite.list", Role: RoleAdmin, Read: true, Input: &InviteListInput{},
		Summary: "Invites, newest first, with their state (open, used, revoked or expired)."},
	{Name: "invite.revoke", Role: RoleAdmin, MCP: true, Input: &InviteRevokeInput{},
		Summary: "Revoke an open invite; the address can no longer get an account."},
	{Name: "app.list", Role: RoleUser, Read: true, Input: &AppListInput{},
		Summary: "Your apps, newest first, with your retained-disk allowance in MiB; archived:true lists archived apps; all:true (admins) lists everyone's."},
	{Name: "app.get", Role: RoleUser, Read: true, Input: &AppGetInput{},
		Summary: "One app by id or slug, with the names of its secrets (never their values)."},
	{Name: "app.create", Role: RoleDeveloper, MCP: true, Input: &AppCreateInput{},
		Summary: "Create an app: name, unique slug (its hostname), kind (ordinary by default or crew for managed sign-in to your verified email), and optional settings (state, idle timeout, vCPU, memory, disk, port, executable, args, env, health check). Its volume counts against your 10 GiB retained-disk allowance."},
	{Name: "app.set", Role: RoleUser, MCP: true, Input: &AppSetInput{},
		Summary: "Change an app's settings (env as {set, unset}, sign_in_email_enabled as a boolean for Crew); a change to the instance configuration starts a new deployment. The slug cannot change; the disk cannot change once the app is placed."},
	{Name: "app.start", Role: RoleUser, MCP: true, Input: &AppStartInput{},
		Summary: "Start a stopped app in the state it had before it stopped (running or on-request)."},
	{Name: "app.stop", Role: RoleUser, MCP: true, Input: &AppStopInput{},
		Summary: "Stop an app; its data stays."},
	{Name: "app.deletions", Role: RoleUser, Read: true, Input: &AppDeletionsInput{}, Summary: "List your deletion receipts or read a receipt by immutable app ID."},
	{Name: "app.delete", Role: RoleUser, Input: &AppDeleteInput{}, Summary: "Permanently delete an app after confirming its slug."},
	{Name: "app.archive", Role: RoleUser, Input: &AppArchiveInput{},
		Summary: "Archive an app: it stops running and keeps its retained disk allocation. Its data, secrets and slug stay; app.restore brings it back."},
	{Name: "app.restore", Role: RoleUser, Input: &AppRestoreInput{},
		Summary: "Restore an archived app if placement has capacity."},
	{Name: "secret.list", Role: RoleUser, Read: true, Input: &SecretListInput{},
		Summary: "An app's secrets: name, version, length, when and how each was set. Values are never returned."},
	{Name: "secret.set", Role: RoleUser, MCP: true, Input: &SecretSetInput{}, SecretInput: true,
		Summary: "Set an app secret (an environment variable kept in AWS SSM, never stored, logged or returned) and start a new deployment. Through MCP the value passes through the assistant's conversation: prefer the reader for real secrets."},
	{Name: "secret.delete", Role: RoleUser, MCP: true, Input: &SecretDeleteInput{},
		Summary: "Delete an app secret (every version) and start a new deployment."},
	{Name: "release.list", Role: RoleUser, Read: true, Input: &ReleaseListInput{},
		Summary: "An app's releases (uploaded executables), newest first: id, sha256, size, note, and which one is current."},
	{Name: "release.upload_link", Role: RoleDeveloper, MCP: true, Input: &ReleaseUploadLinkInput{}, OnceResult: true,
		Summary: "A one-time upload link for a new release of an app: PUT a static linux/amd64 ELF executable (at most 90 MiB) to the returned URL within 15 minutes, for example with curl -fT FILE URL. activate:true deploys it once uploaded. The URL is shown once."},
	{Name: "release.deploy", Role: RoleDeveloper, MCP: true, Input: &ReleaseDeployInput{},
		Summary: "Make one of an app's releases (by id) its current release and start a new deployment."},
	{Name: "status", Role: RoleUser, Read: true, Input: &StatusInput{},
		Summary: "An app's runtime status: phase (archived, no_release, unplaced, pending, deploying, staging, starting, running, sleeping, unhealthy, stopped, failed, unknown...), message, current and reported deployment, restarts and last exit code; the node's name for admins."},
	{Name: "node.list", Role: RoleAdmin, Read: true, Input: &NodeListInput{},
		Summary: "Nodes (hosts) by name: state, whether the agent is online, desired_seq, nibrunner's capacity, allocatable and versions, and how many apps each runs."},
	{Name: "node.create", Role: RoleAdmin, MCP: true, Input: &NodeCreateInput{}, OnceResult: true,
		Summary: "Add a node: name (its agent's sign-in identity) and nibrunner's host_id. Returns a generated 40-character password for the agent, shown once."},
	{Name: "node.rotate_secret", Role: RoleAdmin, MCP: true, Input: &NodeRotateSecretInput{}, OnceResult: true,
		Summary: "Replace a node's password (shown once); the old one and the agent's current tokens stop working until it gets the new one."},
	{Name: "node.set_state", Role: RoleAdmin, MCP: true, Input: &NodeSetStateInput{},
		Summary: "Set a node active, draining (keeps its apps, takes no new ones) or disabled (its agent is locked out and its apps stop being updated)."},
	{Name: "placement.set", Role: RoleAdmin, MCP: true, Input: &PlacementSetInput{},
		Summary: "Place an app (any account's) on an active node, or move it to another one; its disk size is fixed from the first placement. A moved app stops on the old node, whose copy of its data stays there."},
	{Name: "placement.clear", Role: RoleAdmin, MCP: true, Input: &PlacementClearInput{},
		Summary: "Remove an app's placement: its node stops it and keeps its data."},
	{Name: "usage.get", Role: RoleUser, Read: true, Input: &UsageGetInput{},
		Summary: "An app's S3 usage from the replica bucket's logs: the last 24 hours by hour, the month to date against its soft and hard limits, the latest stored bytes and an estimated cost (an estimate, not a bill)."},
	{Name: "usage.set_limits", Role: RoleAdmin, Input: &UsageSetLimitsInput{},
		Summary: "Override an app's monthly soft usage limits (requests_a, requests_b, bytes_out, stored_bytes; hard limits are ten times these); reset:true returns to the server defaults."},
	{Name: "usage.collect", Role: RoleAdmin, Standalone: true, Input: &UsageCollectInput{},
		Summary: "Run the usage collector once: read new S3 access logs and the latest inventory, update usage, check limits."},
	{Name: "beta.approve", Role: RoleAdmin, Input: &BetaDigest{}, Summary: "Admit an exact release digest for inspection."},
	{Name: "beta.revoke", Role: RoleAdmin, Input: &BetaDigest{}, Summary: "Revoke a release digest and stop its Beta apps."},
	{Name: "beta.claim", Role: RoleAdmin, OnceResult: true, Input: &BetaDigest{}, Summary: "Lease inspection of a binary."},
	{Name: "beta.complete", Role: RoleAdmin, Input: &BetaCompleteInput{}, Summary: "Finish a leased manifest inspection."},
	{Name: "beta.probe_upload", Role: RoleDeveloper, OnceResult: true, Input: &BetaDigest{}, Summary: "Issue an upload link for isolated manifest inspection."},
	{Name: "beta.inspect", Role: RoleUser, Read: true, Input: &BetaDigest{}, Summary: "Look up the supported release and its available sizes."},
	{Name: "beta.import_url", Role: RoleDeveloper, Read: true, Input: &ImportURLInput{}, Summary: "Download a bounded public HTTPS release for inspection."},
	{Name: "beta.prepare", Role: RoleDeveloper, Input: &BetaPrepareInput{}, Summary: "Prepare an app for an inspected binary at the selected size."},
	{Name: "beta.deploy", Role: RoleDeveloper, Input: &ReleaseDeployInput{}, Summary: "Place and deploy an inspected release."},
}

func init() {
	seen := map[string]bool{}
	for _, s := range specs {
		if !opName.MatchString(s.Name) || seen[s.Name] || (s.Role != RoleUser && s.Role != RoleAdmin && s.Role != RoleDeveloper) || s.Input == nil || s.Summary == "" || (s.Read && (s.SecretInput || s.MCP || s.Standalone)) || (s.Standalone && (s.SecretInput || s.OnceResult)) {
			panic(fmt.Sprintf("dropapi: invalid operation spec %q", s.Name))
		}
		seen[s.Name] = true
	}
}

// Specs returns a copy of the registry.
func Specs() []Spec { return append([]Spec(nil), specs...) }

// Lookup returns the spec of an operation name.
func Lookup(name string) (Spec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Allows reports whether a hosted account may call this operation.
func (s Spec) Allows(role Role) bool {
	if role == RoleDeveloper && s.Role == RoleUser {
		return true
	}
	if role == RoleAdmin && s.Role == RoleUser {
		return s.Read || s.Name == "app.delete"
	}
	return role == s.Role
}

// LegacyDeployment identifies the generic deployment flow used before the
// official catalog was activated, also used by the local development server.
func (s Spec) LegacyDeployment() bool {
	switch s.Name {
	case "app.create", "beta.prepare", "beta.deploy", "beta.inspect", "beta.probe_upload", "beta.import_url", "release.upload_link", "release.deploy":
		return true
	default:
		return false
	}
}

// PrivateOwnerWrite identifies shared lifecycle handlers available to a
// developer managing a custom workspace through the official catalog interface.
func (s Spec) PrivateOwnerWrite() bool {
	switch s.Name {
	case "app.set", "app.start", "app.stop", "secret.set", "secret.delete":
		return true
	default:
		return false
	}
}

// MCPWriteNames returns the operations drop_write offers a role, in registry
// order: those with MCP set that the role may call.
func MCPWriteNames(role Role) []string {
	var names []string
	for _, s := range specs {
		if s.MCP && s.Allows(role) {
			names = append(names, s.Name)
		}
	}
	return names
}

// Names returns the operation names a role may call, sorted.
func Names(role Role) []string {
	var names []string
	for _, s := range specs {
		if s.Allows(role) {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Error is the body of every failed call, with the HTTP status. Code is a
// stable machine-readable reason; Message is for people; Data holds details
// such as the invalid field. It keeps PocketBase's {status, message, data}
// shape, so clients that read message work unchanged.
type Error struct {
	Status  int            `json:"status"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func (e *Error) Error() string { return e.Message }

// Error codes. Statuses: 400 invalid_input, unknown_op, invalid_request,
// invalid_idempotency_key; 401 unauthenticated; 403 forbidden, suspended;
// 404 not_found; 409 conflict codes; 500 internal; 502 secret_store_failed;
// 503 secrets_unavailable.
const (
	CodeInvalidRequest        = "invalid_request"
	CodeInvalidInput          = "invalid_input"
	CodeUnknownOp             = "unknown_op"
	CodeUnauthenticated       = "unauthenticated"
	CodeForbidden             = "forbidden"
	CodeSuspended             = "suspended"
	CodeNotFound              = "not_found"
	CodeInvalidIdempotencyKey = "invalid_idempotency_key"
	CodeIdempotencyConflict   = "idempotency_conflict"
	CodeIdempotencyNoResult   = "idempotency_result_unavailable"
	CodeAccountExists         = "account_exists"
	CodeInviteExists          = "invite_exists"
	CodeInviteClosed          = "invite_closed"
	CodeQuotaExceeded         = "quota_exceeded"
	CodeSlugTaken             = "slug_taken"
	CodeAppArchived           = "app_archived"
	CodeAppNotArchived        = "app_not_archived"
	CodeDeploymentLimit       = "deployment_limit"
	CodeSecretsUnavailable    = "secrets_unavailable"
	CodeSecretStoreFailed     = "secret_store_failed"
	CodeInternal              = "internal"
	CodeUsageCollectFailed    = "usage_collect_failed"
)

type WaitlistListInput struct {
	Search string `json:"search,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}
type WaitlistID struct {
	ID string `json:"id"`
}

type WaitlistInvite struct {
	ID   string `json:"id"`
	Role Role   `json:"role,omitempty"`
}
