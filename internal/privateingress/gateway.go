// Package privateingress proxies app hostnames to the node runner and strips
// credentials reserved by the former Drop session gateway.
package privateingress

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

const sessionPath = "/.well-known/drop/app-session"
const sessionCookie = "__Host-drop-runtime"

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func loopbackAddress(address string) bool {
	ap, err := netip.ParseAddrPort(address)
	return err == nil && ap.Port() != 0 && ap.Addr().IsLoopback()
}

// Run accepts plain HTTP exclusively on loopback behind TLS termination.
func Run(listen, upstream, domain, controlOrigin string) error {
	if !loopbackAddress(listen) || !loopbackAddress(upstream) || listen == upstream {
		return errors.New("provide distinct loopback IP:port values for listen and upstream")
	}
	handler, err := New(upstream, domain, controlOrigin)
	if err != nil {
		return err
	}
	server := http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 17}
	return server.ListenAndServe()
}

func New(upstream, domain, controlOrigin string) (http.Handler, error) {
	origin, err := url.Parse(controlOrigin)
	if err != nil || origin.Scheme != "https" || !hostnamePattern.MatchString(origin.Host) || controlOrigin != "https://"+origin.Host || !hostnamePattern.MatchString(domain) || !loopbackAddress(upstream) {
		return nil, errors.New("require a canonical HTTPS Drop origin, app domain and fixed loopback upstream")
	}
	if origin.Host == domain || strings.HasSuffix(origin.Host, "."+domain) {
		return nil, errors.New("the Drop login origin must be outside the apps' domain")
	}
	target := &url.URL{Scheme: "http", Host: upstream}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{Transport: transport, ErrorLog: log.New(io.Discard, "", 0), Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = pr.In.Host
		pr.SetXForwarded()
		pr.Out.Header.Del(render.PrivateAccessHeader)
		// A browser's gateway cookie must never reach the guest executable.
		pr.Out.Header.Del("Cookie")
		for _, cookie := range pr.In.Cookies() {
			if cookie.Name != sessionCookie {
				pr.Out.AddCookie(cookie)
			}
		}
	}, ModifyResponse: func(resp *http.Response) error {
		// A guest may manage its own cookies, never the gateway's cookie.
		lines := resp.Header.Values("Set-Cookie")
		resp.Header.Del("Set-Cookie")
		for _, line := range lines {
			c, err := http.ParseSetCookie(line)
			if err == nil && !strings.EqualFold(c.Name, sessionCookie) {
				resp.Header.Add("Set-Cookie", line)
			}
		}
		return nil
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		prefix, ok := strings.CutSuffix(host, "."+domain)
		if !ok || !hostnamePattern.MatchString(host) || strings.Contains(prefix, ".") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == sessionPath {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/", http.StatusSeeOther)
			} else {
				http.Error(w, "Session handoff retired.", http.StatusGone)
			}
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}
