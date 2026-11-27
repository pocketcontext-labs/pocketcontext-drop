package dropserver_test

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestPrivateBuildCreateFromInspection(t *testing.T) {
	t.Run("executable", func(t *testing.T) { testPrivateBuild(t, false) })
	t.Run("tar.gz", func(t *testing.T) { testPrivateBuild(t, true) })
}

func tarGzipBuild(t *testing.T, files ...[]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for index, file := range files {
		name := fmt.Sprintf("release/member%d", index)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(file))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testPrivateBuild(t *testing.T, archive bool) {
	socket := filepath.Join(t.TempDir(), "probe")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := droptest.NewHosted(t, droptest.Options{ProbeSocket: socket, BetaNodes: []string{"test-node"}})
	_, dev := h.User("builder@example.test", "developer")
	_, other := h.User("second@example.test", "developer")
	_, user := h.User("reader@example.test", "user")
	body := droptest.ELF(4096, 5)
	digest := sha(body)
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, err := r.ReadString('\n')
		buf := make([]byte, len(body))
		if err != nil || line != fmt.Sprintf("%s %d\n", digest, len(body)) {
			done <- fmt.Errorf("bad probe header %q", line)
			return
		}
		if _, err := io.ReadFull(r, buf); err != nil || !bytes.Equal(buf, body) {
			done <- fmt.Errorf("bad probe body")
			return
		}
		_, err = io.WriteString(c, `{"outcome":"success","manifest":{"schema":"drop.manifest/v1","app":"field-notes","version":"1.2.3","http_port":8040,"hook_port":0,"health":"tcp","hooks":[],"requires":["verified_owner_email"],"minimum":{"cpu_quarters":2,"memory_mib":256,"disk_mib":500}}}`)
		done <- err
	}()
	input := map[string]any{"sha256": digest, "name": "My Field Notes", "multiplier": 2}
	for _, token := range []string{user, h.Token(h.Owner())} {
		wantError(t, h.Op(token, "developer.build_upload", nil), 403, dropapi.CodeForbidden)
		wantError(t, h.Op(token, "developer.build_create", input), 403, dropapi.CodeForbidden)
		wantError(t, h.Op(token, "developer.build_import", map[string]any{"url": "https://example.org/bin"}), 403, dropapi.CodeForbidden)
	}
	wantError(t, h.Op(dev, "developer.build_import", map[string]any{"url": "http://example.org/bin"}), 400, dropapi.CodeInvalidInput)
	link := h.MustOp(dev, "developer.build_upload", nil)
	payload := body
	if archive {
		payload = tarGzipBuild(t, body, []byte("license"))
		for _, invalid := range [][]byte{tarGzipBuild(t, body, body), tarGzipBuild(t, []byte("no executable")), payload[:len(payload)-1]} {
			wantUploadError(t, upload(t, h, link["path"].(string), invalid), 400, dropapi.CodeInvalidRelease)
		}
	}
	reply := h.Upload(link["path"].(string), payload)
	if reply.Code != 200 {
		t.Fatal(reply.Body)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	if err := json.Unmarshal(reply.Body.Bytes(), &info); err != nil || info["sha256"] != digest || info["product"] != "field-notes" || info["version"] != "1.2.3" || num(info["minimum_multiplier"]) != 2 {
		t.Fatal(info, err)
	}
	wantUploadError(t, upload(t, h, link["path"].(string), body), 404, dropapi.CodeUploadLinkInvalid)
	wantError(t, h.Op(other, "developer.build_create", input), 409, "inspection_expired")
	input["multiplier"] = 0
	wantError(t, h.Op(dev, "developer.build_create", input), 400, dropapi.CodeInvalidInput)
	input["multiplier"] = 1
	wantError(t, h.Op(dev, "developer.build_create", input), 409, "beta_no_capacity")
	_, _, node := h.Node("test-node", hostA)
	ready := reportOf(hostA)
	versions := ready["nibrunner"].(map[string]any)["versions"].(map[string]any)
	versions["privateIngress"] = true
	versions["cpuQuarterQuota"] = true
	if code, resp := report(t, h, node, ready); code != 200 {
		t.Fatal(resp)
	}
	app := appOf(t, h.MustOp(dev, "developer.build_create", input))
	if !strings.HasPrefix(app["slug"].(string), "dev-") || app["developer_private"] != true || app["kind"] == "crew" || app["executable_name"] != "field-notes" || num(app["http_port"]) != 8040 || num(app["memory_mib"]) != 256 || num(app["disk_mib"]) != 2048 {
		t.Fatal(app)
	}
	r, err := h.App.FindRecordById("apps", app["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := h.App.FindRecordById("releases", r.GetString("current_release"))
	if err != nil || rel.GetString("sha256") != digest || rel.GetString("s3_key") == "releases/"+digest || r.GetString("official_product") != "" {
		t.Fatal(rel, err)
	}
	if _, err := h.Releases.Head(t.Context(), "releases/"+digest); err == nil {
		t.Fatal("private build in public release store")
	}
}
