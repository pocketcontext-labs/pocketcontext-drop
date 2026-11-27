package client

import (
	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"os"
)

// hostedFlags are the serve options that exist only with --hosted.
var hostedFlags = []string{"domain", "hosted-http-address", "hosted-proxy-header", "hosted-proxy-networks"}
var hostedBools = []string{"hosted-init", "hosted-restore", "mcp", "headless"}

// hostedServeOptions validates the serve options and returns the hosted
// options core needs, or nil for plain local serving. Drop serves people only
// in hosted mode (several accounts, email sign-in, remote MCP) behind a
// TLS-terminating proxy; plain `serve` is the local single-account server for
// developing the reader, on loopback only. Native public HTTPS is not offered.
// This is the one file that names core's hosted proxy options.
func hostedServeOptions(a *args) (*appserver.ServeOptions, error) {
	if !a.bools["hosted"] {
		for _, name := range hostedFlags {
			if a.has(name) {
				return nil, fail(2, "--%s requires --hosted", name)
			}
		}
		for _, name := range hostedBools {
			if a.bools[name] {
				return nil, fail(2, "--%s requires --hosted", name)
			}
		}
		if a.has("port") {
			return nil, fail(2, "unknown option --port")
		}
		return nil, nil
	}
	if !a.bools["headless"] || !a.has("domain") || !a.bools["mcp"] {
		return nil, fail(2, "--hosted requires --headless --domain HOST --mcp")
	}
	if a.has("ui-dev") || a.bools["tcp-only"] || a.has("port") {
		return nil, fail(2, "--hosted cannot be combined with --ui-dev, --tcp-only or --port")
	}
	if a.bools["hosted-init"] && a.bools["hosted-restore"] {
		return nil, fail(2, "--hosted-restore excludes --hosted-init")
	}
	if err := appserver.ValidateHTTPSDomain(a.flags["domain"]); err != nil {
		return nil, fail(2, "%v", err)
	}
	opts := &appserver.ServeOptions{
		TakeOver: a.bools["take-over"], Headless: true, HTTPSDomain: a.flags["domain"], MCP: true, MCPHandler: newMCPHandler,
		Hosted: true, HostedInit: a.bools["hosted-init"], HostedRestore: a.bools["hosted-restore"],
	}
	if a.has("hosted-http-address") {
		if err := appserver.ValidateHostedHTTPAddress(a.flags["hosted-http-address"]); err != nil {
			return nil, fail(2, "--hosted-http-address: %v", err)
		}
		opts.HostedHTTPAddress = a.flags["hosted-http-address"]
	}
	if a.has("hosted-proxy-header") || a.has("hosted-proxy-networks") {
		proxy, err := appserver.ParseHostedProxy(a.flags["hosted-proxy-header"], a.flags["hosted-proxy-networks"])
		if err != nil {
			return nil, fail(2, "%v", err)
		}
		if proxy == nil {
			return nil, fail(2, "a hosted proxy needs both --hosted-proxy-header and --hosted-proxy-networks")
		}
		opts.HostedProxy = proxy
	}
	if upstream := os.Getenv("DROP_HOSTED_VITE_URL"); upstream != "" {
		if err := appserver.ValidateDevHTTPAddress(opts.HostedHTTPAddress); err != nil {
			return nil, fail(2, "DROP_HOSTED_VITE_URL requires a loopback hosted HTTP listener: %v", err)
		}
		opts.PublicUIDev = true
		opts.UIDev = upstream
	}
	return opts, nil
}

// ensureLocalTCP starts the server if needed and makes sure its loopback port is
// open. A running hosted service starts with only its private socket, and the
// local client and reader use the loopback port.
func ensureLocalTCP(p *appserver.Project, opts appserver.StartOptions) error {
	if err := appserver.EnsureRunning(p, opts); err != nil {
		return err
	}
	return appserver.EnableTCP(p)
}
