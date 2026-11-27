// Package privateingress adapts host-only browser cookies to NibRunner's
// per-app JWT check. The runner remains the authorization boundary, including
// on its direct origin. This listener needs no Drop, node or AWS credentials.
package privateingress

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
var jwtPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

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
		// NibRunner validates this app's signature, audience, issuer and TTL.
		// A browser's gateway cookie must never reach the guest executable.
		pr.Out.Header.Del("Cookie")
		for _, cookie := range pr.In.Cookies() {
			if cookie.Name != sessionCookie {
				pr.Out.AddCookie(cookie)
			}
		}
		if c, err := pr.In.Cookie(sessionCookie); err == nil {
			pr.Out.Header.Set(render.PrivateAccessHeader, c.Value)
		}
	}, ModifyResponse: func(resp *http.Response) error {
		// Private app responses can contain files even on traditionally
		// cacheable paths. A guest's public caching preferences must never
		// publish an authenticated response through a shared cache.
		if resp.Request.Header.Get(render.PrivateAccessHeader) != "" {
			for _, name := range []string{"Expires", "Surrogate-Control", "Cloudflare-CDN-Cache-Control", "CDN-Cache-Control"} {
				resp.Header.Del(name)
			}
			resp.Header.Set("Cache-Control", "private, no-store")
			resp.Header.Add("Vary", "Cookie, "+render.PrivateAccessHeader)
		}
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
			session(w, r, host, controlOrigin)
			return
		}
		if _, err := r.Cookie(sessionCookie); err == nil {
			// Sibling customer apps are separate principals even though browsers
			// treat their origins as same-site.
			origin := r.Header.Get("Origin")
			site := r.Header.Get("Sec-Fetch-Site")
			if (origin != "" && origin != "https://"+host) ||
				((site == "same-site" || site == "cross-site") && !(r.Method == "GET" && r.Header.Get("Sec-Fetch-Mode") == "navigate")) {
				http.Error(w, "Cross-origin private app request denied.", http.StatusForbidden)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

func session(w http.ResponseWriter, r *http.Request, host, controlOrigin string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	switch r.Method {
	case http.MethodGet:
		var nonce [24]byte
		_, _ = rand.Read(nonce[:])
		n := base64.RawStdEncoding.EncodeToString(nonce[:])
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+n+"'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Origin is configuration validated above, never a request parameter.
		fmt.Fprintf(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Open private app</title><p id="status">Open this app from your Drop account.</p><script data-cfasync="false" nonce="%s">
const source = window.opener, origin = %q;
if (source) {
  const receive = async event => {
    if (event.source !== source || event.origin !== origin || event.data?.type !== "drop-private-access" || typeof event.data.token !== "string") return;
    window.removeEventListener("message", receive);
    window.opener = null;
    try {
      const response = await fetch(location.pathname, {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({token:event.data.token})});
      if (!response.ok) throw new Error();
      location.replace("/");
    } catch { document.querySelector("#status").textContent = "Could not open the app. Open it again from Drop."; }
  };
  window.addEventListener("message", receive);
  source.postMessage("drop-private-ready", origin);
}</script></html>`, n, controlOrigin)
	case http.MethodPost:
		if r.Header.Get("Origin") != "https://"+host || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "Invalid session handoff.", 403)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		if dec.Decode(&body) != nil || !jwtPattern.MatchString(body.Token) || dec.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "Invalid session handoff.", 400)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: body.Token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed.", 405)
	}
}
