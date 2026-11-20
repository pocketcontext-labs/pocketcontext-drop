package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// TestTenantReader exercises the built reader against the hosted public router.
func TestTenantReader(t *testing.T) {
	if os.Getenv("DROP_TEST_BROWSER") != "1" {
		t.Skip("set DROP_TEST_BROWSER=1 with the built reader and Playwright installed")
	}
	root := droptest.Root(t)
	h := droptest.NewHosted(t, droptest.Options{ReaderDir: filepath.Join(root, "ui", "dist")})
	type tenant struct {
		Token string `json:"token"`
		ID    string `json:"id"`
		App   string `json:"app"`
		Name  string `json:"name"`
	}
	var tenants []tenant
	for _, name := range []string{"first-tenant", "second-tenant"} {
		user, token := h.User(name+"@example.test", "user")
		created := h.MustOp(token, "app.create", map[string]any{"slug": name, "name": name})
		app := created["app"].(map[string]any)
		tenants = append(tenants, tenant{token, user.Id, app["id"].(string), name})
	}

	// Translate the loopback test origin to the synthetic hosted origin.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://"+r.Host {
			r.Header.Set("Origin", "https://"+droptest.Domain)
		}
		r.Host = droptest.Domain
		h.Server.Public().ServeHTTP(w, r)
	}))
	defer server.Close()
	input, err := json.Marshal(tenants)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", filepath.Join(root, "tests", "tenant_reader.mjs"), server.URL)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
