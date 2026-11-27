package dropserver

import (
	"net/netip"
	"net/url"
	"testing"
)

func TestReleaseDownloadDestinations(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "10.2.3.4", "100.64.0.1", "192.168.0.1", "198.18.0.1", "224.0.0.1", "::1", "2001:db8::1"} {
		if publicReleaseIP(netip.MustParseAddr(host)) {
			t.Errorf("allowed %s", host)
		}
	}
	if !publicReleaseIP(netip.MustParseAddr("1.1.1.1")) {
		t.Error("public IPv4 refused")
	}
	for _, raw := range []string{"http://example.org/object", "https://name:password@example.org/object", "https://example.org:9090/", "file:///tmp/test"} {
		u, err := url.Parse(raw)
		if err == nil && releaseURLAllowed(u) {
			t.Errorf("allowed %s", raw)
		}
	}
}
