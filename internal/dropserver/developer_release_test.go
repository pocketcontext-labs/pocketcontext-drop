package dropserver_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/agent"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestPrivateDeveloperRelease(t *testing.T) {
	for _, withManifest := range []bool{false, true} {
		t.Run(fmt.Sprintf("manifest=%t", withManifest), func(t *testing.T) {
			testPrivateDeveloperRelease(t, withManifest)
		})
	}
}

func testPrivateDeveloperRelease(t *testing.T, withManifest bool) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "probe")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, err := r.ReadString('\n')
		if err == nil {
			var digest string
			var size int64
			if _, err = fmt.Sscan(line, &digest, &size); err == nil {
				_, err = io.CopyN(io.Discard, r, size)
			}
		}
		if err == nil {
			reply := `{"Outcome":"invalid"}`
			if withManifest {
				manifest := strings.NewReplacer(`"s3_replica",`, "", `"minimum":`, `"optional":["s3_replica"],"minimum":`).Replace(validCrewManifest)
				reply = `{"outcome":"success","manifest":` + manifest + `}`
			}
			_, err = io.WriteString(c, reply)
		}
		done <- err
	}()
	store := awsx.NewMemBlobStore()
	h := droptest.NewHosted(t, droptest.Options{ProbeSocket: socket, BetaNodes: []string{"old-node", "new-node"}, Releases: store})
	h.Releases = store
	owner := h.Owner()
	admin := h.Token(owner)
	joe, user := h.User("private-developer@example.test", "user")
	_, other := h.User("other-private@example.test", "user")
	input := map[string]any{"name": "Developer build", "slug": "private-build"}
	if withManifest {
		input["kind"] = "crew"
	}
	wantError(t, h.Op(user, "developer.create", input), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(admin, "developer.create", input), 403, dropapi.CodeForbidden)
	h.MustOp(admin, "user.set", map[string]any{"user": joe.Id, "role": "developer"})
	if _, err := h.App.DB().NewQuery("INSERT INTO drop_app_deletions (app,owner,slug,nib,completed) VALUES ('past','past','dev-retired-build','d-past',1)").Execute(); err != nil {
		t.Fatal(err)
	}
	wantError(t, h.Op(user, "developer.create", map[string]any{"slug": "retired-build", "name": "Retired host"}), 409, dropapi.CodeSlugTaken)
	app := appOf(t, h.MustOp(user, "developer.create", input))
	if app["developer_private"] != true {
		t.Fatal(app)
	}
	id := app["id"].(string)
	ref := map[string]any{"app": id}
	path := h.MustOp(user, "developer.upload_link", ref)["path"].(string)
	raw := droptest.ELF(8192, 67)
	res := upload(t, h, path, raw)
	if res.Status != 200 {
		t.Fatal(res.Raw)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rel := res.Result.Release
	if rel.SHA256 != sha(raw) || rel.S3Key == "releases/"+sha(raw) {
		t.Fatal(rel)
	}
	if _, err := h.Releases.Head(t.Context(), "releases/"+sha(raw)); err == nil {
		t.Fatal("developer executable is present in plaintext")
	}
	var global int
	if err := h.App.DB().NewQuery("SELECT COUNT(*) FROM drop_beta_catalog").Row(&global); err != nil || global != 0 {
		t.Fatal("private inspection registered public digest", global, err)
	}
	wantError(t, h.Op(other, "release.list", ref), 404, dropapi.CodeNotFound)
	deploy := map[string]any{"app": id, "release": rel.ID}
	wantError(t, h.Op(other, "developer.deploy", deploy), 403, dropapi.CodeForbidden)
	_, _, oldNode := h.Node("old-node", hostA)
	if code, _ := report(t, h, oldNode, reportOf(hostA)); code != 200 {
		t.Fatal(code)
	}
	wantError(t, h.Op(user, "developer.deploy", deploy), 409, "beta_no_capacity")
	_, _, node := h.Node("new-node", hostB)
	ready := reportOf(hostB)
	versions := ready["nibrunner"].(map[string]any)["versions"].(map[string]any)
	versions[dropapi.CustomPublicIngress] = true
	if code, _ := report(t, h, node, ready); code != 200 {
		t.Fatal(code)
	}
	h.MustOp(user, "developer.deploy", deploy)
	if withManifest {
		// A resize must continue to honor the installed private manifest.
		wantError(t, h.Op(user, "app.set", map[string]any{"app": id, "http_port": 9090}), 400, dropapi.CodeInvalidInput)
	}
	d, _ := desired(t, h, node)
	wantHost := app["hostname"].(string)
	if len(d.Apps) != 1 || d.Apps[0].PrivateRoute != nil || d.Apps[0].Release.PrivateKey == "" || len(d.Apps[0].Hostnames) != 1 || d.Apps[0].Hostnames[0].Hostname != wantHost || d.Apps[0].Hostnames[0].Kind != "platform" {
		t.Fatal("missing public hostname or encrypted release")
	}
	// Recreate the persisted state of an app placed before public custom
	// routing. The upgraded agent's first capability report must switch
	// that same deployment and volume to the normal platform hostname.
	if _, err := h.App.DB().NewQuery("UPDATE apps SET hostname='' WHERE id={:id}").Bind(dbx.Params{"id": id}).Execute(); err != nil {
		t.Fatal(err)
	}
	delete(versions, dropapi.CustomPublicIngress)
	versions["privateIngress"] = true
	if code, resp := report(t, h, node, ready); code != 200 {
		t.Fatal(resp)
	}
	before, _ := desired(t, h, node)
	if len(before.Apps) != 1 || before.Apps[0].PrivateRoute == nil || before.Apps[0].PrivateRoute.Hostname != wantHost {
		t.Fatal("legacy agent lost its placed route")
	}
	versions[dropapi.CustomPublicIngress] = true
	if code, resp := report(t, h, node, ready); code != 200 {
		t.Fatal(resp)
	}
	after, _ := desired(t, h, node)
	if len(after.Apps) != 1 || after.DesiredSeq <= before.DesiredSeq || after.Apps[0].PrivateRoute != nil || len(after.Apps[0].Hostnames) != 1 || after.Apps[0].Hostnames[0].Hostname != wantHost || after.Apps[0].DeploymentID != before.Apps[0].DeploymentID || after.Apps[0].Volume.VolumeID != before.Apps[0].Volume.VolumeID {
		t.Fatal("placed custom app did not reconcile to its public route")
	}
	inactive, doc := desired(t, h, oldNode)
	if len(inactive.Apps) != 0 || strings.Contains(doc, d.Apps[0].Release.PrivateKey) {
		t.Fatal("other node received private release")
	}
	stager := agent.Stager{Dir: filepath.Join(dir, "artifacts"), Blobs: h.Releases, Cache: map[string]agent.ArtifactInfo{}}
	if err := stager.Stage(t.Context(), d.Apps[0].Release); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(stager.Dir, rel.SHA256))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("staged bytes differ", err)
	}
	wantError(t, h.Op(user, "developer.submit", deploy), 400, dropapi.CodeInvalidInput)
	path = h.MustOp(user, "developer.upload_link", ref)["path"].(string)
	h.MustOp(admin, "user.set", map[string]any{"user": joe.Id, "role": "user"})
	wantUploadError(t, upload(t, h, path, raw), 404, dropapi.CodeUploadLinkInvalid)
	wantError(t, h.Op(user, "developer.deploy", deploy), 403, dropapi.CodeForbidden)
	if withManifest {
		testCatalogSourceDeletion(t, h, user, node, id, rel)
	}

}
