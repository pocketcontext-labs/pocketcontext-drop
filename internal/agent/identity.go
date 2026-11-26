package agent

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

type identityConfig struct{ issuer, audience, role, keyFile, roleDir, sourceDir string }

type workloadSigner struct {
	identityConfig
	key *rsa.PrivateKey
	kid string
	mu  sync.Mutex
}

var rolePattern = regexp.MustCompile("^arn:aws:iam::[0-9]{12}:role/[a-zA-Z0-9/+=,.@_-]+$")

func (c identityConfig) load() (*workloadSigner, error) {
	if c.issuer == "" && c.role == "" && c.keyFile == "" {
		return nil, nil
	}
	u, err := url.Parse(c.issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.audience == "" || !rolePattern.MatchString(c.role) || !filepath.IsAbs(c.keyFile) {
		return nil, errors.New("invalid workload issuer, audience, role or key path")
	}
	f, err := os.OpenFile(c.keyFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != 0 {
		return nil, errors.New("workload signing key must be a root-owned private regular file")
	}
	if st.Size() > 32768 {
		return nil, errors.New("invalid workload signing key size")
	}
	data := make([]byte, st.Size())

	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}
	return c.parseKey(data)
}

func (c identityConfig) parseKey(data []byte) (*workloadSigner, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid workload signing PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid PKCS8 workload signing key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 {
		return nil, errors.New("workload signing requires RSA of at least 2048 bits")
	}
	enc := base64.RawURLEncoding.EncodeToString
	jwk, _ := json.Marshal(map[string]string{"kty": "RSA", "e": enc(big.NewInt(int64(key.E)).Bytes()), "n": enc(key.N.Bytes())})
	thumb := sha256.Sum256(jwk)
	return &workloadSigner{identityConfig: c, key: key, kid: enc(thumb[:])}, nil
}

func (s *workloadSigner) token(id string, now time.Time) ([]byte, error) {
	enc := base64.RawURLEncoding.EncodeToString
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": s.kid, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"sub": id, "iss": s.issuer, "aud": s.audience, "iat": now.Unix(), "nbf": now.Unix() - 5, "exp": now.Unix() + 120, "https://aws.amazon.com/tags": map[string]any{"principal_tags": map[string][]string{"app_id": {id}}}})
	signed := enc(hdr) + "." + enc(claims)
	h := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, h[:])
	if err != nil {
		return nil, err
	}
	return []byte(signed + "." + enc(sig)), nil
}

// clearGuestFiles removes credentials created by older Drop agents. The
// host proxy signs in memory. No workload has a guest token publisher.
func (c identityConfig) clearGuestFiles() error {
	for _, base := range []string{c.roleDir, c.sourceDir} {
		if base == "" {
			continue
		}
		err := clearGuestIdentityDirectory(base)
		if err != nil {
			return err
		}
	}
	return nil
}

func clearGuestIdentityDirectory(base string) error {
	root, err := os.OpenRoot(base)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !appspec.AppIDPattern.MatchString(entry.Name()) {
			continue
		}
		for _, name := range []string{"token", "role-arn"} {
			if err := root.Remove(filepath.Join(entry.Name(), name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
