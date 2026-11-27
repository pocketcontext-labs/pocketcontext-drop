package dropserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() { register("beta.import_url", importReleaseURL) }

var importSlots = make(chan struct{}, 2)

// Use public IPv4 unicast only. Pin each connection to the address checked
// here, including connections made after redirects.
var specialUse = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func publicReleaseIP(ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, block := range specialUse {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func releaseURLAllowed(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && (u.Port() == "" || u.Port() == "443")
}

func dialRelease(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errors.New("invalid release address")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicReleaseIP(ip.Unmap()) {
			return nil, errors.New("release address is not public")
		}
	}
	for _, ip := range ips {
		conn, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("release address unavailable")
}

func importReleaseURL(c *Ctx, in *dropapi.ImportURLInput) (any, error) {
	u, err := url.Parse(in.URL)
	if err != nil || !releaseURLAllowed(u) {
		return nil, Invalid("url", "Enter a public HTTPS release URL.")
	}
	if in.SHA256 != "" && !awsx.ValidSHA256(in.SHA256) {
		return nil, Invalid("sha256", "Enter a lowercase SHA-256 digest.")
	}
	select {
	case importSlots <- struct{}{}:
		defer func() { <-importSlots }()
	default:
		return nil, Conflict("import_busy", "Other imports are running. Try again shortly.")
	}
	transport := &http.Transport{DialContext: dialRelease, DisableCompression: true, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !releaseURLAllowed(req.URL) {
			return errors.New("invalid release redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, Invalid("url", "Invalid release URL.")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, Invalid("url", "Could not download the release from a public HTTPS address.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, Invalid("url", "Release download did not succeed.")
	}
	const limit = dropapi.MaxReleaseBytes
	if resp.ContentLength > limit {
		return nil, Invalid("url", "Release exceeds the 90 MiB download limit.")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) == 0 || len(data) > limit {
		return nil, Invalid("url", "Release is empty, incomplete, or exceeds the download limit.")
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	if in.SHA256 != "" && digest != in.SHA256 {
		return nil, Invalid("sha256", "Downloaded release checksum does not match.")
	}
	return map[string]any{"sha256": digest, "content_base64": data}, nil
}
