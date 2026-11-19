package droppolicy

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
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
	"/api/drop/me", "/api/drop/op", "/api/drop/agent/desired", "/api/drop/agent/report",
	"/api/drop/upload/AbCdEfGhIjKlMnOpQrStUvWxYz012345",
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
