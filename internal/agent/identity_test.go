package agent

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	now := time.Unix(12345, 0)
	first, err := s.token(id, now)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := c.parseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(125 * time.Second)
	fresh, err := restarted.token(id, later)
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
	for _, base := range []string{c.roleDir, c.sourceDir} {
		if _, err := os.Stat(base); !os.IsNotExist(err) {
			t.Fatal("in-memory signing created guest identity files")
		}
		for _, appID := range []string{id, "foreign"} {
			dir := filepath.Join(base, appID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"token", "role-arn", "unrelated"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for range 2 {
		if err := c.clearGuestFiles(); err != nil {
			t.Fatal(err)
		}
	}
	for _, base := range []string{c.roleDir, c.sourceDir} {
		for _, name := range []string{"token", "role-arn"} {
			if _, err := os.Stat(filepath.Join(base, id, name)); !os.IsNotExist(err) {
				t.Fatal("legacy credential remains")
			}
			if _, err := os.Stat(filepath.Join(base, "foreign", name)); err != nil {
				t.Fatal("foreign credentials changed")
			}
		}
		if _, err := os.Stat(filepath.Join(base, id, "unrelated")); err != nil {
			t.Fatal("unrelated file changed")
		}
	}
}
