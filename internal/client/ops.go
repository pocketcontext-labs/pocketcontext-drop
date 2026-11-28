package client

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appconfig"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Drop's operations (POST /api/drop/op): accounts and invites for admins, and
// the generic `op` command. They run against a local project, a hosted server
// on this machine (through its private socket, as the owner) or, with --url, a
// forwarded server.

// opCommands are the commands that call operations.
var opCommands = map[string]bool{"invite": true, "user": true, "op": true, "app": true, "secret": true, "release": true, "node": true, "place": true, "usage": true}

// newOpsClient signs in to the project's server as its owner: through the
// private socket when a hosted server runs (hosted mode has no loopback TCP
// port), otherwise like every other local command.
func newOpsClient(p *appserver.Project) (*Client, error) {
	info, err := appserver.Health(p)
	if err != nil || !info.Hosted {
		return New(p)
	}
	httpClient, err := appserver.LocalHTTPClient(p, 30*time.Second)
	if err != nil {
		return nil, fail(1, "%v", err)
	}
	httpClient.CheckRedirect = noRedirect
	c := &Client{Base: p.URL(), DB: p.DB, http: httpClient}
	key, err := appserver.ReadKey(p)
	if err != nil {
		return nil, fail(1, "%v", err)
	}
	status, data, err := c.send("POST", "/api/bundle/token", nil, map[string]string{appconfig.Identity.LocalKeyHeader: key}, "", nil)
	if err != nil {
		return nil, err
	}
	token, _ := dig(data, "token").(string)
	if status != 200 || token == "" {
		return nil, fail(1, "owner sign-in on the hosted server failed (HTTP %d): %s", status, message(data))
	}
	c.token = token
	return c, nil
}

// Op calls one operation with its typed input and returns its result. An
// error answer becomes a failure (exit 4 for HTTP 409) that shows its code.
func (c *Client) Op(op string, input any, idempotencyKey string) (any, error) {
	req, err := dropapi.Build(op, input, idempotencyKey)
	if err != nil {
		return nil, fail(2, "%v", err)
	}
	return c.sendOp(req)
}

// RawOp calls an operation with an input given as JSON text.
func (c *Client) RawOp(op string, input []byte, idempotencyKey string) (any, error) {
	if _, ok := dropapi.Lookup(op); !ok {
		return nil, fail(2, "unknown operation %q", op)
	}
	if idempotencyKey != "" && !dropapi.IdempotencyKeyPattern.MatchString(idempotencyKey) {
		return nil, fail(2, "--idempotency-key is 1-128 letters, digits, '.', '_', ':' or '-'")
	}
	var object map[string]any
	if len(input) > 0 && json.Unmarshal(input, &object) != nil {
		return nil, fail(2, "the input must be one JSON object")
	}
	return c.sendOp(dropapi.Request{Op: op, Input: json.RawMessage(input), IdempotencyKey: idempotencyKey})
}

func (c *Client) sendOp(req dropapi.Request) (any, error) {
	status, data, err := c.send("POST", dropapi.Route, req, nil, "", nil)
	if err != nil {
		return nil, err
	}
	if status == 200 {
		return dig(data, "result"), nil
	}
	code, _ := dig(data, "code").(string)
	text := message(data)
	if field, ok := dig(data, "data", "field").(string); ok && field != "" {
		text += " (field " + field + ")"
	}
	exit := 1
	if status == 409 {
		exit = 4
	}
	return nil, fail(exit, "%s: HTTP %d %s: %s", req.Op, status, code, text)
}

// runOps runs invite, user, app, secret, release and op.
func runOps(c *Client, command string, a *args) (any, error) {
	sub := ""
	if len(a.pos) > 0 {
		sub = a.pos[0]
	}
	switch command {
	case "app":
		return runApp(c, sub, a)
	case "secret":
		return runSecret(c, sub, a)
	case "release":
		return runRelease(c, sub, a)
	case "node":
		return runNode(c, sub, a)
	case "place":
		return runPlace(c, a)
	case "usage":
		return runUsage(c, sub, a)
	}
	switch command + " " + sub {
	case "invite create":
		if err := a.need(2, "invite create EMAIL [--role user|developer|admin] [--quota N] [--expires-days N]"); err != nil {
			return nil, err
		}
		in := dropapi.InviteCreateInput{Email: a.pos[1], Role: dropapi.Role(a.flags["role"])}
		if a.has("quota") {
			q, err := a.intFlag("quota", 0)
			if err != nil {
				return nil, err
			}
			in.AppQuota = &q
		}
		days, err := a.intFlag("expires-days", 0)
		if err != nil {
			return nil, err
		}
		in.ExpiresDays = days
		return c.Op("invite.create", in, "")
	case "invite list":
		if err := a.need(1, "invite list [--state all|open] [--limit N]"); err != nil {
			return nil, err
		}
		limit, err := a.intFlag("limit", 0)
		if err != nil {
			return nil, err
		}
		return c.Op("invite.list", dropapi.InviteListInput{State: a.flags["state"], Limit: limit}, "")
	case "invite revoke":
		if err := a.need(2, "invite revoke INVITE_ID"); err != nil {
			return nil, err
		}
		return c.Op("invite.revoke", dropapi.InviteRevokeInput{Invite: a.pos[1]}, "")
	case "user list":
		if err := a.need(1, "user list [--limit N]"); err != nil {
			return nil, err
		}
		limit, err := a.intFlag("limit", 0)
		if err != nil {
			return nil, err
		}
		return c.Op("user.list", dropapi.UserListInput{Limit: limit}, "")
	case "user set":
		if err := a.need(2, "user set USER_ID|EMAIL [--role user|developer] [--quota N] [--suspend | --unsuspend]"); err != nil {
			return nil, err
		}
		in := dropapi.UserSetInput{User: a.pos[1]}
		if a.has("role") {
			role := dropapi.Role(a.flags["role"])
			in.Role = &role
		}
		if a.has("quota") {
			q, err := a.intFlag("quota", 0)
			if err != nil {
				return nil, err
			}
			in.AppQuota = &q
		}
		if a.bools["suspend"] && a.bools["unsuspend"] {
			return nil, fail(2, "choose --suspend or --unsuspend")
		}
		if a.bools["suspend"] || a.bools["unsuspend"] {
			suspended := a.bools["suspend"]
			in.Suspended = &suspended
		}
		if in.Role == nil && in.AppQuota == nil && in.Suspended == nil {
			return nil, fail(2, "usage: drop user set USER_ID|EMAIL [--role user|developer] [--quota N] [--suspend | --unsuspend]")
		}
		return c.Op("user.set", in, "")
	}
	if command == "op" {
		if len(a.pos) < 1 || len(a.pos) > 2 {
			return nil, fail(2, "usage: drop op NAME [INPUT_JSON|-] [--idempotency-key KEY]")
		}
		var input []byte
		if len(a.pos) == 2 {
			var err error
			if input, err = readArg(a.pos[1], "the input"); err != nil {
				return nil, err
			}
		}
		return c.RawOp(a.pos[0], input, a.flags["idempotency-key"])
	}
	usage := map[string]string{"invite": "invite create|list|revoke ...", "user": "user list|set ..."}[command]
	return nil, fail(2, "usage: drop %s", usage)
}

// appSettingsUsage lists the app create/set options.
const appSettingsUsage = "[--mode running|on-request|stopped] [--idle-timeout MS] [--vcpu N] [--memory MIB] [--disk MIB] [--port N] [--hook-port N] [--executable NAME] [--args-json JSON] [--health tcp|http] [--health-path PATH]"

// appSettings reads the setting options into the fields of an app.create or
// app.set input (both have the same optional fields).
func appSettings(a *args) (map[string]any, error) {
	out := map[string]any{}
	texts := map[string]string{"mode": "desired_state", "executable": "executable_name", "health": "health_kind", "health-path": "health_path"}
	for flag, field := range texts {
		if a.has(flag) {
			out[field] = a.flags[flag]
		}
	}
	ints := map[string]string{"idle-timeout": "idle_timeout_ms", "vcpu": "vcpu", "memory": "memory_mib", "disk": "disk_mib", "port": "http_port", "hook-port": "hook_port"}
	for flag, field := range ints {
		if a.has(flag) {
			n, err := a.intFlag(flag, 0)
			if err != nil {
				return nil, err
			}
			out[field] = n
		}
	}
	if a.has("args-json") {
		var list []string
		if err := json.Unmarshal([]byte(a.flags["args-json"]), &list); err != nil {
			return nil, fail(2, "--args-json must be a JSON array of strings, such as '[\"serve\",\"--port\",\"8080\"]'")
		}
		if list == nil {
			list = []string{}
		}
		out["args"] = list
	}
	return out, nil
}

// typedOp converts a map input into an operation's typed input, so unknown
// fields fail here, and calls it.
func typedOp[In any](c *Client, op string, fields map[string]any) (any, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var in In
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return nil, fail(2, "%s: %v", op, err)
	}
	return c.Op(op, in, "")
}

func runApp(c *Client, sub string, a *args) (any, error) {
	if a.bools["finish-enrollment"] && sub != "set" {
		return nil, fail(2, "--finish-enrollment is only for app set")
	}
	if a.has("kind") && sub != "create" {
		return nil, fail(2, "--kind is only for app create")
	}
	if a.has("sign-in-email") && sub != "set" {
		return nil, fail(2, "--sign-in-email is only for app set")
	}
	settingsOK := func() error {
		for _, flag := range []string{"all", "archived"} {
			if a.bools[flag] {
				return fail(2, "--%s is only for app list", flag)
			}
		}
		return nil
	}
	switch sub {
	case "list":
		if err := a.need(1, "app list [--all] [--archived] [--limit N]"); err != nil {
			return nil, err
		}
		limit, err := a.intFlag("limit", 0)
		if err != nil {
			return nil, err
		}
		return c.Op("app.list", dropapi.AppListInput{All: a.bools["all"], Archived: a.bools["archived"], Limit: limit}, "")
	case "show":
		if err := a.need(2, "app show APP"); err != nil {
			return nil, err
		}
		return c.Op("app.get", dropapi.AppGetInput{App: a.pos[1]}, "")
	case "create":
		usage := "app create NAME|- --slug SLUG [--kind ordinary|crew] " + appSettingsUsage
		if err := a.need(2, usage); err != nil {
			return nil, err
		}
		if !a.has("slug") || a.has("name") {
			return nil, fail(2, "usage: drop %s", usage)
		}
		if err := settingsOK(); err != nil {
			return nil, err
		}
		name, err := readArg(a.pos[1], "the name")
		if err != nil {
			return nil, err
		}
		fields, err := appSettings(a)
		if err != nil {
			return nil, err
		}
		if a.has("kind") {
			fields["kind"] = a.flags["kind"]
		}
		fields["name"], fields["slug"] = strings.TrimSpace(string(name)), a.flags["slug"]
		return typedOp[dropapi.AppCreateInput](c, "app.create", fields)
	case "set":
		usage := "app set APP [--name NAME] [--sign-in-email enabled|disabled] [--finish-enrollment] [--replica-sync-interval 10s] " + appSettingsUsage
		if err := a.need(2, usage); err != nil {
			return nil, err
		}
		if a.has("slug") {
			return nil, fail(2, "an app's slug cannot change")
		}
		if err := settingsOK(); err != nil {
			return nil, err
		}
		fields, err := appSettings(a)
		if err != nil {
			return nil, err
		}
		if a.has("name") {
			fields["name"] = a.flags["name"]
		}
		if a.bools["finish-enrollment"] {
			fields["finish_enrollment"] = true
		}
		if a.has("replica-sync-interval") {
			fields["replica_sync_interval"] = a.flags["replica-sync-interval"]
		}
		if a.has("sign-in-email") {
			switch a.flags["sign-in-email"] {
			case "enabled":
				fields["sign_in_email_enabled"] = true
			case "disabled":
				fields["sign_in_email_enabled"] = false
			default:
				return nil, fail(2, "--sign-in-email must be enabled or disabled")
			}
		}
		if len(fields) == 0 {
			return nil, fail(2, "usage: drop %s", usage)
		}
		fields["app"] = a.pos[1]
		return typedOp[dropapi.AppSetInput](c, "app.set", fields)
	case "env":
		usage := "app env APP KEY=VALUE|-KEY ..."
		if len(a.pos) < 3 {
			return nil, fail(2, "usage: drop %s", usage)
		}
		change := dropapi.EnvChange{Set: map[string]string{}}
		for _, item := range a.pos[2:] {
			if name, ok := strings.CutPrefix(item, "-"); ok && !strings.Contains(item, "=") {
				change.Unset = append(change.Unset, name)
				continue
			}
			name, value, ok := strings.Cut(item, "=")
			if !ok || name == "" {
				return nil, fail(2, "usage: drop %s", usage)
			}
			change.Set[name] = value
		}
		return c.Op("app.set", dropapi.AppSetInput{App: a.pos[1], Env: &change}, "")
	case "start", "stop", "archive", "restore":
		if err := a.need(2, "app "+sub+" APP"); err != nil {
			return nil, err
		}
		return c.Op("app."+sub, dropapi.AppRef{App: a.pos[1]}, "")
	case "status":
		if err := a.need(2, "app status APP"); err != nil {
			return nil, err
		}
		return c.Op("status", dropapi.StatusInput{App: a.pos[1]}, "")
	}
	return nil, fail(2, "usage: drop app list|show|status|create|set|env|start|stop|archive|restore ...")
}

// maxSecretInput bounds what `secret set` reads from standard input.
const maxSecretInput = dropapi.MaxSecretBytes + 2

func runSecret(c *Client, sub string, a *args) (any, error) {
	switch sub {
	case "list":
		if err := a.need(2, "secret list APP"); err != nil {
			return nil, err
		}
		return c.Op("secret.list", dropapi.SecretListInput{App: a.pos[1]}, "")
	case "set":
		// The value comes only from standard input: never an argument, which
		// would show in the process list and shell history.
		if err := a.need(3, "secret set APP NAME (the value on standard input)"); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxSecretInput+1))
		if err != nil {
			return nil, err
		}
		value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if value == "" || len(value) > dropapi.MaxSecretBytes {
			return nil, fail(2, "write the secret value (1-%d bytes) to standard input", dropapi.MaxSecretBytes)
		}
		return c.Op("secret.set", dropapi.SecretSetInput{App: a.pos[1], Name: a.pos[2], Value: value}, "")
	case "delete":
		if err := a.need(3, "secret delete APP NAME"); err != nil {
			return nil, err
		}
		return c.Op("secret.delete", dropapi.SecretDeleteInput{App: a.pos[1], Name: a.pos[2]}, "")
	}
	return nil, fail(2, "usage: drop secret list|set|delete ...")
}

// runNode runs `drop node` (admins): nodes are the hosts that run apps.
func runNode(c *Client, sub string, a *args) (any, error) {
	switch sub {
	case "list":
		if err := a.need(1, "node list [--limit N]"); err != nil {
			return nil, err
		}
		limit, err := a.intFlag("limit", 0)
		if err != nil {
			return nil, err
		}
		return c.Op("node.list", dropapi.NodeListInput{Limit: limit}, "")
	case "create", "add":
		usage := "node create NAME --host-id HOST_ID [--ec2-instance-id ID] [--private-ip IP]"
		if err := a.need(2, usage); err != nil {
			return nil, err
		}
		if !a.has("host-id") {
			return nil, fail(2, "usage: drop %s", usage)
		}
		return c.Op("node.create", dropapi.NodeCreateInput{Name: a.pos[1], HostID: a.flags["host-id"],
			EC2InstanceID: a.flags["ec2-instance-id"], PrivateIP: a.flags["private-ip"]}, "")
	case "rotate-secret":
		if err := a.need(2, "node rotate-secret NODE"); err != nil {
			return nil, err
		}
		return c.Op("node.rotate_secret", dropapi.NodeRotateSecretInput{Node: a.pos[1]}, "")
	case "set-state":
		if err := a.need(3, "node set-state NODE active|draining|disabled"); err != nil {
			return nil, err
		}
		return c.Op("node.set_state", dropapi.NodeSetStateInput{Node: a.pos[1], State: a.pos[2]}, "")
	}
	return nil, fail(2, "usage: drop node list|create|rotate-secret|set-state ...")
}

// runPlace runs `drop place` (admins): put an app on a node, or clear it.
func runPlace(c *Client, a *args) (any, error) {
	if a.bools["clear"] {
		if err := a.need(1, "place APP --clear"); err != nil {
			return nil, err
		}
		return c.Op("placement.clear", dropapi.PlacementClearInput{App: a.pos[0]}, "")
	}
	if err := a.need(2, "place APP NODE | place APP --clear"); err != nil {
		return nil, err
	}
	return c.Op("placement.set", dropapi.PlacementSetInput{App: a.pos[0], Node: a.pos[1]}, "")
}

// runUsage runs `drop usage`: an app's S3 usage, an admin's limit overrides
// and a one-off collector run.
func runUsage(c *Client, sub string, a *args) (any, error) {
	usage := "usage show APP | usage limits APP [--requests-a N] [--requests-b N] [--bytes-out BYTES] [--stored-bytes BYTES] [--reset] | usage collect"
	switch sub {
	case "show":
		if err := a.need(2, "usage show APP"); err != nil {
			return nil, err
		}
		return c.Op("usage.get", dropapi.UsageGetInput{App: a.pos[1]}, "")
	case "limits":
		if err := a.need(2, "usage limits APP [--requests-a N] [--requests-b N] [--bytes-out BYTES] [--stored-bytes BYTES] [--reset]"); err != nil {
			return nil, err
		}
		in := dropapi.UsageSetLimitsInput{App: a.pos[1], Reset: a.bools["reset"]}
		for flag, into := range map[string]**int64{"requests-a": &in.RequestsA, "requests-b": &in.RequestsB, "bytes-out": &in.BytesOut, "stored-bytes": &in.StoredBytes} {
			if !a.has(flag) {
				continue
			}
			n, err := strconv.ParseInt(a.flags[flag], 10, 64)
			if err != nil {
				return nil, fail(2, "--%s must be a whole number", flag)
			}
			*into = &n
		}
		return c.Op("usage.set_limits", in, "")
	case "collect":
		if err := a.need(1, "usage collect"); err != nil {
			return nil, err
		}
		return c.Op("usage.collect", dropapi.UsageCollectInput{}, "")
	}
	return nil, fail(2, "usage: drop %s", usage)
}
