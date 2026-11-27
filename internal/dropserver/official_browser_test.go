package dropserver_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// Synthetic manifests and node reports isolate the hosted browser flow from KVM.
func TestOfficialBrowser(t *testing.T) {
	if os.Getenv("DROP_TEST_BROWSER") != "1" {
		t.Skip("set DROP_TEST_BROWSER=1 with Playwright and the built reader")
	}
	root := droptest.Root(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "probe.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := droptest.NewHosted(t, droptest.Options{ReaderDir: filepath.Join(root, "ui", "dist"), ProbeSocket: sock})
	owner := h.Owner()
	admin := h.Token(owner)
	alice, customer := h.User("browser@example.test", "developer")
	legacy := appOf(t, h.MustOp(customer, "app.create", map[string]any{"name": "Retained workspace", "slug": "retained-workspace"}))["id"].(string)
	h.MustOp(admin, "user.set", map[string]any{"user": alice.Id, "role": "user"})
	dropserver.SetServices(h.App, dropserver.Services{Releases: h.Releases, Secrets: h.Secrets, ProbeSocket: sock, BetaNodes: []string{"official-node"}, AppDomain: droptest.AppDomain, CrewReplicaBucket: "crew-replica-fixture", AWSRegion: "eu-north-1"})
	node := h.MustOp(admin, "node.create", map[string]any{"name": "official-node", "host_id": hostA})
	_, nt := h.NodeLogin("official-node", node["password"].(string))
	ready := reportOf(hostA)
	ready["nibrunner"].(map[string]any)["versions"].(map[string]any)["cpuQuarterQuota"] = true
	if code, b := report(t, h, nt, ready); code != 200 {
		t.Fatal(b)
	}
	_, err = h.App.DB().NewQuery(`WITH RECURSIVE n(v) AS (VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<5000) INSERT INTO drop_waitlist(id,email,terms,requested,verified_at) SELECT printf('%015d',v),printf('waiting-%04d@example.test',v),'2026-11-26-waitlist',0,'2026-01-01T00:00:00Z' FROM n`).Execute()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	known := map[string]string{}
	for i, p := range []string{"crew", "atlas"} {
		body := droptest.ELF(4096, byte(i+1))
		digest := sha(body)
		known[digest] = p
		path := filepath.Join(dir, p)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		files[p] = path
		evidence := sha([]byte("browser fixture"))
		receipt, _ := json.Marshal(map[string]any{"sha256": digest, "baseline_sha256": "", "evidence_url": "https://evidence.example.test/browser", "evidence_sha256": evidence, "before_records_sha256": evidence, "after_records_sha256": evidence, "before_attachments_sha256": evidence, "after_attachments_sha256": evidence, "records": 1, "attachments": 1})
		if err := os.WriteFile(path+".json", receipt, 0600); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer c.Close()
				r := bufio.NewReader(c)
				if _, err := r.ReadString('\n'); err != nil {
					return
				}
				body := make([]byte, 4096)
				if _, err := io.ReadFull(r, body); err != nil {
					return
				}
				p, ok := known[sha(body)]
				if !ok {
					return
				}
				m := strings.NewReplacer(`"app":"crew"`, `"app":"`+p+`"`, `"version":"0.45.4-drop.4"`, `"version":"1.0.0"`, `"s3_replica",`, ``, `"minimum":`, `"optional":["s3_replica"],"minimum":`, `"memory_mib":512`, `"memory_mib":256`, `"cpu_quarters":4`, `"cpu_quarters":1`).Replace(validCrewManifest)
				_ = json.NewEncoder(c).Encode(map[string]any{"outcome": "success", "manifest": json.RawMessage(m)})
			}()
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://"+r.Host {
			r.Header.Set("Origin", "https://"+droptest.Domain)
		}
		r.Host = droptest.Domain
		h.Server.Public().ServeHTTP(w, r)
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"files": files, "admin": admin, "adminId": owner.Id, "customer": customer, "customerId": alice.Id, "legacy": legacy})
	cmd := exec.Command("node", filepath.Join(root, "tests", "official_browser.mjs"), server.URL)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
