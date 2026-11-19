package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// TestMCPListsNoToolsYet: the hosted endpoint authenticates through OAuth with
// a signed-in account, answers the protocol and lists no tools until Drop's
// own tools arrive.
func TestMCPListsNoToolsYet(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{MCPHandler: newMCPHandler})
	bearer := h.OAuthToken(h.SignIn(droptest.OwnerEmail).Token)
	w := h.MCP(bearer, "tools/list", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list: %d %s", w.Code, w.Body)
	}
	var out struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Error != nil || len(out.Result.Tools) != 0 {
		t.Fatalf("tools/list answer %s: %v", w.Body, err)
	}
	w = h.MCP(bearer, "tools/call", map[string]any{"name": "query", "arguments": map[string]any{"sql": "SELECT 1"}})
	if w.Code == http.StatusOK && !strings.Contains(w.Body.String(), "error") {
		t.Fatalf("a removed tool answered: %s", w.Body)
	}
}

// TestMCPRejectsAnonymous: no OAuth token, no tools, and the reader's own
// session token is not a substitute.
func TestMCPRejectsAnonymous(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{MCPHandler: newMCPHandler})
	session := h.SignIn(droptest.OwnerEmail).Token
	for name, authorization := range map[string]string{"none": "", "garbage": "Bearer not-a-token", "session token": "Bearer " + session} {
		w := h.MCP(authorization, "tools/list", map[string]any{})
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
			t.Errorf("%s: %d %q", name, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	if status := h.Request("POST", "/oauth/authorize?client_id=x", "", `{"approve":true}`).Code; status < 400 {
		t.Errorf("anonymous approval = %d", status)
	}
}

// The adapter refuses a request that did not come through the public handler.
func TestMCPRequiresPublicBoundary(t *testing.T) {
	handler := newMCPHandler(http.NotFoundHandler())
	request := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", response.Code)
	}
}

// Until Drop's tools exist, the transport reaches no API route at all.
func TestMCPAPIRouteLabels(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/context/schema"}, {"POST", "/api/context/query"}, {"POST", "/api/collections/apps/records"},
		{"GET", "/api/collections/apps/records"}, {"GET", "/api/settings"}, {"POST", "/api/batch"},
	} {
		if got := mcpAPIRoute(c.method, c.path); got != "unknown" {
			t.Errorf("%s %s = %s", c.method, c.path, got)
		}
	}
}

var serveValues = []string{"ui-dev", "port", "domain", "hosted-http-address", "hosted-proxy-header", "hosted-proxy-networks"}
var serveBools = []string{"take-over", "tcp-only", "headless", "mcp", "hosted", "hosted-init", "hosted-restore"}

func TestHostedServeOptions(t *testing.T) {
	hosted := []string{"--hosted", "--headless", "--mcp", "--domain", "drop.example.test"}
	with := func(extra ...string) []string { return append(append([]string{}, hosted...), extra...) }
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"--mcp"}, "--mcp requires --hosted"},
		{[]string{"--headless"}, "--headless requires --hosted"},
		{[]string{"--domain", "drop.example.test"}, "--domain requires --hosted"},
		{[]string{"--hosted-init"}, "--hosted-init requires --hosted"},
		{[]string{"--hosted-http-address", "127.0.0.1:8100"}, "requires --hosted"},
		{[]string{"--hosted-proxy-header", "CF-Connecting-IP"}, "requires --hosted"},
		{[]string{"--port", "9443"}, "unknown option"},
		{[]string{"--hosted", "--headless", "--domain", "drop.example.test"}, "--hosted requires"},
		{[]string{"--hosted", "--mcp", "--domain", "drop.example.test"}, "--hosted requires"},
		{[]string{"--hosted", "--headless", "--mcp"}, "--hosted requires"},
		{with("--tcp-only"), "cannot be combined"},
		{with("--ui-dev", "http://127.0.0.1:5173"), "cannot be combined"},
		{with("--hosted-init", "--hosted-restore"), "excludes"},
		{[]string{"--hosted", "--headless", "--mcp", "--domain", "https://drop.example.test"}, "domain"},
		{with("--hosted-http-address", "localhost:8100"), "literal IP"},
		{with("--hosted-http-address", "127.0.0.1:0"), "literal IP"},
		{with("--hosted-proxy-header", "CF-Connecting-IP"), "both"},
		{with("--hosted-proxy-networks", "127.0.0.0/8"), "both"},
		{with("--hosted-proxy-header", "X-Real-IP", "--hosted-proxy-networks", "127.0.0.0/8"), "header must be"},
		{with("--hosted-proxy-header", "CF-Connecting-IP", "--hosted-proxy-networks", "0.0.0.0/0"), "every peer"},
		{with("--hosted-proxy-header", "CF-Connecting-IP", "--hosted-proxy-networks", "127.0.0.1/8"), "host bits"},
	} {
		a, err := parseArgs(c.argv, serveValues, serveBools)
		if err != nil {
			t.Fatal(err)
		}
		_, err = hostedServeOptions(a)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.argv, err, c.want)
		}
	}
	a, _ := parseArgs(nil, serveValues, serveBools)
	if opts, err := hostedServeOptions(a); err != nil || opts != nil {
		t.Errorf("plain serve: %v %v", opts, err)
	}
	a, _ = parseArgs(with("--hosted-init", "--hosted-http-address", "127.0.0.1:8100", "--hosted-proxy-header", "CF-Connecting-IP", "--hosted-proxy-networks", "127.0.0.0/8, 10.0.0.0/8"), serveValues, serveBools)
	opts, err := hostedServeOptions(a)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Hosted || !opts.HostedInit || !opts.MCP || opts.MCPHandler == nil || !opts.Headless || opts.HTTPSDomain != "drop.example.test" ||
		opts.HostedHTTPAddress != "127.0.0.1:8100" || opts.HostedProxy == nil || opts.HostedProxy.Header != "CF-Connecting-IP" || len(opts.HostedProxy.Networks) != 2 {
		t.Fatalf("hosted options: %+v", opts)
	}
}
