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

// Role is who may call an operation. Admins may call every operation.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
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
}

var opName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// specs is the registry, in the order the documentation and MCP list them.
// Later milestones add their operations here (and their handlers in
// internal/dropserver); a test checks that both sides agree.
var specs = []Spec{
	{Name: "me", Role: RoleUser, Read: true, Input: &MeInput{},
		Summary: "The signed-in account: id, email, role, app quota and whether it is the owner."},
	{Name: "user.list", Role: RoleAdmin, Read: true, Input: &UserListInput{},
		Summary: "Every account, newest first, with role, quota, suspension and whether it has signed in."},
	{Name: "user.set", Role: RoleAdmin, Input: &UserSetInput{},
		Summary: "Change an account's role, app quota or suspension. Nobody changes their own role or suspension, or the owner's."},
	{Name: "invite.create", Role: RoleAdmin, Input: &InviteCreateInput{},
		Summary: "Invite an email address (role user or admin, app quota, 1-30 days, default 7). Returns the invite and a sign-in URL to share; no email is sent."},
	{Name: "invite.list", Role: RoleAdmin, Read: true, Input: &InviteListInput{},
		Summary: "Invites, newest first, with their state (open, used, revoked or expired)."},
	{Name: "invite.revoke", Role: RoleAdmin, Input: &InviteRevokeInput{},
		Summary: "Revoke an open invite; the address can no longer get an account."},
	{Name: "app.list", Role: RoleUser, Read: true, Input: &AppListInput{},
		Summary: "Your apps, newest first, with your app quota; archived:true lists archived apps; all:true (admins) lists everyone's."},
	{Name: "app.get", Role: RoleUser, Read: true, Input: &AppGetInput{},
		Summary: "One app by id or slug, with the names of its secrets (never their values)."},
	{Name: "app.create", Role: RoleUser, Input: &AppCreateInput{},
		Summary: "Create an app: name, unique slug (its hostname), and optional settings (state, idle timeout, vCPU, memory, disk, port, executable, args, env, health check). Counts against your app quota."},
	{Name: "app.set", Role: RoleUser, Input: &AppSetInput{},
		Summary: "Change an app's settings (env as {set, unset}); a change to the instance configuration starts a new deployment. The slug cannot change; the disk cannot change once the app is placed."},
	{Name: "app.start", Role: RoleUser, Input: &AppStartInput{},
		Summary: "Start a stopped app in the state it had before it stopped (running or on-request)."},
	{Name: "app.stop", Role: RoleUser, Input: &AppStopInput{},
		Summary: "Stop an app; its data stays."},
	{Name: "app.archive", Role: RoleUser, Input: &AppArchiveInput{},
		Summary: "Archive an app: it stops running and no longer counts against your quota. Its data, secrets and slug stay; app.restore brings it back."},
	{Name: "app.restore", Role: RoleUser, Input: &AppRestoreInput{},
		Summary: "Restore an archived app (it counts against your quota again)."},
	{Name: "secret.list", Role: RoleUser, Read: true, Input: &SecretListInput{},
		Summary: "An app's secrets: name, version, length, when and how each was set. Values are never returned."},
	{Name: "secret.set", Role: RoleUser, Input: &SecretSetInput{}, SecretInput: true,
		Summary: "Set an app secret (an environment variable kept in AWS SSM, never stored, logged or returned) and start a new deployment. Through MCP the value passes through the assistant's conversation: prefer the reader for real secrets."},
	{Name: "secret.delete", Role: RoleUser, Input: &SecretDeleteInput{},
		Summary: "Delete an app secret (every version) and start a new deployment."},
	{Name: "release.list", Role: RoleUser, Read: true, Input: &ReleaseListInput{},
		Summary: "An app's releases (uploaded executables), newest first: id, sha256, size, note, and which one is current."},
	{Name: "release.upload_link", Role: RoleUser, Input: &ReleaseUploadLinkInput{}, OnceResult: true,
		Summary: "A one-time upload link for a new release of an app: PUT a static linux/amd64 ELF executable (at most 90 MiB) to the returned URL within 15 minutes, for example with curl -fT FILE URL. activate:true deploys it once uploaded. The URL is shown once."},
	{Name: "release.deploy", Role: RoleUser, Input: &ReleaseDeployInput{},
		Summary: "Make one of an app's releases (by id) its current release and start a new deployment."},
}

func init() {
	seen := map[string]bool{}
	for _, s := range specs {
		if !opName.MatchString(s.Name) || seen[s.Name] || (s.Role != RoleUser && s.Role != RoleAdmin) || s.Input == nil || s.Summary == "" || (s.Read && s.SecretInput) {
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

// Names returns the operation names a role may call, sorted.
func Names(role Role) []string {
	var names []string
	for _, s := range specs {
		if role == RoleAdmin || s.Role == RoleUser {
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
)
