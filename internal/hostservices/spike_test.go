//go:build hostservices_spike

package hostservices

// Offline spike (Phase 2 host S3): SPIKE_VERSITYGW names a local S3-compatible
// server (versitygw posix) and SPIKE_LITESTREAM the Litestream 0.5.18 binary.
//   go test -tags hostservices_spike -run TestSpike -v ./internal/hostservices/

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

const (
	spikeApp    = "d-000000000000001"
	spikeDep    = "d-000000000000001-dep-1"
	spikeBucket = "drop-replica"
	spikeAK     = "SPIKEROOTKEY"
	spikeSK     = "spike-root-secret-key-1234567890"
)

type staticCreds struct{}

func (staticCreds) Credentials(context.Context, string) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: spikeAK, SecretAccessKey: spikeSK, Source: "spike"}, nil
}

type logTransport struct {
	mu   sync.Mutex
	seen []string
}

func (l *logTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	line := fmt.Sprintf("%s %s?%s sha=%q len=%d enc=%q inm=%q ck=%q md5=%q", r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		r.Header.Get("X-Amz-Content-Sha256"), r.ContentLength, r.Header.Get("Content-Encoding"), r.Header.Get("If-None-Match"),
		r.Header.Get("X-Amz-Checksum-Sha256")+r.Header.Get("X-Amz-Checksum-Crc32")+r.Header.Get("X-Amz-Checksum-Crc64nvme"), r.Header.Get("Content-Md5"))
	if err == nil {
		line += fmt.Sprintf(" -> %d", resp.StatusCode)
	}
	l.mu.Lock()
	l.seen = append(l.seen, line)
	l.mu.Unlock()
	return resp, err
}

func (l *logTransport) dump(t *testing.T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.seen {
		t.Log(s)
	}
	l.seen = nil
}

func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); closeWrite(a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); closeWrite(b); done <- struct{}{} }()
	<-done
	<-done
	a.Close()
	b.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// chain starts TCP 127.0.0.1:port → unix vsock stand-in → forwarder stand-in
// (header line) → agent socket → proxy.
func chain(t *testing.T, dir string, port int, upstream string, tr http.RoundTripper, limits Limits) {
	agentSock := filepath.Join(dir, "agent.sock")
	srv := &Server{Socket: agentSock, PeerUID: uint32(os.Getuid()), Lookup: func(app, dep string) (App, bool) {
		return App{AppID: spikeApp, DeploymentID: spikeDep, Bucket: spikeBucket, Region: "us-east-1", Limits: limits}, app == spikeApp && dep == spikeDep
	}, Proxy: &Proxy{Budget: func(context.Context, dropapi.StorageBudget) error { return nil }, Upstream: Upstream{Endpoint: upstream, Client: &http.Client{Transport: tr}}, Creds: staticCreds{}}}
	l, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, l)
	vsock := filepath.Join(dir, "vsock_51007")
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
				fmt.Fprintf(a, "CONNECT %s %s\n", spikeApp, spikeDep)
				relay(c, a)
			}()
		}
	}()
	tl, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
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
}

func startGateway(t *testing.T, dir string) string {
	bin := os.Getenv("SPIKE_VERSITYGW")
	if bin == "" {
		t.Skip("SPIKE_VERSITYGW unset")
	}
	root := filepath.Join(dir, "gw")
	os.MkdirAll(root, 0o700)
	cmd := exec.Command(bin, "--access", spikeAK, "--secret", spikeSK, "--port", "127.0.0.1:19100", "posix", root)
	logf, _ := os.Create(filepath.Join(dir, "gw.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:19100"); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	direct := client("http://127.0.0.1:19100", spikeAK, spikeSK)
	if _, err := direct.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(spikeBucket)}); err != nil {
		t.Fatal(err)
	}
	return "http://127.0.0.1:19100"
}

func client(endpoint, ak, sk string, opts ...func(*s3.Options)) *s3.Client {
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(ak, sk, "")}
	return s3.NewFromConfig(cfg, append([]func(*s3.Options){func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	}}, opts...)...)
}

func code(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestSpikeSDKClients(t *testing.T) {
	dir := t.TempDir()
	up := startGateway(t, dir)
	tr := &logTransport{}
	chain(t, dir, 19000, up, tr, Limits{MaxObjectBytes: 1 << 30})
	ctx := context.Background()
	for _, mode := range []string{"default", "when_required"} {
		t.Run(mode, func(t *testing.T) {
			var opts []func(*s3.Options)
			if mode == "when_required" {
				opts = append(opts, func(o *s3.Options) {
					o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
					o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
				})
			}
			c := client("http://127.0.0.1:19000", "drop-host", "drop-host-services", opts...)
			body := make([]byte, 3<<20)
			rand.Read(body)
			sum := sha256.Sum256(body)
			key := fmt.Sprintf("%s/files/%x-%s", spikeApp, sum, mode)
			size := int64(len(body))
			_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(spikeBucket), Key: &key, Body: bytes.NewReader(body), ContentLength: &size,
				IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:]))})
			t.Logf("attachment put: %v", code(err))
			_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(spikeBucket), Key: &key, Body: bytes.NewReader(body), ContentLength: &size,
				IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:]))})
			var httpErr interface{ HTTPStatusCode() int }
			status := 0
			if errors.As(err, &httpErr) {
				status = httpErr.HTTPStatusCode()
			}
			t.Logf("attachment second put: %v status %d", code(err), status)
			got, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(spikeBucket), Key: &key})
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			b, _ := io.ReadAll(got.Body)
			t.Logf("attachment get equal=%v", bytes.Equal(b, body))
			// Unseekable body (streaming payload).
			ukey := key + "-stream"
			_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(spikeBucket), Key: &ukey, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: &size})
			t.Logf("unseekable put: %v", code(err))
			prefix := spikeApp + "/crew-drop-" + mode + "/"
			ekey := prefix + "enrollment"
			l, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(spikeBucket), Prefix: &prefix, MaxKeys: aws.Int32(1)})
			t.Logf("enrollment list: %v n=%d", code(err), func() int {
				if l == nil {
					return -1
				}
				return len(l.Contents)
			}())
			_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(spikeBucket), Key: &ekey, Body: strings.NewReader("nonce"), IfNoneMatch: aws.String("*")})
			t.Logf("enrollment put: %v", code(err))
			_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(spikeBucket), Key: &ekey, Body: strings.NewReader("nonce"), IfNoneMatch: aws.String("*")})
			t.Logf("enrollment second put: %v", code(err))
			o, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(spikeBucket), Key: &ekey})
			if err == nil {
				b, _ := io.ReadAll(o.Body)
				t.Logf("enrollment get: %q", b)
			} else {
				t.Logf("enrollment get: %v", code(err))
			}
			other := "d-000000000000002/files/x"
			_, err = c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(spikeBucket), Key: &other})
			t.Logf("escape get: %v", code(err))
			tr.dump(t)
		})
	}
}

func litestreamConfig(t *testing.T, path, live, endpoint, ak, sk string, extra string) {
	cfg := fmt.Sprintf(`dbs:
  - path: %q
    replica:
      url: "s3://%s/%s/crew-drop/sqlite"
      endpoint: %q
      region: us-east-1
      force-path-style: true
      access-key-id: %q
      secret-access-key: %q
      sync-interval: 1s
%s`, live, spikeBucket, spikeApp, endpoint, ak, sk, extra)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "AWS_EC2_METADATA_DISABLED=true")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func makeDB(t *testing.T, path string, rows, blob int) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, b BLOB)"); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	buf := make([]byte, blob)
	for i := 0; i < rows; i++ {
		rand.Read(buf[:blob/2]) // half random, half zero: roughly 2:1 compressible
		if _, err := tx.Exec("INSERT INTO t (b) VALUES (?)", buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, path string) int {
	db, err := sql.Open("sqlite3", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// replicate runs `litestream replicate` until the replica holds the
// database, then stops it.
func replicate(t *testing.T, bin, cfg string, wait time.Duration) {
	cmd := exec.Command(bin, "replicate", "-config", cfg)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "AWS_EC2_METADATA_DISABLED=true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(wait)
	cmd.Process.Signal(os.Interrupt)
	cmd.Wait()
	sc := bufio.NewScanner(&out)
	n := 0
	for sc.Scan() {
		if n < 40 && (strings.Contains(sc.Text(), "level=ERROR") || strings.Contains(sc.Text(), "level=WARN")) {
			t.Log(sc.Text())
			n++
		}
	}
}

func TestSpikeLitestream(t *testing.T) {
	bin := os.Getenv("SPIKE_LITESTREAM")
	if bin == "" {
		t.Skip("SPIKE_LITESTREAM unset")
	}
	dir := t.TempDir()
	up := startGateway(t, dir)
	tr := &logTransport{}
	chain(t, dir, 19000, up, tr, Limits{MaxObjectBytes: 1 << 30})
	live := filepath.Join(dir, "live.db")
	makeDB(t, live, 2000, 4096)
	cfg := filepath.Join(dir, "ls.yml")
	litestreamConfig(t, cfg, live, "http://127.0.0.1:19000", "drop-host", "drop-host-services", "")
	replicate(t, bin, cfg, 6*time.Second)
	makeDB(t, live, 500, 4096)
	replicate(t, bin, cfg, 4*time.Second)
	out := filepath.Join(dir, "restored.db")
	res, err := run(t, bin, "restore", "-config", cfg, "-integrity-check", "full", "-o", out, live)
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, res)
	}
	t.Logf("restored rows %d of %d", count(t, out), count(t, live))
	ops := map[string]int{}
	for _, s := range tr.seen {
		f := strings.Fields(s)
		ops[f[0]+" "+strings.SplitN(f[1], "?", 2)[1]]++
	}
	t.Logf("upstream requests by method?query: %v", ops)
	tr.dump(t)
}

func TestSpikeThroughput(t *testing.T) {
	bin := os.Getenv("SPIKE_LITESTREAM")
	if bin == "" {
		t.Skip("SPIKE_LITESTREAM unset")
	}
	dir := t.TempDir()
	up := startGateway(t, dir)
	chain(t, dir, 19000, up, http.DefaultTransport, Limits{MaxObjectBytes: 1 << 30})
	live := filepath.Join(dir, "live.db")
	makeDB(t, live, 50000, 3500)
	st, _ := os.Stat(live)
	t.Logf("database %d MiB", st.Size()>>20)
	direct := filepath.Join(dir, "direct.yml")
	litestreamConfig(t, direct, live, up, spikeAK, spikeSK, "")
	proxied := filepath.Join(dir, "proxied.yml")
	litestreamConfig(t, proxied, live, "http://127.0.0.1:19000", "drop-host", "drop-host-services", "")
	start := time.Now()
	replicate(t, bin, proxied, 40*time.Second)
	t.Logf("replicate through relays: stopped after %s", time.Since(start).Round(time.Millisecond))
	var stored int64
	filepath.Walk(filepath.Join(dir, "gw"), func(p string, i os.FileInfo, err error) error {
		if err == nil && i.Mode().IsRegular() {
			stored += i.Size()
			if i.Size() > 50<<20 {
				t.Logf("large object %s %d MiB", strings.TrimPrefix(p, dir), i.Size()>>20)
			}
		}
		return nil
	})
	t.Logf("replica holds %d MiB", stored>>20)
	for round := 0; round < 3; round++ {
		for _, c := range []struct{ name, cfg string }{{"direct", direct}, {"relays", proxied}} {
			out := filepath.Join(dir, fmt.Sprintf("%s-%d.db", c.name, round))
			start := time.Now()
			res, err := run(t, bin, "restore", "-config", c.cfg, "-o", out, live)
			if err != nil {
				t.Fatalf("%s restore: %v\n%s", c.name, err, res)
			}
			d := time.Since(start)
			st, _ := os.Stat(out)
			t.Logf("round %d %s restore: %s, %d MiB, %.0f MiB/s", round, c.name, d.Round(time.Millisecond), st.Size()>>20, float64(st.Size())/(1<<20)/d.Seconds())
		}
	}
}
