package dropserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestBetaActivationRequiresVerifiedConsent(t *testing.T) {
	legal := dropserver.BetaLegal{TermsURL: "https://drop.example.test/terms/one", PrivacyURL: "https://drop.example.test/privacy/one", TermsVersion: "terms-1"}
	for _, tc := range []struct {
		name     string
		legal    dropserver.BetaLegal
		accepted string
		link     bool
		want     int
	}{
		{"code accepted", legal, "terms-1", false, 200},
		{"link accepted", legal, "terms-1", true, 200},
		{"raw code without a receipt", legal, "", false, 428},
		{"raw link with an old version", legal, "terms-old", true, 428},
		{"closed Beta", dropserver.BetaLegal{}, "terms-1", false, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := droptest.NewHosted(t, droptest.Options{BetaNodes: []string{"demo-node"}, BetaLegal: tc.legal})
			email := "new@example.test"
			h.Invite(email, "user")
			id := h.RequestOTP(email)
			mail := h.Mail()
			path := appserver.OTPCodePath
			body := fmt.Sprintf(`{"otpId":%q,"code":%q}`, id, mail.Code)
			if tc.link {
				path = "/api/collections/users/auth-with-otp"
				body = fmt.Sprintf(`{"otpId":%q,"password":%q}`, id, mail.Password)
			}
			request := func(body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "https://"+droptest.Domain+path, strings.NewReader(body))
				r.TLS = nil
				r.Header.Set("Content-Type", "application/json")
				if tc.accepted != "" {
					r.Header.Set("X-Drop-Accept-Terms", tc.accepted)
				}
				w := httptest.NewRecorder()
				h.Server.Public().ServeHTTP(w, r)
				return w
			}
			bad := strings.ReplaceAll(body, mail.Code, "x")
			if tc.link {
				bad = strings.ReplaceAll(body, mail.Password, "x")
			}
			if w := request(bad); w.Code != http.StatusBadRequest {
				t.Fatalf("invalid credential got %d %s", w.Code, w.Body)
			}
			before, err := h.App.FindAuthRecordByEmail("users", email)
			if err != nil || before.Verified() || !before.GetDateTime("beta_terms_accepted_at").IsZero() {
				t.Fatal("receipt before proof", err)
			}
			w := request(body)
			if w.Code != tc.want {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			user, err := h.App.FindAuthRecordByEmail("users", email)
			if err != nil {
				t.Fatal(err)
			}
			wantReceipt := tc.want == 200
			if got := strings.Contains(w.Body.String(), `"token":"`); got != wantReceipt {
				t.Fatal(w.Body)
			}
			if user.Verified() != wantReceipt || user.GetBool("beta_terms_pending") == wantReceipt || (user.GetString("beta_terms_version") == "terms-1") != wantReceipt || user.GetDateTime("beta_terms_accepted_at").IsZero() == wantReceipt {
				t.Fatal("incorrect acceptance state", user.Verified(), user.GetBool("beta_terms_pending"), user.GetString("beta_terms_version"))
			}
			if strings.Contains(w.Body.String(), "beta_terms_") {
				t.Fatal("private fields exposed", w.Body)
			}
		})
	}
}

func TestBetaPreservesVerifiedAccountSignIn(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	existing, _ := h.User("existing@example.test", "user")
	h.MustOp(h.Token(h.Owner()), "beta.approve", map[string]any{"sha256": strings.Repeat("a", 64)})
	if got := h.SignIn(existing.Email()); got.Token == "" {
		t.Fatal("returning verified user denied")
	}
	if got := h.SignIn(droptest.OwnerEmail); got.Token == "" {
		t.Fatal("pre-existing owner denied")
	}
}
