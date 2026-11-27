package privateingress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

func TestBrowserHandoffAndCredentialBoundaries(t *testing.T) {
	const host = "test.apps.example.test"
	const control = "https://drop.example.test"
	const valid = "header.claims.signature"
	calls := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host != host {
			t.Errorf("wrong guest host %q", r.Host)
		}
		if _, err := r.Cookie(sessionCookie); err != http.ErrNoCookie {
			t.Error("browser gateway cookie reached the guest")
		}
		if r.Header.Get(render.PrivateAccessHeader) != valid {
			http.Error(w, "Denied", 403)
			return
		}
		if c, err := r.Cookie("guest"); err != nil || c.Value != "yes" {
			t.Error("guest session cookie missing")
		}
		w.Header().Set("Cache-Control", "public, immutable")
		w.Header().Set("Cloudflare-CDN-Cache-Control", "public")
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "guest-chosen"})
		http.SetCookie(w, &http.Cookie{Name: "ordinary", Value: "session"})
		_, _ = io.WriteString(w, "private content")
	}))
	defer origin.Close()
	addr, _ := url.Parse(origin.URL)
	g, err := New(addr.Host, "apps.example.test", control)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, origin, body, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w
	}
	get := request("GET", sessionPath, "", "", "")
	if get.Code != 200 || !strings.Contains(get.Body.String(), control) || !strings.Contains(get.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || calls != 0 {
		t.Fatal("bootstrap must be served locally and bounded to the configured origin")
	}
	for _, origin := range []string{"", "https://other.apps.example.test", control} {
		if w := request("POST", sessionPath, origin, `{"token":"`+valid+`"}`, ""); w.Code != 403 || len(w.Result().Cookies()) != 0 {
			t.Fatal("unauthorized cookie handoff", origin, w.Code)
		}
	}
	if w := request("GET", "/", "", "", ""); w.Code != 403 {
		t.Fatal("unauthenticated request accepted")
	}
	w := request("POST", sessionPath, "https://"+host, `{"token":"`+valid+`"}`, "")
	cookies := w.Result().Cookies()
	if w.Code != 204 || len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].Domain != "" || cookies[0].Path != "/" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe session cookie %+v", cookies)
	}
	cookie := sessionCookie + "=" + valid + "; guest=yes"
	if w := request("POST", "/", "https://other.apps.example.test", "", cookie); w.Code != 403 || calls != 1 {
		t.Fatal("cross-origin guest request with runtime cookie")
	}
	ok := request("GET", "/", "", "", cookie)
	if ok.Header().Get("Cache-Control") != "private, no-store" || ok.Header().Get("Cloudflare-CDN-Cache-Control") != "" {
		t.Fatal("private content would be stored by a shared cache")
	}
	if ok.Code != 200 || ok.Body.String() != "private content" || len(ok.Result().Cookies()) != 1 || ok.Result().Cookies()[0].Name != "ordinary" {
		t.Fatal("runtime session not passed through correctly")
	}
}

func TestGatewayConfiguration(t *testing.T) {
	for _, v := range [][3]string{{"guest.example.org:80", "apps.example.com", "https://drop.example.net"}, {"192.0.2.1:80", "apps.example.com", "https://drop.example.net"}, {"127.0.0.1:80", "apps.example.com", "http://drop.example.net"}, {"127.0.0.1:80", "apps.example.com", "https://login.apps.example.com"}} {
		if _, err := New(v[0], v[1], v[2]); err == nil {
			t.Fatalf("unsafe configuration accepted: %v", v)
		}
	}
}
