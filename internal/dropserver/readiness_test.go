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
