package dropserver

import (
	"net/http"

	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// The agent routes (plan 4.2; contract in internal/dropapi/agent.go, manual
// docs/agent-api.md). Only a node's token reaches them: an anonymous request
// gets 401, a users account's token 403 forbidden, a disabled node's 403
// node_disabled. A node's token reaches nothing else: the operation route
// refuses it (403), and every collection rule admits it only to its own
// nodes record and its own placements.
//
//   - GET  /api/drop/agent/desired: the node's drop.desired/v1 document.
//   - POST /api/drop/agent/report: the agent's drop.report/v1 (reports.go).
//
// The node sign-in routes (POST /api/collections/nodes/auth-with-password and
// auth-refresh) are PocketBase's own; Drop caps their bodies at 16 KiB and
// limits each client address, as core does for account sign-in.

// nodeAuthBodyLimit caps a node sign-in body.
const nodeAuthBodyLimit = 16 << 10

func registerAgentRoutes(e *core.ServeEvent) {
	state := agentStateOf(e.App)
	e.Router.BindFunc(func(re *core.RequestEvent) error {
		p := re.Request.URL.Path
		if re.Request.Method == http.MethodPost && (p == dropapi.NodeAuthRoute || p == dropapi.NodeRefreshRoute) {
			if !state.signIn.allow(re.Request.RemoteAddr, state.clock()) {
				return rateLimited(re, "Too many node sign-ins from this address. Try again shortly.")
			}
			// PocketBase's own limit reader keeps the body rereadable.
			return apis.BodyLimit(nodeAuthBodyLimit).Func(re)
		}
		return re.Next()
	})
	e.Router.GET(dropapi.AgentDesiredRoute, func(re *core.RequestEvent) error {
		return serveDesired(re, state)
	})
	e.Router.POST(dropapi.AgentReportRoute, func(re *core.RequestEvent) error {
		return serveReport(re, state)
	}).Bind(apis.BodyLimit(dropapi.MaxReportBytes))
}

// agentNode checks that the request comes from an enabled node and spends
// one request of its budget; it returns the node's current record.
func agentNode(re *core.RequestEvent, state *agentState) (*core.Record, error) {
	re.Response.Header().Set("Cache-Control", "no-store")
	if re.Auth == nil {
		return nil, &dropapi.Error{Status: http.StatusUnauthorized, Code: dropapi.CodeUnauthenticated, Message: "Sign in as the node first."}
	}
	if re.Auth.Collection().Name != dropapi.NodeCollection {
		return nil, Forbidden("Only nodes use the agent routes.")
	}
	node, err := re.App.FindRecordById(dropapi.NodeCollection, re.Auth.Id)
	if err != nil {
		return nil, &dropapi.Error{Status: http.StatusUnauthorized, Code: dropapi.CodeUnauthenticated, Message: "Sign in as the node first."}
	}
	if node.GetString("state") == dropapi.NodeDisabled {
		return nil, &dropapi.Error{Status: http.StatusForbidden, Code: dropapi.CodeNodeDisabled, Message: "This node is disabled."}
	}
	if !state.allowRoute(node.Id) {
		re.Response.Header().Set("Retry-After", "5")
		return nil, &dropapi.Error{Status: http.StatusTooManyRequests, Code: dropapi.CodeRateLimited, Message: "Too many agent requests. Slow down."}
	}
	return node, nil
}

// serveDesired answers the node's desired document. When its stored content
// hash is stale (a change no operation saw, such as a new server version),
// it bumps desired_seq first, so one desiredSeq always means one content.
func serveDesired(re *core.RequestEvent, state *agentState) error {
	node, err := agentNode(re, state)
	if err != nil {
		return writeError(re, err)
	}
	d, err := buildDesired(re.App, node)
	if err == nil && d.ContentHash != node.GetString("desired_hash") {
		err = re.App.RunInTransaction(func(tx core.App) error {
			fresh, err := tx.FindRecordById(dropapi.NodeCollection, node.Id)
			if err != nil {
				return err
			}
			_, err = syncNode(tx, fresh)
			return err
		})
		if err == nil {
			if node, err = re.App.FindRecordById(dropapi.NodeCollection, node.Id); err == nil {
				d, err = buildDesired(re.App, node)
			}
		}
	}
	if err != nil {
		re.App.Logger().Error("Drop desired document failed", "node", node.Id, "error", err.Error())
		return writeError(re, &dropapi.Error{Status: http.StatusServiceUnavailable, Code: dropapi.CodeDesiredFailed,
			Message: "The desired document is not available. Keep what runs and retry."})
	}
	return re.JSON(http.StatusOK, d)
}
