package client

import (
	"strings"
	"testing"
)

func TestServeFakeAWSNeedsLoopback(t *testing.T) {
	dir := t.TempDir()
	a, err := parseArgs([]string{"--fake-aws", dir}, []string{"fake-aws"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, listen := range []string{":80", "0.0.0.0:8100", "10.0.0.5:8100"} {
		if _, err := openServeStores(a, true, listen); err == nil || !strings.Contains(err.Error(), "--fake-aws") {
			t.Fatalf("listen %q: want a --fake-aws refusal, got %v", listen, err)
		}
	}
	for _, listen := range []string{"127.0.0.1:0", "127.0.0.1:8100"} {
		stores, err := openServeStores(a, listen != "127.0.0.1:0", listen)
		if err != nil || stores == nil || !stores.Fake {
			t.Fatalf("listen %q: want fake stores, got %v, %v", listen, stores, err)
		}
	}
	empty, err := parseArgs([]string{"--fake-aws", ""}, []string{"fake-aws"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openServeStores(empty, false, "127.0.0.1:0"); err == nil {
		t.Fatal("an empty --fake-aws directory must be refused")
	}
	none, _ := parseArgs(nil, []string{"fake-aws"}, nil)
	if stores, err := openServeStores(none, false, "127.0.0.1:0"); err != nil || stores != nil {
		t.Fatalf("plain serve without --fake-aws opens nothing, got %v, %v", stores, err)
	}
}

// TestServeServicesHandOverTheWriteSide: serve gives the server the secret
// store as a write-only interface, and checks DROP_APP_DOMAIN.
func TestServeServicesHandOverTheWriteSide(t *testing.T) {
	a, _ := parseArgs([]string{"--fake-aws", t.TempDir()}, []string{"fake-aws"}, nil)
	env := map[string]string{EnvAppDomain: "apps.example.test"}
	s, err := serveServices(a, false, "127.0.0.1:0", func(k string) string { return env[k] })
	if err != nil || s.Secrets == nil || s.SSMPrefix != "/drop" || s.AppDomain != "apps.example.test" {
		t.Fatalf("%+v %v", s, err)
	}
	env[EnvAppDomain] = "Not A Domain"
	if _, err := serveServices(a, false, "127.0.0.1:0", func(k string) string { return env[k] }); err == nil {
		t.Fatal("a malformed DROP_APP_DOMAIN was accepted")
	}
	none, _ := parseArgs(nil, []string{"fake-aws"}, nil)
	if s, err := serveServices(none, false, "127.0.0.1:0", func(string) string { return "" }); err != nil || s.Secrets != nil {
		t.Fatalf("plain serve has no secret store: %+v %v", s, err)
	}
}

func TestBetaNodePoolsAreDisjoint(t *testing.T) {
	env := map[string]string{"DROP_BETA_NODES": "public-node", "DROP_BETA_OWNER_NODES": "private-node"}
	read := func(key string) string { return env[key] }
	s, err := serveServices(&args{}, false, "127.0.0.1:0", read)
	if err != nil || len(s.BetaOwnerNodes) != 1 || s.BetaOwnerNodes[0] != "private-node" || len(s.BetaNodes) != 1 || s.BetaNodes[0] != "public-node" {
		t.Fatalf("unexpected node pools: %v, %v, %v", s.BetaOwnerNodes, s.BetaNodes, err)
	}
	env["DROP_BETA_NODES"] = "private-node"
	if _, err := serveServices(&args{}, false, "127.0.0.1:0", read); err == nil {
		t.Fatal("accepted the same node in both pools")
	}
}
