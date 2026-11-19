package awsx

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
)

// TestLiveAWS runs the conformance suites against real S3 and SSM. It is
// skipped unless DROP_LIVE_AWS=1, and then needs DROP_AWS_REGION and
// DROP_S3_BUCKET (DROP_SSM_PREFIX defaults to /drop) and credentials from the
// default chain (on walter-aws, the instance profile). It leaves a few small
// random blobs under releases/ (the policy grants no delete) and removes its
// secrets.
func TestLiveAWS(t *testing.T) {
	if os.Getenv("DROP_LIVE_AWS") != "1" {
		t.Skip("live AWS test; set DROP_LIVE_AWS=1 to run it")
	}
	cfg, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := OpenAWS(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [15]byte
	_, _ = rand.Read(b[:])
	id := make([]byte, 15)
	for i := range b {
		id[i] = alphabet[int(b[i])%len(alphabet)]
	}
	param, err := SecretParameter(cfg.SSMPrefix, "d-"+string(id), "X")
	if err != nil {
		t.Fatal(err)
	}
	base := param[:len(param)-2]
	t.Run("blobs", func(t *testing.T) { runBlobConformance(t, stores.Blobs, blobSuiteOptions{}) })
	t.Run("secrets", func(t *testing.T) { runSecretConformance(t, stores.Secrets, base, secretSuiteOptions{}) })
}
