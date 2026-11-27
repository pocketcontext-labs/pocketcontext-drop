package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/leakscan"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

const appA = "d-aaaaaaaaaaaaaaa"
const appB = "d-bbbbbbbbbbbbbbb"

type parsedDoc struct {
	HostID    string `json:"hostId"`
	Revision  string `json:"revision"`
	Instances []struct {
		AppID        string `json:"appId"`
		DeploymentID string `json:"deploymentId"`
		DesiredState string `json:"desiredState"`
		Layers       []struct {
			ObjectKey string `json:"objectKey"`
		} `json:"layers"`
		Config struct {
			Command struct {
				Environment map[string]string `json:"environment"`
			} `json:"command"`
		} `json:"config"`
	} `json:"instances"`
	Volumes []struct {
		AppID        string `json:"appId"`
		DesiredState string `json:"desiredState"`
	} `json:"volumes"`
}

func parse(t *testing.T, doc []byte) parsedDoc {
	t.Helper()
	var p parsedDoc
	if err := json.Unmarshal(doc, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func revisionIs(e *env, prefix string) func() bool {
	return func() bool { return strings.HasPrefix(docField(readFileOr(e.cfg.Desired), "revision"), prefix) }
}

func TestRunWritesDesiredAndReports(t *testing.T) {
	e := newEnv(t)
	rel := e.release("one")
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Apps = []dropapi.DesiredApp{testApp(appA, 3, rel, e.secret(appA, "API_KEY", testSentinel))}
	})
	before := e.readDesired()
	e.start()
	eventually(t, "the first write", revisionIs(e, "drop-2-"))

	doc := e.readDesired()
	if err := render.ValidateSchema(doc); err != nil {
		t.Fatalf("written document fails the schema: %v", err)
	}
	p := parse(t, doc)
	if p.HostID != testHostID || len(p.Instances) != 1 || p.Instances[0].AppID != appA || p.Instances[0].DeploymentID != appA+"-dep-3" {
		t.Fatalf("unexpected document: %s", doc)
	}
	if p.Instances[0].Layers[0].ObjectKey != rel.SHA256 || p.Instances[0].Config.Command.Environment["API_KEY"] != testSentinel {
		t.Fatal("the layer must point at the staged sha and the secret be resolved")
	}
	info, _ := os.Stat(e.cfg.Desired)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("desired.json mode %v, want 0600", info.Mode().Perm())
	}
	if prev := readFileOr(e.cfg.BackupPath()); !bytes.Equal(prev, before) {
		t.Fatalf("desired.json.drop-prev must hold the previous file, got %q", prev)
	}
	staged, err := os.ReadFile(filepath.Join(e.cfg.Artifacts, rel.SHA256))
	if err != nil || int64(len(staged)) != rel.SizeBytes {
		t.Fatalf("release not staged under its sha256: %v", err)
	}
	if entries, _ := os.ReadDir(e.cfg.Artifacts); len(entries) != 1 {
		t.Fatalf("no temporary files may stay in the artifact store: %v", entries)
	}

	// The report follows the write: sanitised, with cron stripped.
	eventually(t, "a report", func() bool { _, _, n, _ := e.server.counts(); return n > 0 })
	r := e.server.lastReport(t)
	if r.Schema != dropapi.ReportSchema || r.HostID != testHostID || r.DesiredSeq != 2 || r.AgentVersion != "0.0.0-test" {
		t.Fatalf("report header: %+v", r)
	}
	if len(r.Apps) != 1 || r.Apps[0].AppID != appA || r.Apps[0].Phase != "pending" {
		t.Fatalf("report apps: %+v", r.Apps)
	}
	if r.Nibrunner == nil || r.Nibrunner.Capacity["extra"] != nil || r.Nibrunner.Versions["other"] != nil || r.Nibrunner.Capacity["vcpuCount"] == nil {
		t.Fatalf("host report must keep only the known numbers and versions: %+v", r.Nibrunner)
	}
	if r.Nibrunner.Versions[dropapi.CustomPublicIngress] != true {
		t.Fatal("agent must advertise custom app public routing independently of the legacy gateway")
	}
	all := e.server.allReports()
	if strings.Contains(all, "cron") || strings.Contains(all, testSentinel) {
		t.Fatal("a report must never carry cron or a secret value")
	}

	// state.json: metadata only.
	state := readFileOr(e.cfg.StatePath())
	var s State
	if err := json.Unmarshal(state, &s); err != nil || s.Schema != StateSchema || s.LastSeq != 2 || s.NodeID != testNodeID ||
		s.HostID != testHostID || !strings.HasPrefix(s.LastRevision, "drop-2-") || len(s.Apps) != 1 || s.Artifacts[rel.SHA256].Size != rel.SizeBytes {
		t.Fatalf("state.json: %s", state)
	}
	if info, _ := os.Stat(e.cfg.StatePath()); info.Mode().Perm() != 0o600 {
		t.Fatal("state.json must be 0600")
	}
	for _, forbidden := range []string{testSentinel, testSecret, "token-"} {
		if strings.Contains(string(state), forbidden) || strings.Contains(e.logs(), forbidden) {
			t.Fatalf("state.json and logs must not hold %q", forbidden)
		}
	}
}

func TestRealtimeEventAndReconnectTriggerPulls(t *testing.T) {
	e := newEnv(t)
	e.start()
	eventually(t, "the first pull and subscription", func() bool { _, p, _, s := e.server.counts(); return p >= 1 && s >= 1 })
	// A desired_seq event pulls and writes.
	rel := e.release("two")
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, rel)} })
	eventually(t, "a write after the event", revisionIs(e, "drop-2-"))
	// A dropped stream reconnects, resubscribes and pulls again.
	_, pulls, _, subs := e.server.counts()
	e.server.dropStreams()
	eventually(t, "reconnect, resubscribe and pull", func() bool { _, p, _, s := e.server.counts(); return s > subs && p > pulls })
}

func TestTickerPullsWithoutEvents(t *testing.T) {
	e := newEnv(t)
	e.cfg.PullInterval = 200 * time.Millisecond
	e.start()
	eventually(t, "the first pull", func() bool { _, p, _, _ := e.server.counts(); return p >= 1 })
	// Change the document without an event: the ticker finds it.
	e.server.mu.Lock()
	e.server.desired.DesiredSeq = 5
	e.server.desired.Apps = []dropapi.DesiredApp{testApp(appA, 1, e.release("tick"))}
	e.server.desired.ContentHash, _ = e.server.desired.ComputeContentHash()
	e.server.mu.Unlock()
	eventually(t, "the ticker's write", revisionIs(e, "drop-5-"))
}

func TestServerDownOrErrorNeverWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{{"unavailable", 503, "desired_unavailable"}, {"disabled", 403, "node_disabled"}, {"rate limited", 429, "rate_limited"}, {"internal", 500, "internal"}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg.PullInterval = 100 * time.Millisecond
			before := e.readDesired()
			e.server.mu.Lock()
			e.server.desiredStatus, e.server.desiredCode = tc.status, tc.code
			e.server.mu.Unlock()
			e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, e.release("x"))} })
			e.start()
			eventually(t, "several failed pulls", func() bool { _, p, _, _ := e.server.counts(); return p >= 3 })
			if !bytes.Equal(e.readDesired(), before) || readFileOr(e.cfg.BackupPath()) != nil {
				t.Fatal("desired.json must stay untouched when the server answers an error")
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		e := newEnv(t)
		e.cfg.PullInterval = 100 * time.Millisecond
		previous := e.readDesired()
		seq := e.server.setDesired(func(d *dropapi.Desired) {
			d.Apps = []dropapi.DesiredApp{testApp(appA, 1, e.release("running"))}
		})
		e.start()
		eventually(t, "the successful pull to be persisted", func() bool {
			s, err := loadState(e.cfg.StatePath())
			return err == nil && s.LastSeq == seq && s.LastError == ""
		})
		before := e.readDesired()
		e.server.srv.CloseClientConnections()
		e.server.srv.Close()
		eventually(t, "unreachable pulls to be retried and persisted", func() bool {
			s, err := loadState(e.cfg.StatePath())
			return err == nil && s.LastSeq == seq && strings.Contains(s.LastError, "not reachable") &&
				strings.Count(e.logs(), "pull: the Drop server is not reachable") >= 3
		})
		if !bytes.Equal(e.readDesired(), before) {
			t.Fatal("desired.json must stay untouched while the server is down")
		}
		if !bytes.Equal(readFileOr(e.cfg.BackupPath()), previous) {
			t.Fatal("the desired.json backup must stay untouched while the server is down")
		}
	})
}

func TestMalformedDocumentNeverWrites(t *testing.T) {
	e := newEnv(t)
	e.cfg.PullInterval = 100 * time.Millisecond
	before := e.readDesired()
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Schema = "drop.desired/v0"
		d.Apps = []dropapi.DesiredApp{testApp(appA, 1, e.release("m"))}
	})
	e.start()
	eventually(t, "pulls", func() bool { _, p, _, _ := e.server.counts(); return p >= 3 })
	if !bytes.Equal(e.readDesired(), before) {
		t.Fatal("a malformed document must not be written")
	}
}

func TestIncompleteSnapshotKeepsRunningApp(t *testing.T) {
	e := newEnv(t)
	rel := e.release("stable")
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, rel)} })
	e.start()
	eventually(t, "the first deployment", revisionIs(e, "drop-2-"))
	before := e.readDesired()
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = nil })
	eventually(t, "the refused snapshot", func() bool {
		return bytes.Contains(readFileOr(e.cfg.StatePath()), []byte("needs apps and rejected arrays"))
	})
	if !bytes.Equal(e.readDesired(), before) {
		t.Fatal("a partial snapshot stopped or changed the running app")
	}
}

func TestDesiredSnapshotValidation(t *testing.T) {
	e := newEnv(t)
	app := testApp(appA, 1, e.release("snapshot"))
	for _, tc := range []struct {
		name   string
		change func(*dropapi.Desired)
	}{
		{"null apps", func(d *dropapi.Desired) { d.Apps = nil }},
		{"null rejected", func(d *dropapi.Desired) { d.Rejected = nil }},
		{"foreign app", func(d *dropapi.Desired) { d.Apps[0].AppID = "other" }},
		{"foreign rejection", func(d *dropapi.Desired) { d.Rejected = []render.AppError{{AppID: "other"}} }},
		{"ambiguous membership", func(d *dropapi.Desired) { d.Rejected = []render.AppError{{AppID: appA}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := e.server.desired
			d.Apps = []dropapi.DesiredApp{app}
			tc.change(&d)
			d.ContentHash, _ = d.ComputeContentHash()
			raw, _ := json.Marshal(d)
			if _, err := decodeDesired(raw); err == nil {
				t.Fatal("accepted malformed snapshot")
			}
		})
	}
	d := e.server.desired
	d.Apps = []dropapi.DesiredApp{app} // hash still belongs to the empty snapshot
	raw, _ := json.Marshal(d)
	if _, err := decodeDesired(raw); err == nil {
		t.Fatal("accepted mismatched content hash")
	}
	raw, _ = json.Marshal(e.server.desired)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "apps")
	raw, _ = json.Marshal(fields)
	if _, err := decodeDesired(raw); err == nil {
		t.Fatal("accepted snapshot with missing apps")
	}
}

func TestForeignEntriesRefuseStartWithoutWriting(t *testing.T) {
	e := newEnv(t)
	doc := []byte(`{"hostId":"host-test-0001","revision":"canary-5","instances":[{"appId":"canary"}],"volumes":[{"appId":"crew-dev","volumeId":"crew-dev-vol"}],"checkpoints":[],"exports":[]}`)
	e.writeDesired(doc)
	_, done := e.start()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent must refuse to start")
	}
	if !IsRefusal(err) || !strings.Contains(err.Error(), "canary") || !strings.Contains(err.Error(), "crew-dev") {
		t.Fatalf("want a refusal naming canary and crew-dev, got %v", err)
	}
	if !bytes.Equal(e.readDesired(), doc) || readFileOr(e.cfg.BackupPath()) != nil || readFileOr(e.cfg.StatePath()) != nil {
		t.Fatal("a refused start writes nothing")
	}
	if l, p, _, _ := e.server.counts(); l != 0 || p != 0 {
		t.Fatal("a refused start does not even sign in")
	}
	// check reports the same and writes nothing.
	res := Check(context.Background(), e.cfg, Stores{Blobs: e.stores.Blobs, Secrets: e.stores.Secrets}, "test")
	if res.OK || !res.Refused || !strings.Contains(res.Checks[0].Message, "canary") || !strings.Contains(res.Checks[0].Message, "crew-dev") {
		t.Fatalf("check: %+v", res)
	}
	if !bytes.Equal(e.readDesired(), doc) || readFileOr(e.cfg.StatePath()) != nil {
		t.Fatal("check writes nothing")
	}
}

func TestForeignEntryAddedWhileRunningStops(t *testing.T) {
	e := newEnv(t)
	e.cfg.PullInterval = 100 * time.Millisecond
	_, done := e.start()
	eventually(t, "the first pull", func() bool { _, p, _, _ := e.server.counts(); return p >= 1 })
	doc := []byte(`{"hostId":"host-test-0001","revision":"x","instances":[],"volumes":[{"appId":"hand-made","volumeId":"v"}],"checkpoints":[],"exports":[]}`)
	e.writeDesired(doc)
	select {
	case err := <-done:
		if !IsRefusal(err) || !strings.Contains(err.Error(), "hand-made") {
			t.Fatalf("want a refusal naming hand-made, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent must stop on a foreign entry")
	}
	if !bytes.Equal(e.readDesired(), doc) {
		t.Fatal("the agent never rewrites a file holding a foreign entry")
	}
}

func TestHostMismatchStops(t *testing.T) {
	e := newEnv(t)
	e.server.mu.Lock()
	e.server.desired.HostID = "another-host"
	e.server.desired.ContentHash, _ = e.server.desired.ComputeContentHash()
	e.server.mu.Unlock()
	before := e.readDesired()
	_, done := e.start()
	select {
	case err := <-done:
		if !IsRefusal(err) || !strings.Contains(err.Error(), "another-host") {
			t.Fatalf("want a host refusal, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent must stop on a host mismatch")
	}
	if !bytes.Equal(e.readDesired(), before) {
		t.Fatal("no write on a host mismatch")
	}

	// A report answered 409 host_mismatch stops it too.
	e2 := newEnv(t)
	e2.server.mu.Lock()
	e2.server.reportStatus, e2.server.reportCode = 409, "host_mismatch"
	e2.server.mu.Unlock()
	e2.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, e2.release("h"))} })
	_, done2 := e2.start()
	select {
	case err := <-done2:
		if !IsRefusal(err) {
			t.Fatalf("want a refusal, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a 409 host_mismatch report must stop the agent")
	}
}

func TestUnauthorizedSignsInAgain(t *testing.T) {
	e := newEnv(t)
	e.start()
	eventually(t, "the first pull", func() bool { _, p, _, _ := e.server.counts(); return p >= 1 })
	logins, _, _, _ := e.server.counts()
	// End every token (as a rotation or a restore would), then change the document.
	e.server.mu.Lock()
	e.server.tokens = map[string]bool{}
	e.server.mu.Unlock()
	time.Sleep(5100 * time.Millisecond) // the client signs in again at most every 5 s
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, e.release("u"))} })
	e.server.dropStreams()
	eventually(t, "a write after signing in again", revisionIs(e, "drop-2-"))
	if l, _, _, _ := e.server.counts(); l <= logins {
		t.Fatal("a 401 must lead to a new sign-in")
	}
}

func TestWrongSecretKeepsRetryingWithoutWriting(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.cfg.SecretFile, []byte(strings.Repeat("z", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	before := e.readDesired()
	e.start()
	eventually(t, "a refused sign-in", func() bool { return strings.Contains(e.logs(), "wrong node name or secret") })
	if !bytes.Equal(e.readDesired(), before) {
		t.Fatal("no write without a sign-in")
	}
	if strings.Contains(e.logs(), strings.Repeat("z", 40)) {
		t.Fatal("the secret must never be logged")
	}
}

func TestStagingAndSecretFailuresKeepPreviousEntries(t *testing.T) {
	e := newEnv(t)
	relA, relB := e.release("a1"), e.release("b1")
	secA := e.secret(appA, "API_KEY", testSentinel)
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Apps = []dropapi.DesiredApp{testApp(appA, 1, relA, secA), testApp(appB, 1, relB)}
	})
	e.start()
	eventually(t, "the first write", revisionIs(e, "drop-2-"))
	first := parse(t, e.readDesired())

	// A new deployment of A whose secret version is missing, and of B whose
	// release is not in the store: both keep their previous entries.
	missing := e.secret(appA, "API_KEY", "v2")
	missing.Version = 9
	ghost := render.Release{SHA256: strings.Repeat("e", 64), S3Key: "releases/" + strings.Repeat("e", 64), SizeBytes: 10}
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Apps = []dropapi.DesiredApp{testApp(appA, 2, relA, missing), testApp(appB, 2, ghost)}
	})
	eventually(t, "a report with both failures", func() bool {
		_, _, n, _ := e.server.counts()
		if n == 0 {
			return false
		}
		r := e.server.lastReport(t)
		return r.DesiredSeq == 3 && len(r.Apps) == 2 && r.Apps[0].Phase == "secret_failed" && r.Apps[1].Phase == "staging_failed"
	})
	// No change to the file: the previous entries were kept unchanged.
	second := parse(t, e.readDesired())
	if second.Revision != first.Revision || second.Instances[0].DeploymentID != appA+"-dep-1" || second.Instances[1].DeploymentID != appB+"-dep-1" {
		t.Fatalf("previous entries must be kept: %+v", second)
	}
	if strings.Contains(e.server.allReports(), testSentinel) {
		t.Fatal("no secret value in reports")
	}
	// A new app whose staging fails is left out, never referenced unstaged.
	appC := "d-ccccccccccccccc"
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Apps = []dropapi.DesiredApp{testApp(appA, 1, relA, secA), testApp(appB, 1, relB), testApp(appC, 1, ghost)}
	})
	eventually(t, "the report for seq 4", func() bool { r := e.server.lastReport(t); return r.DesiredSeq == 4 })
	for _, inst := range parse(t, e.readDesired()).Instances {
		if inst.AppID == appC {
			t.Fatal("an app whose release could not be staged must be left out")
		}
	}
}

func TestDigestMismatchRefused(t *testing.T) {
	e := newEnv(t)
	rel := e.release("real")
	bad := rel
	bad.SizeBytes++ // the store's object differs from what the document says
	s := &Stager{Dir: e.cfg.Artifacts, Blobs: e.stores.Blobs, Cache: map[string]ArtifactInfo{}}
	if err := s.Stage(context.Background(), bad); err == nil {
		t.Fatal("a size mismatch must be refused")
	}
	lie := render.Release{SHA256: strings.Repeat("1", 64), S3Key: rel.S3Key, SizeBytes: rel.SizeBytes}
	if err := s.Stage(context.Background(), lie); err == nil || !strings.Contains(err.Error(), "s3Key") {
		t.Fatalf("an s3Key that is not releases/<sha256> must be refused: %v", err)
	}
	if entries, _ := os.ReadDir(e.cfg.Artifacts); len(entries) != 0 {
		t.Fatalf("nothing may be staged: %v", entries)
	}
	// A corrupted staged file is replaced by a verified download.
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(e.cfg.Artifacts, rel.SHA256)
	corrupt := bytes.Repeat([]byte("x"), int(rel.SizeBytes))
	if err := os.WriteFile(dest, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cache = map[string]ArtifactInfo{}
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if got, _ := fileSHA256(dest); got != rel.SHA256 {
		t.Fatal("a corrupted artifact must be downloaded again")
	}
}

func TestSecretParameterFromServerIsChecked(t *testing.T) {
	e := newEnv(t)
	sec := e.secret(appA, "API_KEY", testSentinel)
	sec.Parameter = "/drop/apps/" + appB[2:] + "/API_KEY" // another app's parameter
	s := &Secrets{Store: e.stores.Secrets, Prefix: "/drop"}
	if _, err := s.Resolve(context.Background(), appA, appA+"-dep-1", []dropapi.DesiredSecret{sec}); err == nil {
		t.Fatal("a parameter of another app must be refused")
	} else if strings.Contains(err.Error(), testSentinel) {
		t.Fatal("errors never carry values")
	}
}

func TestRecreatedSecretBetweenPulls(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.secret(appA, "API_KEY", "first")
	s := &Secrets{Store: e.stores.Secrets, Prefix: e.cfg.SSMPrefix}
	if _, err := s.Resolve(ctx, appA, appA+"-dep-2", []dropapi.DesiredSecret{first}); err != nil {
		t.Fatal(err)
	}
	if err := e.stores.Secrets.Delete(ctx, first.Parameter); err != nil {
		t.Fatal(err)
	}
	second := e.secret(appA, "API_KEY", "second")
	if second.Version != first.Version {
		t.Fatal("expected recreated parameter to restart the version counter")
	}
	values, err := s.Resolve(ctx, appA, appA+"-dep-4", []dropapi.DesiredSecret{second})
	if err != nil || values["API_KEY"] != "second" {
		t.Fatal("new deployment must resolve the recreated parameter")
	}
}

func TestTombstonesAndServerRejectedApps(t *testing.T) {
	e := newEnv(t)
	relA, relB := e.release("ta"), e.release("tb")
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Apps = []dropapi.DesiredApp{testApp(appA, 1, relA), testApp(appB, 1, relB)}
	})
	e.start()
	eventually(t, "the first write", revisionIs(e, "drop-2-"))
	// A is gone (archived or moved): a stopped tombstone with its volume kept.
	// B is refused by the server: its previous entry stays unchanged.
	e.server.setDesired(func(d *dropapi.Desired) {
		d.Apps = []dropapi.DesiredApp{}
		d.Rejected = []render.AppError{{AppID: appB, Code: "invalid_app", Field: "resources.vcpu", Message: "too big"}}
	})
	eventually(t, "the second write", revisionIs(e, "drop-3-"))
	p := parse(t, e.readDesired())
	if len(p.Instances) != 2 || p.Instances[0].DesiredState != "stopped" || p.Instances[1].DesiredState != "on-request" {
		t.Fatalf("A tombstoned, B kept: %+v", p.Instances)
	}
	for _, v := range p.Volumes {
		if v.DesiredState != "present" {
			t.Fatal("volumes are never absent")
		}
	}
	eventually(t, "the report", func() bool { r := e.server.lastReport(t); return r.DesiredSeq == 3 })
	r := e.server.lastReport(t)
	if len(r.Apps) != 1 || r.Apps[0].AppID != appB || r.Apps[0].Phase != "failed" || r.Apps[0].Message == "" {
		t.Fatalf("the server-refused app is reported failed: %+v", r.Apps)
	}
}

func TestUnchangedContentSkipsTheWrite(t *testing.T) {
	e := newEnv(t)
	rel := e.release("same")
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, rel)} })
	e.start()
	eventually(t, "the first write", revisionIs(e, "drop-2-"))
	info1, _ := os.Stat(e.cfg.Desired)
	// A new seq with the same apps (for example a rename) changes nothing on disk.
	e.server.setDesired(func(d *dropapi.Desired) {})
	eventually(t, "the state at seq 3", func() bool { return strings.Contains(string(readFileOr(e.cfg.StatePath())), `"lastSeq": 3`) })
	info2, _ := os.Stat(e.cfg.Desired)
	if !info2.ModTime().Equal(info1.ModTime()) || !strings.HasPrefix(docField(e.readDesired(), "revision"), "drop-2-") {
		t.Fatal("an unchanged document must not be rewritten")
	}
}

func TestReportPhases(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second).Format(time.RFC3339)
	rev := "drop-7-0123456789ab"
	one := 1
	r := &nibReported{HostID: testHostID, State: "ready", ReportedAt: fresh, AcceptedRevision: &rev, Instances: []nibInstance{
		{AppID: appA, DeploymentID: appA + "-dep-2", State: "idle", RestartCount: 2, LastExitCode: &one},
		{AppID: appB, DeploymentID: appB + "-dep-1", State: "running"},
	}}
	intents := []appIntent{{AppID: appA, DeploymentID: appA + "-dep-2"}, {AppID: appB, DeploymentID: appB + "-dep-2"}, {AppID: "d-ccccccccccccccc", DeploymentID: "d-ccccccccccccccc-dep-1"}}
	got := appReports(intents, r, rev, now)
	if got[0].Phase != "sleeping" || got[0].RestartCount != 2 || *got[0].LastExitCode != 1 || got[0].DeploymentID != appA+"-dep-2" {
		t.Fatalf("idle maps to sleeping: %+v", got[0])
	}
	if got[1].Phase != "deploying" || got[1].DeploymentID != appB+"-dep-1" {
		t.Fatalf("an older deployment is deploying, reporting what runs: %+v", got[1])
	}
	if got[2].Phase != "pending" || got[2].DeploymentID != "" {
		t.Fatalf("not yet seen is pending: %+v", got[2])
	}
	other := "drop-6-aaaaaaaaaaaa"
	if p := appReports(intents[:1], r, other, now)[0].Phase; p != "deploying" {
		t.Fatalf("before nibrunner accepts the written revision it is deploying, got %s", p)
	}
	if p := appReports(intents[:1], r, rev, now.Add(2*time.Minute))[0]; p.Phase != "unknown" || p.Message != "nibrunner report stale" {
		t.Fatalf("a stale report is unknown: %+v", p)
	}
	for state, phase := range map[string]string{"pending": "starting", "starting": "starting", "running": "running", "idle": "sleeping",
		"unhealthy": "unhealthy", "stopping": "stopped", "stopped": "stopped", "failed": "failed", "weird": "unknown"} {
		if nibPhase(state) != phase {
			t.Errorf("%s maps to %s", state, nibPhase(state))
		}
	}

}

func TestReportOmitsNibrunnerDiagnosticText(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	rev := "drop-2-0123456789ab"
	message := "diagnostic: aZ! " + testSentinel
	raw, err := json.Marshal(map[string]any{
		"hostId": testHostID, "message": message, "reportedAt": now.UTC().Format(time.RFC3339Nano), "acceptedRevision": rev,
		"instances": []any{map[string]any{"appId": appA, "deploymentId": appA + "-dep-1", "state": "failed", "message": message}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(e.cfg.Reported, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reported, err := readReported(e.cfg.Reported)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReport(testHostID, "dev", 2, []appIntent{{AppID: appA, DeploymentID: appA + "-dep-1"}}, reported, rev, now)
	if report.Apps[0].Phase != "failed" || report.Apps[0].Message != "" || report.Nibrunner.Message != "" {
		t.Fatal("host and instance diagnostic text must stay on the node")
	}
}

func TestReadOnlyDirectoryLeavesFileUntouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "desired.json")
	old := emptyDoc(testHostID, "old")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := writeDesired(path, path+BackupSuffix, old, []byte("{}")); err == nil {
		t.Fatal("the write must fail")
	}
	if got := readFileOr(path); !bytes.Equal(got, old) {
		t.Fatal("a failed write leaves the old file whole")
	}
	// A file changed behind the agent's back is never overwritten.
	os.Chmod(dir, 0o700)
	if err := writeDesired(path, path+BackupSuffix, []byte("other"), []byte("{}")); err == nil {
		t.Fatal("a changed file must not be overwritten")
	}
}

func TestRenderDry(t *testing.T) {
	e := newEnv(t)
	rel := e.release("dry")
	d := dropapi.Desired{Schema: dropapi.DesiredSchema, Node: testNode, NodeID: testNodeID, HostID: testHostID, State: "active", DesiredSeq: 4,
		Apps: []dropapi.DesiredApp{testApp(appA, 1, rel, e.secret(appA, "API_KEY", testSentinel))}, Rejected: []render.AppError{}}
	d.ContentHash, _ = d.ComputeContentHash()
	input, _ := json.Marshal(d)
	out, err := RenderDry(input, e.readDesired(), render.Limits{AllowedHostSuffix: testSuffix, SSMPrefix: "/drop"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || !strings.HasPrefix(out.Revision, "drop-4-") || !strings.Contains(string(out.Document), RedactedValue) || strings.Contains(string(out.Document), testSentinel) {
		t.Fatalf("dry run: %+v", out)
	}
	if _, err := RenderDry(input, []byte(`{"hostId":"host-test-0001","instances":[{"appId":"canary"}],"volumes":[]}`), render.Limits{AllowedHostSuffix: testSuffix, SSMPrefix: "/drop"}); !IsRefusal(err) {
		t.Fatalf("a foreign previous entry is refused: %v", err)
	}
}

func TestPreviewRedactsPreviousEnvironment(t *testing.T) {
	e := newEnv(t)
	rel := e.release("preview")
	s := e.secret(appA, "API_KEY", testSentinel)
	app := testApp(appA, 1, rel, s)
	limits := render.Limits{AllowedHostSuffix: testSuffix, SSMPrefix: "/drop"}
	previous, err := render.Render(render.Input{HostID: testHostID, DesiredSeq: 2, Limits: limits,
		Apps: []render.App{app.RenderApp(map[string]string{"API_KEY": testSentinel})}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		apps     []dropapi.DesiredApp
		rejected []render.AppError
	}{
		{"tombstoned", []dropapi.DesiredApp{}, []render.AppError{}},
		{"kept by server", []dropapi.DesiredApp{}, []render.AppError{{AppID: appA, Code: "invalid_app"}}},
		{"rejected locally", []dropapi.DesiredApp{app}, []render.AppError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := e.server.desired
			d.Apps, d.Rejected = tc.apps, tc.rejected
			if len(d.Apps) > 0 {
				d.Apps[0].HTTPPort = 0
			}
			d.ContentHash, _ = d.ComputeContentHash()
			raw, _ := json.Marshal(d)
			out, err := RenderDry(raw, previous.Document, limits)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			leakscan.Require(t, []leakscan.Sentinel{{Label: "previous API key", Value: testSentinel}}, leakscan.Bytes("agent render output", data))
			if parse(t, out.Document).Instances[0].Config.Command.Environment["API_KEY"] != RedactedValue {
				t.Fatal("preview exposed a carried-over environment value")
			}
		})
	}
}

func TestConfig(t *testing.T) {
	env := map[string]string{
		EnvHost: testDomain, EnvNode: testNode, EnvDesired: "/n/desired.json", EnvReported: "/n/reported.json", EnvArtifacts: "/n/a",
		EnvAllowedHostSuffix: testSuffix, EnvCredentialsDirectory: "/run/credentials/drop-agent.service", EnvStateDirectory: "/var/lib/drop-agent",
	}
	get := func(k string) string { return env[k] }
	c, err := LoadConfig(get, Overrides{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != DefaultServer || c.SecretFile != "/run/credentials/drop-agent.service/node-secret" || c.StateDir != "/var/lib/drop-agent" ||
		c.SSMPrefix != "/drop" || c.PullInterval != time.Minute || c.ReportInterval != 30*time.Second {
		t.Fatalf("defaults: %+v", c)
	}
	if _, err := LoadConfig(get, Overrides{"server": "http://10.0.0.5:8100", "fake-aws": "/tmp/x"}, true); err == nil {
		t.Fatal("--fake-aws needs a loopback server")
	}
	for _, server := range []string{"http://10.0.0.5:8100", "http://localhost.example.test"} {
		if _, err := LoadConfig(get, Overrides{"server": server}, true); err == nil {
			t.Fatal("remote HTTP accepted")
		}
	}
	for _, server := range []string{"https://drop.example.test", "http://[::1]:8100"} {
		if _, err := LoadConfig(get, Overrides{"server": server}, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadConfig(get, Overrides{"desired": "relative.json"}, true); err == nil {
		t.Fatal("paths must be absolute")
	}
	env[EnvPullInterval] = "1"
	if _, err := LoadConfig(get, Overrides{}, true); err == nil {
		t.Fatal("intervals are bounded")
	}
	// The secret file must be private.
	dir := t.TempDir()
	path := filepath.Join(dir, "s")
	os.WriteFile(path, []byte(testSecret), 0o644)
	if _, err := ReadSecret(path); err == nil {
		t.Fatal("a group- or world-readable secret file is refused")
	}
	os.Chmod(path, 0o600)
	if s, err := ReadSecret(path); err != nil || s != testSecret {
		t.Fatalf("ReadSecret: %v", err)
	}
}
