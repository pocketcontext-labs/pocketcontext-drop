package client

import (
	"encoding/json"
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
var opCommands = map[string]bool{"invite": true, "user": true, "op": true}

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
		return nil, fail(2, "unknown operation %q; known: %s", op, strings.Join(dropapi.Names(dropapi.RoleAdmin), ", "))
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

// runOps runs invite, user and op.
func runOps(c *Client, command string, a *args) (any, error) {
	sub := ""
	if len(a.pos) > 0 {
		sub = a.pos[0]
	}
	switch command + " " + sub {
	case "invite create":
		if err := a.need(2, "invite create EMAIL [--role user|admin] [--quota N] [--expires-days N]"); err != nil {
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
		if err := a.need(2, "user set USER_ID|EMAIL [--role user|admin] [--quota N] [--suspend | --unsuspend]"); err != nil {
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
			return nil, fail(2, "usage: drop user set USER_ID|EMAIL [--role user|admin] [--quota N] [--suspend | --unsuspend]")
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
