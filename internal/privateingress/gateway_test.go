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

func TestAppRootAndRetiredSession(t *testing.T) {
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
		if r.Header.Get(render.PrivateAccessHeader) != "" {
			t.Error("legacy ticket reached the guest")
		}
		if c, err := r.Cookie("guest"); calls > 1 && (err != nil || c.Value != "yes") {
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
		if cookie != "" {
			r.Header.Set(render.PrivateAccessHeader, valid)
		}
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w
	}
	get := request("GET", sessionPath, "", "", "")
	if get.Code != http.StatusSeeOther || get.Header().Get("Location") != "/" || calls != 0 {
		t.Fatal("old session bookmark did not redirect to root")
	}
	if w := request("POST", sessionPath, control, `{"token":"old"}`, ""); w.Code != http.StatusGone || calls != 0 {
		t.Fatal("old session POST reached the guest")
	}
	if w := request("GET", "/", "", "", ""); w.Code != 200 || w.Body.String() != "private content" {
		t.Fatal("clean browser could not open the app")
	}
	ok := request("GET", "/", "", "", sessionCookie+"="+valid+"; guest=yes")
	if ok.Code != 200 || len(ok.Result().Cookies()) != 1 || ok.Result().Cookies()[0].Name != "ordinary" {
		t.Fatal("guest cookies were not isolated from the former gateway cookie")
	}
}

func TestGatewayConfiguration(t *testing.T) {
	for _, v := range [][3]string{{"guest.example.org:80", "apps.example.com", "https://drop.example.net"}, {"192.0.2.1:80", "apps.example.com", "https://drop.example.net"}, {"127.0.0.1:80", "apps.example.com", "http://drop.example.net"}, {"127.0.0.1:80", "apps.example.com", "https://login.apps.example.com"}} {
		if _, err := New(v[0], v[1], v[2]); err == nil {
			t.Fatalf("unsafe configuration accepted: %v", v)
		}
	}
}
