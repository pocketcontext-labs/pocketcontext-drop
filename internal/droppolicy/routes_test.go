package droppolicy

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver/routepolicytest"
)

// routeCase is one row of the golden public route table.
type routeCase struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Allow  bool   `json:"allow"`
}

const publicRouteTable = "testdata/public_routes.json"

var updateRoutes = flag.Bool("update-routes", false, "rewrite testdata/public_routes.json from PublicRoute; review the diff")

var routeMethods = []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"}

// routePaths are the paths every method is checked against: the reader, the
// routes core adds, PocketBase's own API, the local administration routes and
// every Drop collection planned so far, so a route that becomes public shows
// up as a reviewed change of the table.
var routePaths = []string{
	"/", "/index.html", "/assets/index-AbCd1234.js", "/assets/index-AbCd1234.css", "/assets/logo-AbCd1234.svg",
	"/assets/..%2fsecret", "/assets/", "/assets/sub/index.js", "/favicon.ico", "/robots.txt",
	"/bundle/login", "/bundle/login.js", "/_", "/_/", "/up", "/mcp",
	"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server",
	"/oauth/authorize", "/oauth/token", "/oauth/register", "/oauth/connections",
	"/api/health", "/api/settings", "/api/logs", "/api/backups", "/api/batch", "/api/crons", "/api/collections",
	"/api/realtime", "/api/files/token", "/api/files/apps/aaaaaaaaaaaaaaa/file.bin",
	"/api/context/query", "/api/context/search", "/api/context/schema", "/api/context/maintenance", "/api/context/traces",
	"/api/bundle/auth-mode", "/api/bundle/version", "/api/bundle/instance", "/api/bundle/token", "/api/bundle/account",
	"/api/bundle/login-code", "/api/bundle/reader-code", "/api/bundle/redeem", "/api/bundle/flush", "/api/bundle/stop",
	"/api/bundle/name", "/api/bundle/enable-tcp",
	"/api/collections/users/auth-methods", "/api/collections/users/auth-with-password", "/api/collections/users/auth-refresh",
	"/api/collections/users/request-password-reset", "/api/collections/users/request-verification",
	"/api/collections/users/request-otp", "/api/collections/users/auth-with-otp", "/api/collections/users/auth-with-otp-code",
	"/api/collections/users/impersonate/aaaaaaaaaaaaaaa",
	"/api/collections/_superusers/records", "/api/collections/_superusers/auth-with-password",
	"/api/collections/nodes/auth-with-password", "/api/collections/nodes/auth-refresh",
	"/api/drop/waitlist/request", "/api/drop/waitlist/verify", "/api/drop/waitlist/leave",
	"/api/drop/me", "/api/drop/op", "/api/drop/agent/desired", "/api/drop/agent/report",
	"/api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz012345",
	"/api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz01234", "/api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz0123456",
	"/api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz01234-", "/api/drop/upload/", "/api/drop/upload",
	"/api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz012345/extra",
}

// routeCollections get record list, view and malformed-path rows.
var routeCollections = []string{"users", "apps", "invites", "releases", "app_secret_names", "upload_links", "nodes", "placements", "node_reports", "events", "_superusers", "_otps"}

func generatedRouteTable() []routeCase {
	paths := append([]string{}, routePaths...)
	for _, c := range routeCollections {
		base := "/api/collections/" + c + "/records"
		paths = append(paths, base, base+"/", base+"/aaaaaaaaaaaaaaa", base+"/AAAA", base+"/aaaaaaaaaaaaaaa/extra")
	}
	table := []routeCase{}
	for _, p := range paths {
		for _, m := range routeMethods {
			table = append(table, routeCase{m, p, PublicRoute(m, p)})
		}
	}
	return table
}

func TestPublicRouteTable(t *testing.T) {
	generated := generatedRouteTable()
	if *updateRoutes {
		data, err := json.MarshalIndent(generated, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(publicRouteTable, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(publicRouteTable)
	if err != nil {
		t.Fatal(err)
	}
	var table []routeCase
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&table); err != nil {
		t.Fatal(err)
	}
	// Every generated row is in the file, so a new path is a reviewed change.
	if len(table) != len(generated) {
		t.Fatalf("table has %d rows, PublicRoute gives %d; run go test ./internal/droppolicy -run TestPublicRouteTable -update-routes and review the diff", len(table), len(generated))
	}
	allowed := 0
	for i, c := range table {
		if c.Allow {
			allowed++
		}
		if c.Method != generated[i].Method || c.Path != generated[i].Path {
			t.Fatalf("row %d is %s %s, want %s %s", i, c.Method, c.Path, generated[i].Method, generated[i].Path)
		}
		if got := PublicRoute(c.Method, c.Path); got != c.Allow {
			t.Errorf("%s %s: allowed=%v, want %v", c.Method, c.Path, got, c.Allow)
		}
	}
	if allowed == 0 {
		t.Fatal("table allows nothing")
	}
}

func TestPublicRouteCommonDenied(t *testing.T) {
	routepolicytest.Check(t, PublicRoute, routepolicytest.CommonDenied())
}

// TestPublicRouteDeniesLocalReads keeps the SQL and search reads, which read
// whole tables, and every record write off the public origin.
func TestPublicRouteDeniesLocalReads(t *testing.T) {
	for _, m := range routeMethods {
		for _, p := range []string{"/api/context/query", "/api/context/search", "/api/context/schema", "/api/batch", "/api/files/token", "/api/bundle/token", "/api/collections/users/records/aaaaaaaaaaaaaaa"} {
			if PublicRoute(m, p) {
				t.Errorf("%s %s is public", m, p)
			}
		}
		if m != "GET" && m != "HEAD" && (PublicRoute(m, "/api/collections/apps/records") || PublicRoute(m, "/api/collections/apps/records/aaaaaaaaaaaaaaa")) {
			t.Errorf("record %s is public", m)
		}
	}
}

// frozenAllowed is every allowed row of testdata/public_routes.json, written
// by hand. The route table is frozen (plan M10): -update-routes rewrites the
// golden file, but it never touches this list, so making a route public takes
// two deliberate edits in one reviewed diff. Never add a row to make a test
// pass; a new public route needs the owner's review (AGENTS.md).
var frozenAllowed = []string{
	"GET /", "HEAD /",
	"GET /assets/index-AbCd1234.js", "HEAD /assets/index-AbCd1234.js",
	"GET /assets/index-AbCd1234.css", "HEAD /assets/index-AbCd1234.css",
	"GET /assets/logo-AbCd1234.svg", "HEAD /assets/logo-AbCd1234.svg",
	"GET /api/realtime", "HEAD /api/realtime", "POST /api/realtime",
	"GET /api/bundle/auth-mode", "HEAD /api/bundle/auth-mode",
	"GET /api/bundle/version", "HEAD /api/bundle/version",
	"POST /api/collections/users/auth-refresh",
	// The node agent (node tokens only; Drop's per-address limiter).
	"POST /api/collections/nodes/auth-with-password", "POST /api/collections/nodes/auth-refresh",
	"POST /api/drop/waitlist/request", "POST /api/drop/waitlist/verify", "POST /api/drop/waitlist/leave",
	"GET /api/drop/me", "HEAD /api/drop/me",
	"POST /api/drop/op",
	"GET /api/drop/agent/desired", "HEAD /api/drop/agent/desired",
	"POST /api/drop/agent/report",
	// The one anonymous write: a 32-character one-time upload token.
	"PUT /api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz012345",
	// Record reads the collection rules decide.
	"GET /api/collections/apps/records", "HEAD /api/collections/apps/records",
	"GET /api/collections/apps/records/aaaaaaaaaaaaaaa", "HEAD /api/collections/apps/records/aaaaaaaaaaaaaaa",
	"GET /api/collections/invites/records", "HEAD /api/collections/invites/records",
	"GET /api/collections/invites/records/aaaaaaaaaaaaaaa", "HEAD /api/collections/invites/records/aaaaaaaaaaaaaaa",
	"GET /api/collections/events/records", "HEAD /api/collections/events/records",
	"GET /api/collections/events/records/aaaaaaaaaaaaaaa", "HEAD /api/collections/events/records/aaaaaaaaaaaaaaa",
}

// TestPublicRouteTableFrozen: the allowed rows of the golden table and of
// PublicRoute are exactly frozenAllowed.
func TestPublicRouteTableFrozen(t *testing.T) {
	data, err := os.ReadFile(publicRouteTable)
	if err != nil {
		t.Fatal(err)
	}
	var table []routeCase
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	var golden, generated []string
	for _, c := range table {
		if c.Allow {
			golden = append(golden, c.Method+" "+c.Path)
		}
	}
	for _, c := range generatedRouteTable() {
		if c.Allow {
			generated = append(generated, c.Method+" "+c.Path)
		}
	}
	for name, got := range map[string][]string{publicRouteTable: golden, "PublicRoute": generated} {
		if strings.Join(got, "\n") != strings.Join(frozenAllowed, "\n") {
			t.Errorf("%s allows a different set of routes than the frozen list:\n got  %q\n want %q\nthe public route table is frozen: a change needs the owner's review and a hand edit of frozenAllowed", name, got, frozenAllowed)
		}
	}
}
