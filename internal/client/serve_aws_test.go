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
