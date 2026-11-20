package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// A fake Drop server implementing the agent API contract
// (docs/agent-api.md) closely enough to drive the agent: sign-in, refresh,
// realtime on nodes/<id>, the desired document and the report.

const (
	testDomain   = "drop.example.test"
	testNode     = "node-one"
	testNodeID   = "nodeid000000001"
	testHostID   = "host-test-0001"
	testSecret   = "0123456789abcdef0123456789abcdef01234567"
	testSuffix   = ".apps.example.test"
	testSentinel = "synthetic-secret-sentinel-4f1d"
)

type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	desired       dropapi.Desired
	desiredStatus int    // when set, GET desired answers this status
	desiredCode   string // with this code
	reportStatus  int
	reportCode    string
	tokens        map[string]bool
	tokenN        int
	logins        int
	pulls         int
	reports       [][]byte
	streams       map[int]chan string
	streamN       int
	subscriptions int
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, tokens: map[string]bool{}, streams: map[int]chan string{}}
	f.desired = dropapi.Desired{Schema: dropapi.DesiredSchema, Node: testNode, NodeID: testNodeID, HostID: testHostID, State: "active",
		DesiredSeq: 1, Apps: []dropapi.DesiredApp{}, Rejected: []render.AppError{}, ContentHash: strings.Repeat("0", 64)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.close)
	return f
}

func (f *fakeServer) close() {
	f.dropStreams()
	f.srv.Close()
}

func (f *fakeServer) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeServer) fail(w http.ResponseWriter, status int, code string) {
	f.writeJSON(w, status, map[string]any{"status": status, "code": code, "message": "fake " + code, "data": map[string]any{}})
}

func (f *fakeServer) newToken() string {
	f.tokenN++
	tok := fmt.Sprintf("token-%d", f.tokenN)
	f.tokens[tok] = true
	return tok
}

func (f *fakeServer) authed(r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[r.Header.Get("Authorization")]
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Host != testDomain || r.Header.Get("Origin") != "" {
		f.fail(w, 403, "host")
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Path == dropapi.NodeAuthRoute:
		var body struct{ Identity, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.logins++
		ok := body.Identity == testNode && body.Password == testSecret
		var tok string
		if ok {
			tok = f.newToken()
		}
		f.mu.Unlock()
		if !ok {
			f.fail(w, 400, "invalid_credentials")
			return
		}
		f.writeJSON(w, 200, map[string]any{"token": tok, "record": map[string]any{"id": testNodeID, "collectionName": "nodes", "name": testNode}})
	case r.Method == "POST" && r.URL.Path == dropapi.NodeRefreshRoute:
		if !f.authed(r) {
			f.fail(w, 401, "unauthenticated")
			return
		}
		f.mu.Lock()
		tok := f.newToken()
		f.mu.Unlock()
		f.writeJSON(w, 200, map[string]any{"token": tok, "record": map[string]any{"id": testNodeID, "collectionName": "nodes", "name": testNode}})
	case r.Method == "GET" && r.URL.Path == "/api/realtime":
		f.stream(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/realtime":
		if !f.authed(r) {
			f.fail(w, 401, "unauthenticated")
			return
		}
		var body struct {
			ClientID      string   `json:"clientId"`
			Subscriptions []string `json:"subscriptions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Subscriptions) != 1 || body.Subscriptions[0] != "nodes/"+testNodeID {
			f.fail(w, 403, "forbidden")
			return
		}
		f.mu.Lock()
		f.subscriptions++
		f.mu.Unlock()
		w.WriteHeader(204)
	case r.Method == "GET" && r.URL.Path == dropapi.AgentDesiredRoute:
		if !f.authed(r) {
			f.fail(w, 401, "unauthenticated")
			return
		}
		f.mu.Lock()
		f.pulls++
		status, code, d := f.desiredStatus, f.desiredCode, f.desired
		f.mu.Unlock()
		if status != 0 {
			f.fail(w, status, code)
			return
		}
		f.writeJSON(w, 200, d)
	case r.Method == "POST" && r.URL.Path == dropapi.AgentReportRoute:
		if !f.authed(r) {
			f.fail(w, 401, "unauthenticated")
			return
		}
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(r.Body)
		dec := json.NewDecoder(bytes.NewReader(raw.Bytes()))
		dec.DisallowUnknownFields()
		var rep dropapi.Report
		if err := dec.Decode(&rep); err != nil {
			f.t.Errorf("report does not decode strictly: %v", err)
			f.fail(w, 400, "invalid_request")
			return
		}
		f.mu.Lock()
		f.reports = append(f.reports, raw.Bytes())
		status, code, seq := f.reportStatus, f.reportCode, f.desired.DesiredSeq
		f.mu.Unlock()
		if status != 0 {
			f.fail(w, status, code)
			return
		}
		f.writeJSON(w, 200, dropapi.ReportResult{DesiredSeq: seq, State: "active", Accepted: len(rep.Apps), Ignored: []string{}})
	default:
		f.fail(w, 404, "not_found")
	}
}

func (f *fakeServer) stream(w http.ResponseWriter, r *http.Request) {
	flusher := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f.mu.Lock()
	f.streamN++
	id := f.streamN
	ch := make(chan string, 16)
	f.streams[id] = ch
	f.mu.Unlock()
	fmt.Fprintf(w, "id:%d\nevent:PB_CONNECT\ndata:{\"clientId\":\"client-%d\"}\n\n", id, id)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		}
	}
}

// dropStreams closes every realtime stream (as the hosted write timeout does).
func (f *fakeServer) dropStreams() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, ch := range f.streams {
		close(ch)
		delete(f.streams, id)
	}
}

// setDesired replaces the document, bumps desiredSeq and sends the event.
func (f *fakeServer) setDesired(mutate func(d *dropapi.Desired)) int64 {
	f.mu.Lock()
	mutate(&f.desired)
	f.desired.DesiredSeq++
	seq := f.desired.DesiredSeq
	data, _ := json.Marshal(f.desired.Apps)
	sum := sha256.Sum256(data)
	f.desired.ContentHash = hex.EncodeToString(sum[:])
	msg := fmt.Sprintf("event:nodes/%s\ndata:{\"action\":\"update\",\"record\":{\"id\":%q,\"desired_seq\":%d}}\n\n", testNodeID, testNodeID, seq)
	for _, ch := range f.streams {
		ch <- msg
	}
	f.mu.Unlock()
	return seq
}

func (f *fakeServer) counts() (logins, pulls, reports, subs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins, f.pulls, len(f.reports), f.subscriptions
}

func (f *fakeServer) lastReport(t *testing.T) dropapi.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) == 0 {
		t.Fatal("no report yet")
	}
	var r dropapi.Report
	if err := json.Unmarshal(f.reports[len(f.reports)-1], &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fakeServer) allReports() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(bytes.Join(f.reports, []byte("\n")))
}

// env is one agent's temporary host: nibrunner paths, a state directory,
// in-memory AWS and a log.
type env struct {
	t      *testing.T
	dir    string
	cfg    Config
	stores *awsx.Stores
	server *fakeServer

	logMu sync.Mutex
	log   strings.Builder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, stores: awsx.NewMemoryStores(), server: newFakeServer(t)}
	secretFile := filepath.Join(dir, "node-secret")
	if err := os.WriteFile(secretFile, []byte(testSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nib := filepath.Join(dir, "nibrunner")
	if err := os.MkdirAll(nib, 0o700); err != nil {
		t.Fatal(err)
	}
	e.cfg = Config{
		Server: e.server.srv.URL, Host: testDomain, Node: testNode, SecretFile: secretFile,
		Desired: filepath.Join(nib, "desired.json"), Reported: filepath.Join(nib, "reported.json"),
		Artifacts: filepath.Join(nib, "artifact-store"), StateDir: filepath.Join(dir, "drop-agent"),
		AllowedHostSuffix: testSuffix, SSMPrefix: "/drop",
		PullInterval: time.Hour, ReportInterval: time.Hour,
	}
	e.writeDesired(emptyDoc(testHostID, "empty-1"))
	e.writeReported(nil)
	return e
}

func (e *env) logf(format string, args ...any) {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	fmt.Fprintf(&e.log, format+"\n", args...)
}

func (e *env) logs() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return e.log.String()
}

func emptyDoc(hostID, revision string) []byte {
	return []byte(fmt.Sprintf(`{"hostId":%q,"revision":%q,"instances":[],"volumes":[],"checkpoints":[],"exports":[]}`+"\n", hostID, revision))
}

func (e *env) writeDesired(doc []byte) {
	e.t.Helper()
	if err := os.WriteFile(e.cfg.Desired, doc, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) readDesired() []byte {
	e.t.Helper()
	data, err := os.ReadFile(e.cfg.Desired)
	if err != nil {
		e.t.Fatal(err)
	}
	return data
}

// writeReported writes a reported.json for testHostID with the given
// instances, a fresh reportedAt and a cron listing holding tenant
// environment values (which must never reach a report).
func (e *env) writeReported(instances []map[string]any) {
	e.t.Helper()
	if instances == nil {
		instances = []map[string]any{}
	}
	accepted := docField(readFileOr(e.cfg.Desired), "revision")
	doc := map[string]any{
		"hostId": testHostID, "state": "ready", "reportedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"acceptedRevision": accepted, "acceptedDigest": strings.Repeat("a", 64),
		"capacity":    map[string]any{"vcpuCount": 4, "memoryMib": 8192, "cacheBytes": 1 << 30, "extra": "x"},
		"allocatable": map[string]any{"vcpuCount": 4, "memoryMib": 8192, "cacheBytes": 1 << 30},
		"versions":    map[string]any{"agent": "v1", "firecracker": "v2", "guestImage": "v3", "zerofs": "v4", "other": "dropped"},
		"checkpoints": []any{}, "exports": []any{}, "volumes": []any{},
		"instances": instances,
		"cron": []any{map[string]any{"appId": "d-aaaaaaaaaaaaaaa", "deploymentId": "d-aaaaaaaaaaaaaaa-dep-1", "enabled": true, "timeZone": "UTC",
			"jobs": []any{map[string]any{"jobId": "j", "command": "x", "schedule": "* * * * *", "nextRunAt": nil,
				"environment": map[string]any{"API_KEY": testSentinel}}}}},
	}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(e.cfg.Reported, data, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func readFileOr(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

// release puts a synthetic executable in the blob store and returns it.
func (e *env) release(content string) render.Release {
	e.t.Helper()
	body := []byte("\x7fELF synthetic " + content)
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	key, _ := awsx.ReleaseKey(sha)
	if _, err := e.stores.Blobs.PutIfAbsent(context.Background(), key, bytes.NewReader(body), int64(len(body)), sha); err != nil {
		e.t.Fatal(err)
	}
	return render.Release{SHA256: sha, S3Key: key, SizeBytes: int64(len(body))}
}

// secret stores a value and returns its desired-document reference.
func (e *env) secret(appID, name, value string) dropapi.DesiredSecret {
	e.t.Helper()
	param, err := awsx.SecretParameter("/drop", appID, name)
	if err != nil {
		e.t.Fatal(err)
	}
	version, err := e.stores.Secrets.Put(context.Background(), param, value)
	if err != nil {
		e.t.Fatal(err)
	}
	return dropapi.DesiredSecret{Name: name, Parameter: param, Version: version}
}

func testApp(id string, dep int, rel render.Release, secrets ...dropapi.DesiredSecret) dropapi.DesiredApp {
	return dropapi.DesiredApp{App: render.App{
		AppID: id, DeploymentID: fmt.Sprintf("%s-dep-%d", id, dep), DesiredState: "on-request", IdleTimeoutMs: 300000,
		Volume: render.Volume{VolumeID: id + "-vol", SizeBytes: 1024 << 20}, Release: rel, ExecutableName: "server",
		HTTPPort: 8080, Args: []string{}, Resources: render.Resources{VCPU: 1, MemoryMib: 512},
		HealthCheck: render.HealthCheck{Kind: "tcp"},
		Hostnames:   []render.Hostname{{Hostname: "h-" + id[2:] + testSuffix, Kind: "platform"}},
		Env:         map[string]string{"MODE": "demo"},
	}, Secrets: append([]dropapi.DesiredSecret{}, secrets...)}
}

// start runs the agent until the test ends; the returned channel yields Run's
// result.
func (e *env) start() (*Agent, chan error) {
	e.t.Helper()
	secret, err := ReadSecret(e.cfg.SecretFile)
	if err != nil {
		e.t.Fatal(err)
	}
	a := New(e.cfg, secret, "0.0.0-test", Stores{Blobs: e.stores.Blobs, Secrets: e.stores.Secrets}, e.logf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- a.Run(ctx)
		close(finished)
	}()
	e.t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			e.t.Error("the agent did not stop")
		}
	})
	return a, done
}

// eventually waits up to 10 s for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
