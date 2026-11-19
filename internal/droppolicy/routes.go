package droppolicy

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
)

// readableCollections are the collections whose record reads are public
// routes; their list and view rules decide who sees which record (invites:
// admins only; events: admins, and each account those of its own apps).
// Record writes are never public: Drop's writes go through its own
// operations. users records stay off (core's common denied list); admins
// list accounts with the user.list operation.
var readableCollections = map[string]bool{"apps": true, "invites": true, "events": true}

var recordID = regexp.MustCompile(`^[a-z0-9]{15}$`)

// PublicRoute is the public route allowlist of the hosted origin. It is
// deliberately default-deny: a route registered later on the application's
// router does not become public merely by existing. Core adds the sign-in
// email routes, /up and, with --mcp, /oauth/*, /mcp and /.well-known/*
// itself. testdata/public_routes.json is its golden table.
func PublicRoute(method, p string) bool {
	read := method == http.MethodGet || method == http.MethodHead
	if read {
		switch p {
		case "/", "/api/bundle/auth-mode", "/api/bundle/version", "/api/realtime", "/api/drop/me":
			return true
		}
		if strings.HasPrefix(p, "/assets/") && appserver.ReaderAssetName(strings.TrimPrefix(p, "/assets/")) {
			return true
		}
		if collection, id, ok := recordPath(p); ok && readableCollections[collection] && (id == "" || recordID.MatchString(id)) {
			return true
		}
	}
	if method == http.MethodPost {
		switch p {
		// /api/drop/op is every write: the operation framework checks the
		// account, its role and the input (internal/dropserver/ops.go).
		case "/api/realtime", "/api/collections/users/auth-refresh", "/api/drop/op":
			return true
		}
	}
	return false
}

// recordPath splits /api/collections/{collection}/records[/{id}].
func recordPath(p string) (collection, id string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 4 || len(parts) > 5 || parts[0] != "api" || parts[1] != "collections" || parts[3] != "records" || parts[2] == "" {
		return "", "", false
	}
	if len(parts) == 5 {
		if parts[4] == "" {
			return "", "", false
		}
		id = parts[4]
	}
	return parts[2], id, true
}
