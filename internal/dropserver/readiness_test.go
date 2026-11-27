package dropserver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type readinessTransport func(*http.Request) (*http.Response, error)

func (f readinessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCrewURLReadyChecksOnlyManagedOriginAndHostedLogin(t *testing.T) {
	for _, tc := range []struct {
		host          string
		status        int
		body          string
		called, ready bool
	}{
		{"crew.example.test", 200, `{"mode":"email","codeOnly":true}`, true, true},
		{"crew.example.test", 200, `{"mode":"password"}`, true, false},
		{"crew.example.test", 200, strings.Repeat(" ", 4097) + `{"mode":"email"}`, true, false},
		{"crew.example.test", 502, "gateway unavailable", true, false},
		{"crew.example.test", 302, "", true, false},
		{"attacker.test", 200, `{"mode":"email"}`, false, false},
	} {
		called := false
		client := &http.Client{CheckRedirect: readinessClient.CheckRedirect, Transport: readinessTransport(func(r *http.Request) (*http.Response, error) {
			called = true
			if r.URL.String() != "https://crew.example.test/api/bundle/auth-mode" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Fatalf("unexpected %s or credentials", r.URL)
			}
			h := http.Header{}
			if tc.status == 302 {
				h.Set("Location", "https://another.test/")
			}
			return &http.Response{StatusCode: tc.status, Header: h, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})}
		ready := crewURLReady(context.Background(), "example.test", "crew", tc.host, client)
		if called != tc.called || ready != tc.ready {
			t.Errorf("%s %d called=%v ready=%v", tc.host, tc.status, called, ready)
		}
	}
}

func TestImportedReadinessRequiresCurrentManagedHTTPResponse(t *testing.T) {
	for _, tc := range []struct {
		executable, endpoint string
		status               int
		ready                bool
	}{
		{"bin/pocketbase", "/api/health", 200, true},
		{"zeroclaw", "/", 200, true},
		{"ordinary", "/", 401, true},
		{"ordinary", "/", 404, false},
		{"ordinary", "/", 502, false},
		{"ordinary", "/", 504, false},
		{"ordinary", "/", 0, false},
	} {
		client := &http.Client{Transport: readinessTransport(func(req *http.Request) (*http.Response, error) {
			if req.Method != "GET" || req.URL.String() != "https://demo.example.test"+tc.endpoint || req.Header.Get("Cache-Control") != "no-cache" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
				t.Fatalf("unexpected readiness request %v", req)
			}
			if tc.status == 0 {
				return nil, context.DeadlineExceeded
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(""))}, nil
		}), CheckRedirect: readinessClient.CheckRedirect}
		if got := betaURLReady(context.Background(), "example.test", "demo", "demo.example.test", tc.executable, client); got != tc.ready {
			t.Errorf("status %d: got %v", tc.status, got)
		}
		if betaURLReady(context.Background(), "example.test", "demo", "other.example.test", tc.executable, client) {
			t.Fatal("checked another app")
		}
	}
}
