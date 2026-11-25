package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/hostservices"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/leakscan"
)

const (
	hsApp     = "d-0123456789abcde"
	hsDep     = "d-0123456789abcde-dep-2"
	hsOldDep  = "d-0123456789abcde-dep-1"
	hsBucket  = "crew-replica"
	stsKey    = "ASIAFAKESESSIONKEY01"
	stsSecret = "fake-sts-secret-value-never-in-a-guest"
	stsToken  = "fake-sts-session-token-never-in-a-guest"
)

// fakeS3 is an in-memory S3 that accepts only the STS session's key.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	badAuth int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Authorization"), "Credential="+stsKey+"/") || r.Header.Get("X-Amz-Security-Token") != stsToken {
		f.mu.Lock()
		f.badAuth++
		f.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != hsBucket {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && key == "" && q.Get("list-type") == "2":
		type content struct {
			Key  string `xml:"Key"`
			Size int    `xml:"Size"`
		}
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, q.Get("prefix")) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		res := struct {
			XMLName  xml.Name  `xml:"ListBucketResult"`
			KeyCount int       `xml:"KeyCount"`
			Contents []content `xml:"Contents"`
		}{KeyCount: len(keys)}
		for _, k := range keys {
			res.Contents = append(res.Contents, content{k, len(f.objects[k])})
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(res)
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		if sum := sha256.Sum256(body); r.Header.Get("X-Amz-Content-Sha256") != fmt.Sprintf("%x", sum) && r.Header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := f.objects[key]; ok && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		f.objects[key] = body
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// fakeSTS issues the fixed session for a token whose subject is the session name.
func fakeSTS(t *testing.T, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		token := r.PostForm.Get("WebIdentityToken")
		parts := strings.Split(token, ".")
		claims := map[string]any{}
		if len(parts) == 3 {
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			_ = json.Unmarshal(raw, &claims)
		}
		if r.PostForm.Get("Action") != "AssumeRoleWithWebIdentity" || claims["sub"] != r.PostForm.Get("RoleSessionName") || claims["aud"] != "sts.amazonaws.com" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
			return
		}
		calls.Add(1)
		fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`,
			stsKey, stsSecret, stsToken, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
}

func testSigner(t *testing.T, dir string) (*workloadSigner, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	secret := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	c := identityConfig{issuer: "https://issuer.example", audience: "sts.amazonaws.com", role: "arn:aws:iam::123456789012:role/shared", roleDir: filepath.Join(dir, "roles"), sourceDir: filepath.Join(dir, "sources")}
	s, err := c.parseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s, secret
}

// guestPath stands in for the guest bridge (loopback TCP), the vsock hop
// and nibrunnerd's forwarder, which names the jail's deployment.
func guestPath(t *testing.T, dir, agentSock string, deployment *atomic.Value) string {
	t.Helper()
	vsock := filepath.Join(dir, "vsock_51007")
	relay := func(a, b net.Conn) {
		done := make(chan struct{}, 2)
		cp := func(dst, src net.Conn) {
			_, _ = io.Copy(dst, src)
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			done <- struct{}{}
		}
		go cp(a, b)
		go cp(b, a)
		<-done
		<-done
		a.Close()
		b.Close()
	}
	vl, err := net.Listen("unix", vsock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vl.Close() })
	go func() {
		for {
			c, err := vl.Accept()
			if err != nil {
				return
			}
			go func() {
				a, err := net.Dial("unix", agentSock)
				if err != nil {
					c.Close()
					return
				}
				fmt.Fprintf(a, "CONNECT %s %s\n", hsApp, deployment.Load().(string))
				relay(c, a)
			}()
		}
	}()
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tl.Close() })
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			go func() {
				v, err := net.Dial("unix", vsock)
				if err != nil {
					c.Close()
					return
				}
				relay(c, v)
			}()
		}
	}()
	return "http://" + tl.Addr().String()
}

func s3Code(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func s3Status(err error) int {
	var httpErr interface{ HTTPStatusCode() int }
	if errors.As(err, &httpErr) {
		return httpErr.HTTPStatusCode()
	}
	return 0
}

func TestHostServicesEndToEnd(t *testing.T) {
	dir, err := os.MkdirTemp("", "drop-hs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	store := &fakeS3{objects: map[string][]byte{"d-zzzzzzzzzzzzzzz/files/secret": []byte("another app")}}
	s3srv := httptest.NewServer(store)
	defer s3srv.Close()
	var stsCalls atomic.Int32
	stssrv := fakeSTS(t, &stsCalls)
	defer stssrv.Close()
	signer, signingKey := testSigner(t, dir)

	desired := filepath.Join(dir, "desired.json")
	if err := os.WriteFile(desired, []byte(`{"instances":[{"appId":"`+hsApp+`","deploymentId":"`+hsDep+`","desiredState":"on-request","hostServices":{"s3":true}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := hostservices.WriteAppsFile(filepath.Join(state, HostServicesAppsFile), []hostservices.AppEntry{{
		AppID: hsApp, DeploymentID: hsDep, Bucket: hsBucket, Region: "eu-north-1",
		Limits: hostservices.FileLimits{MaxObjectBytes: 1 << 20, StoredBytesQuota: 3 << 20},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := HostServicesConfig{Desired: desired, StateDir: state, Socket: filepath.Join(dir, "run", "host-services.sock"), PeerUID: uint32(os.Getuid()),
		S3Endpoint: s3srv.URL, STSEndpoint: stssrv.URL, STSRegion: "eu-north-1"}
	var logs bytes.Buffer
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHostServices(ctx, cfg, signer, nil, logf) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(cfg.Socket); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st, err := os.Stat(cfg.Socket); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("agent socket: %v %v", st, err)
	}
	var deployment atomic.Value
	deployment.Store(hsDep)
	endpoint := guestPath(t, dir, cfg.Socket, &deployment)
	guest := s3.NewFromConfig(aws.Config{Region: "eu-north-1", Credentials: credentials.NewStaticCredentialsProvider("nibrunner-host-services", "signed-again-by-the-host", ""),
		RetryMaxAttempts: 1}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	bucket := aws.String(hsBucket)

	// Attachment upload as Crew makes it, then the enrollment calls.
	body := bytes.Repeat([]byte("a"), 512<<10)
	sum := sha256.Sum256(body)
	key := fmt.Sprintf("%s/files/%x", hsApp, sum)
	size := int64(len(body))
	put := func(key string, b []byte) error {
		n := int64(len(b))
		h := sha256.Sum256(b)
		_, err := guest.PutObject(ctx, &s3.PutObjectInput{Bucket: bucket, Key: aws.String(key), Body: bytes.NewReader(b), ContentLength: &n,
			IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(h[:]))})
		return err
	}
	if err := put(key, body); err != nil {
		t.Fatalf("attachment put: %v", err)
	}
	if err := put(key, body); s3Status(err) != http.StatusPreconditionFailed {
		t.Fatalf("second put: %v", err)
	}
	got, err := guest.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: aws.String(key)})
	if err != nil {
		t.Fatal(err)
	}
	read, _ := io.ReadAll(got.Body)
	if !bytes.Equal(read, body) {
		t.Fatal("download differs")
	}
	prefix := hsApp + "/crew-drop/"
	if l, err := guest.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket, Prefix: &prefix, MaxKeys: aws.Int32(1)}); err != nil || len(l.Contents) != 0 {
		t.Fatalf("enrollment list: %v", err)
	}
	if _, err := guest.PutObject(ctx, &s3.PutObjectInput{Bucket: bucket, Key: aws.String(prefix + "enrollment"), Body: strings.NewReader("nonce"), IfNoneMatch: aws.String("*")}); err != nil {
		t.Fatalf("enrollment put: %v", err)
	}

	// Prefix escapes.
	if _, err := guest.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: aws.String("d-zzzzzzzzzzzzzzz/files/secret")}); s3Code(err) != "AccessDenied" {
		t.Fatalf("other app's object: %v", err)
	}
	other := "d-zzzzzzzzzzzzzzz/"
	if _, err := guest.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket, Prefix: &other}); s3Code(err) != "AccessDenied" {
		t.Fatalf("other app's listing: %v", err)
	}
	if _, err := guest.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: aws.String(hsApp + "/../d-zzzzzzzzzzzzzzz/files/secret")}); s3Code(err) != "AccessDenied" {
		t.Fatalf("dot segments: %v", err)
	}
	if _, err := guest.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("another-bucket"), Key: aws.String(key)}); s3Code(err) != "AccessDenied" {
		t.Fatalf("another bucket: %v", err)
	}

	// Oversize object and stored-bytes quota.
	if err := put(hsApp+"/files/big", make([]byte, 1<<20+1)); s3Code(err) != "EntityTooLarge" {
		t.Fatalf("oversize: %v", err)
	}
	var quota error
	for i := 0; i < 8 && quota == nil; i++ {
		quota = put(fmt.Sprintf("%s/files/part-%d", hsApp, i), bytes.Repeat([]byte{byte(i)}, 1<<20))
	}
	if s3Code(quota) != "QuotaExceeded" || s3Status(quota) != http.StatusForbidden {
		t.Fatalf("quota: %v", quota)
	}
	var stored int
	store.mu.Lock()
	for k, v := range store.objects {
		if strings.HasPrefix(k, hsApp+"/") {
			stored += len(v)
		}
	}
	store.mu.Unlock()
	if stored > 3<<20 {
		t.Fatalf("stored %d bytes past the quota", stored)
	}

	// A deployment that is no longer current reaches nothing.
	deployment.Store(hsOldDep)
	if _, err := guest.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: aws.String(key)}); s3Status(err) != http.StatusForbidden {
		t.Fatalf("old deployment: %v", err)
	}
	deployment.Store(hsDep)

	// Flood: the default 20 requests/s, burst 200, from 16 connections (below
	// the per-app connection cap, so the token bucket answers).
	var slow, ok atomic.Int32
	var wg sync.WaitGroup
	jobs := make(chan struct{}, 300)
	for i := 0; i < 300; i++ {
		jobs <- struct{}{}
	}
	close(jobs)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				_, err := guest.HeadObject(ctx, &s3.HeadObjectInput{Bucket: bucket, Key: aws.String(key)})
				switch {
				case err == nil:
					ok.Add(1)
				case s3Status(err) == http.StatusServiceUnavailable:
					slow.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if slow.Load() == 0 || ok.Load() < 150 || ok.Load() > 230 {
		t.Fatalf("flood: %d ok, %d slowed down", ok.Load(), slow.Load())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := stsCalls.Load(); n != 1 {
		t.Fatalf("STS exchanges: %d, want one cached session", n)
	}
	if store.badAuth != 0 {
		t.Fatalf("%d requests reached S3 without the app's session", store.badAuth)
	}
	usage := hostUsage(filepath.Join(state, HostServicesUsageFile))
	u := usage[hsDep]
	if u == nil || u.Seq < 1 {
		t.Fatalf("usage: %+v", usage)
	}
	c := u.Counters
	// Rejections: four escapes, the oversize object, the quota and the rate
	// limit (a flood also meets the per-app connection cap, refused before
	// any request is read). The 412 answer comes before the second upload's
	// body is read (100-continue), so it adds no bytes.
	if c.WriteRequests < 4 || c.ReadRequests < 2 || c.ListRequests != 1 || c.RejectedRequests < 6+int64(slow.Load()) ||
		c.BytesIn < size || c.BytesOut < size {
		t.Fatalf("counters %+v", c)
	}
	if usage[hsOldDep] != nil {
		t.Fatal("the old deployment has usage")
	}
	logMu.Lock()
	defer logMu.Unlock()
	leakscan.Require(t, []leakscan.Sentinel{{Label: "STS secret", Value: stsSecret}, {Label: "STS token", Value: stsToken}, {Label: "node key", Value: string(signingKey)}},
		leakscan.Path(state), leakscan.Text("logs", logs.String()))
}

func TestHostServicesRefuseOtherPeers(t *testing.T) {
	dir, _ := os.MkdirTemp("", "drop-hs-")
	t.Cleanup(func() { os.RemoveAll(dir) })
	signer, _ := testSigner(t, dir)
	cfg := HostServicesConfig{Desired: filepath.Join(dir, "desired.json"), StateDir: dir, Socket: filepath.Join(dir, "hs.sock"), PeerUID: uint32(os.Getuid()) + 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runHostServices(ctx, cfg, signer, nil, func(string, ...any) {}) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(cfg.Socket); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, err := net.Dial("unix", cfg.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s %s\nGET / HTTP/1.1\r\nHost: x\r\n\r\n", hsApp, hsDep)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, _ := c.Read(make([]byte, 64)); n != 0 {
		t.Fatal("a connection from another uid was answered")
	}
}

func TestHostServicesConfig(t *testing.T) {
	base := map[string]string{
		"DROP_AGENT_ALLOWED_HOST_SUFFIX": ".example.test", EnvDesired: "/var/lib/nibrunner/desired.json",
		"DROP_IDENTITY_ISSUER": "https://issuer.example", "DROP_IDENTITY_ROLE_ARN": "arn:aws:iam::123456789012:role/shared", "DROP_IDENTITY_KEY_FILE": "/etc/drop/key.pem",
	}
	get := func(extra map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		}
	}
	c, err := LoadHostServicesConfig(get(nil), Overrides{})
	if err != nil || c.Socket != hostservices.DefaultSocketPath || c.PeerUID != 0 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []map[string]string{
		{EnvHostServicesS3: "http://s3.example.com"},
		{EnvHostServicesSTS: "ftp://127.0.0.1"},
		{EnvHostServicesPeer: "root"},
		{EnvHostServicesSocket: "relative.sock"},
		{"DROP_IDENTITY_ISSUER": "", "DROP_IDENTITY_ROLE_ARN": "", "DROP_IDENTITY_KEY_FILE": ""},
	} {
		if _, err := LoadHostServicesConfig(get(bad), Overrides{}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if _, err := hostServicesEnabled(get(map[string]string{EnvHostServices: "yes"})); err == nil {
		t.Error("accepted DROP_AGENT_HOST_SERVICES=yes")
	}
}

func TestPublishSkipsHostS3Apps(t *testing.T) {
	dir := t.TempDir()
	s, _ := testSigner(t, dir)
	plain := `{"instances":[{"appId":"` + hsApp + `","desiredState":"on-request"}]}`
	if err := s.publish([]byte(plain), time.Now(), true); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(s.sourceDir, hsApp, "token")
	if _, err := os.Stat(token); err != nil {
		t.Fatal("no token before the switch")
	}
	hosted := `{"instances":[{"appId":"` + hsApp + `","desiredState":"on-request","hostServices":{"s3":true}}]}`
	if err := s.publish([]byte(hosted), time.Now(), true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{token, filepath.Join(s.sourceDir, hsApp, "role-arn"), filepath.Join(s.roleDir, hsApp, "role-arn")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left for a host S3 app", p)
		}
	}
}

func TestRunGivesManagedCrewTheHostProxy(t *testing.T) {
	e := newEnv(t)
	e.cfg.HostServices = true
	rel := e.release("host-services")
	crew := testApp(appA, 3, rel)
	crew.ManagedCrew, crew.CrewOwnerEmail, crew.CrewReplicaBucket, crew.AWSRegion = true, "owner@example.test", hsBucket, "eu-north-1"
	crew.Limits = &dropapi.HostLimits{RPS: 5, StoredBytesQuota: 1 << 30}
	plain := testApp(appB, 1, rel)
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{crew, plain} })
	if err := os.MkdirAll(e.cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(hostservices.UsageFile{Schema: hostservices.UsageSchema, Deployments: []*hostservices.DeploymentUsage{
		{AppID: appA, DeploymentID: crew.DeploymentID, Seq: 4, Counters: hostservices.Counters{WriteRequests: 7, BytesIn: 99}},
	}})
	if err := os.WriteFile(filepath.Join(e.cfg.StateDir, HostServicesUsageFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e.writeReported([]map[string]any{{"appId": appA, "deploymentId": crew.DeploymentID, "state": "running"}})
	a := New(e.cfg, testSecret, "0.0.0-test", Stores{e.stores.Blobs, e.stores.Secrets}, e.logf)
	a.hostID = testHostID
	a.state = State{Artifacts: map[string]ArtifactInfo{}}
	a.stager = &Stager{Dir: e.cfg.Artifacts, Blobs: e.stores.Blobs, Cache: a.state.Artifacts}
	ctx := context.Background()
	if err := a.client.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Instances []struct {
			AppID        string          `json:"appId"`
			HostServices json.RawMessage `json:"hostServices"`
			Config       struct {
				Command struct {
					Environment map[string]string `json:"environment"`
				} `json:"command"`
			} `json:"config"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(e.readDesired(), &doc); err != nil || len(doc.Instances) != 2 {
		t.Fatalf("desired: %v", err)
	}
	for _, i := range doc.Instances {
		var hs struct{ S3 bool }
		_ = json.Unmarshal(i.HostServices, &hs)
		hosted := hs.S3
		if hosted != (i.AppID == appA) {
			t.Fatalf("%s hostServices %s", i.AppID, i.HostServices)
		}
		if _, ok := i.Config.Command.Environment["AWS_WORKLOAD_IDENTITY"]; ok {
			t.Fatalf("%s has a workload identity", i.AppID)
		}
	}
	var apps hostservices.AppsFile
	b, _ := os.ReadFile(filepath.Join(e.cfg.StateDir, HostServicesAppsFile))
	if err := json.Unmarshal(b, &apps); err != nil || len(apps.Apps) != 1 || apps.Apps[0].DeploymentID != crew.DeploymentID ||
		apps.Apps[0].Bucket != hsBucket || apps.Apps[0].Limits.RPS != 5 || apps.Apps[0].Limits.StoredBytesQuota != 1<<30 {
		t.Fatalf("apps file %s", b)
	}
	report := e.server.lastReport(t)
	var got *dropapi.HostUsage
	for _, r := range report.Apps {
		if r.AppID == appA {
			got = r.Usage
		} else if r.Usage != nil {
			t.Fatal("usage on an app without host services")
		}
	}
	if got == nil || got.Seq != 4 || got.Counters.WriteRequests != 7 || got.DeploymentID != crew.DeploymentID {
		t.Fatalf("report usage %+v", got)
	}
	// Invalid limits refuse the app alone and keep it off the proxy.
	crew.Limits = &dropapi.HostLimits{RPS: -1}
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{crew, plain} })
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(e.cfg.StateDir, HostServicesAppsFile))
	if err := json.Unmarshal(b, &apps); err != nil || len(apps.Apps) != 1 {
		t.Fatalf("kept entry: %s", b)
	}
}
