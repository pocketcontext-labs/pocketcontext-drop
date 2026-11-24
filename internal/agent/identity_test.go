package agent

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/leakscan"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkloadIdentityOffline(t *testing.T) {
	dir := t.TempDir()
	c := identityConfig{issuer: "https://issuer.example", audience: "sts.amazonaws.com", role: "arn:aws:iam::123456789012:role/shared", roleDir: filepath.Join(dir, "roles"), sourceDir: filepath.Join(dir, "sources")}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	secret := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	s, err := c.parseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	const id = "d-0123456789abcde"
	doc := []byte(`{"instances":[{"appId":"` + id + `","desiredState":"on-request"},{"appId":"d-222222222222222","desiredState":"stopped"}]}`)
	now := time.Unix(12345, 0)
	tokenPath := filepath.Join(c.sourceDir, id, "token")
	if err := s.publish(doc, now, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatal("minted before desired commit")
	}
	if err := s.publish(doc, now, true); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.sourceDir, "d-222222222222222")); !os.IsNotExist(err) {
		t.Fatal("minted for stopped app")
	}
	// Reconstruct the signer as after a process restart, without a server.
	restarted, err := c.parseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(125 * time.Second)
	if err := restarted.publish(doc, later, true); err != nil {
		t.Fatal(err)
	}
	fresh, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, fresh) {
		t.Fatal("expired token was reused")
	}
	parts := strings.Split(string(fresh), ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Sub, Iss, Aud string
		Exp           int64
		Tags          struct {
			Principal map[string][]string `json:"principal_tags"`
		} `json:"https://aws.amazon.com/tags"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Sub != id || claims.Iss != c.issuer || claims.Aud != c.audience || claims.Exp != later.Unix()+120 || strings.Join(claims.Tags.Principal["app_id"], ",") != id {
		t.Fatal("invalid workload claims")
	}
	for _, path := range []string{filepath.Join(c.roleDir, id, "role-arn"), filepath.Join(c.sourceDir, id, "role-arn"), tokenPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("identity file permissions")
		}
		b, err := os.ReadFile(path)
		if err != nil || bytes.Contains(b, der) || (path != tokenPath && string(b) != c.role) {
			t.Fatal("unexpected identity file contents")
		}
	}
	if err := s.publish([]byte(`{"instances":[{"appId":"../outside","desiredState":"running"}]}`), later, true); err == nil {
		t.Fatal("accepted invalid identity")
	}
	// Exercise real desired staging and scan every non-credential output.
	e := newEnv(t)
	rel := e.release("identity")
	e.server.setDesired(func(d *dropapi.Desired) { d.Apps = []dropapi.DesiredApp{testApp(appA, 1, rel)} })
	a := New(e.cfg, testSecret, "0.0.0-test", Stores{e.stores.Blobs, e.stores.Secrets}, e.logf)
	a.hostID = testHostID
	a.state = State{Artifacts: map[string]ArtifactInfo{}}
	a.stager = &Stager{Dir: e.cfg.Artifacts, Blobs: e.stores.Blobs, Cache: a.state.Artifacts}
	a.signer = restarted
	ctx := context.Background()
	if err := a.client.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.cycle(ctx); err != nil {
		t.Fatal(err)
	}
	tok, err := os.ReadFile(filepath.Join(c.sourceDir, appA, "token"))
	if err != nil {
		t.Fatal(err)
	}
	leakscan.Require(t, []leakscan.Sentinel{{Label: "node key", Value: string(secret)}, {Label: "assertion", Value: string(tok)}}, leakscan.Path(e.cfg.StatePath()), leakscan.Path(e.cfg.Desired), leakscan.Text("logs", e.logs()), leakscan.Text("reports", e.server.allReports()))

}
