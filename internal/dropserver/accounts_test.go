package dropserver_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func accounts(t *testing.T, h *droptest.Hosted) int {
	t.Helper()
	n, err := h.App.CountRecords("users")
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func setInvite(t *testing.T, h *droptest.Hosted, id string, fields map[string]any) {
	t.Helper()
	invite, err := h.App.FindRecordById("invites", id)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		invite.Set(k, v)
	}
	if err := h.App.Save(invite); err != nil {
		t.Fatal(err)
	}
}

// TestInvitedEmailGetsTheInvitesRole: an invited address, in any case, gets
// the sign-in email and an account with the invite's role and quota; the
// invite is used, and the session sees its role through /api/drop/me.
func TestInvitedEmailGetsTheInvitesRole(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	inviteID := h.Invite("helper@example.test", "admin")
	setInvite(t, h, inviteID, map[string]any{"app_quota": 7})
	auth := h.SignIn("Helper@Example.TEST")
	user, err := h.App.FindAuthRecordByEmail("users", "helper@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if user.GetString("role") != "admin" || user.GetInt("app_quota") != 7 || user.GetString("invited_by") != h.Owner().Id || !user.EmailVisibility() || !user.Verified() {
		t.Fatalf("account fields: role=%q quota=%d invited_by=%q", user.GetString("role"), user.GetInt("app_quota"), user.GetString("invited_by"))
	}
	invite, _ := h.App.FindRecordById("invites", inviteID)
	if invite.GetDateTime("used_at").IsZero() || invite.GetString("used_by") != user.Id {
		t.Fatal("the invite was not marked used")
	}
	w := h.Request("GET", "/api/drop/me", auth.Token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"admin"`) || !strings.Contains(w.Body.String(), `"email":"helper@example.test"`) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	// A user invite gives a user.
	h.Invite("member@example.test", "user")
	member := h.SignIn("member@example.test")
	if r := h.MustOp(member.Token, "me", nil); r["role"] != "user" || r["admin"] != false || r["owner"] != false {
		t.Fatalf("member: %v", r)
	}
}

// TestAllowSignInRefusesWithoutAnOpenInvite: no invite, an expired, revoked
// or used invite and the local address all get the ordinary answer, no email
// and no account.
func TestAllowSignInRefusesWithoutAnOpenInvite(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	expired := h.Invite("expired@example.test", "user")
	setInvite(t, h, expired, map[string]any{"expires_at": time.Now().UTC().Add(-time.Minute)})
	revoked := h.Invite("revoked@example.test", "user")
	setInvite(t, h, revoked, map[string]any{"revoked_at": time.Now().UTC()})
	used := h.Invite("used@example.test", "user")
	setInvite(t, h, used, map[string]any{"used_at": time.Now().UTC()})
	for _, email := range []string{"stranger@example.test", "expired@example.test", "revoked@example.test", "used@example.test", "account@drop.invalid"} {
		allowed, err := dropserver.AllowSignIn(h.App, email)
		if err != nil || allowed {
			t.Errorf("AllowSignIn(%s) = %v, %v", email, allowed, err)
		}
		h.RequestOTP(email)
	}
	h.NoMail()
	if n := accounts(t, h); n != 1 {
		t.Fatalf("accounts = %d", n)
	}
	h.Invite("open@example.test", "user")
	if allowed, err := dropserver.AllowSignIn(h.App, "open@example.test"); err != nil || !allowed {
		t.Fatalf("AllowSignIn(open) = %v, %v", allowed, err)
	}
}

// TestAccountCreationNeedsAnInvite: even a save that bypasses sign-in cannot
// create an account without an open invite (defence in depth); the owner is
// an admin.
func TestAccountCreationNeedsAnInvite(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	if h.Owner().GetString("role") != "admin" {
		t.Fatalf("owner role %q", h.Owner().GetString("role"))
	}
	users, _ := h.App.FindCollectionByNameOrId("users")
	record := core.NewRecord(users)
	record.SetEmail("sneaky@example.test")
	record.SetRandomPassword()
	record.Set("role", "admin")
	if err := h.App.Save(record); err == nil || !strings.Contains(err.Error(), "open invite") {
		t.Fatalf("account without an invite: %v", err)
	}
}

// TestOwnerIsMadeAdminAtStart: the start hook repairs the owner's role.
func TestOwnerIsMadeAdminAtStart(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Owner()
	owner.Set("role", "user")
	if err := h.App.Save(owner); err != nil {
		t.Fatal(err)
	}
	if err := dropserver.Hooks().OnServeStart(&core.ServeEvent{App: h.App}, &appserver.Project{}); err != nil {
		t.Fatal(err)
	}
	if h.Owner().GetString("role") != "admin" {
		t.Fatal("the owner is not an admin after start")
	}
}

// TestInviteIsSingleUseUnderConcurrency: simultaneous sign-in requests for
// one invited address create exactly one account.
func TestInviteIsSingleUseUnderConcurrency(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	h.Invite("rush@example.test", "user")
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Go(func() {
			codes[i] = h.Request(http.MethodPost, "/api/collections/users/request-otp", "", `{"email":"rush@example.test"}`).Code
		})
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if n := accounts(t, h); n != 2 {
		t.Fatalf("accounts = %d, want the owner and one invited account", n)
	}
	open, err := h.App.FindRecordsByFilter("invites", "used_at = ''", "", 0, 0)
	if err != nil || len(open) != 0 {
		t.Fatalf("open invites after use: %d, %v", len(open), err)
	}
}

// TestPrunedAccountGetsItsInviteBack: core prunes an account that never
// signed in; its invite opens again so the person can still sign in.
func TestPrunedAccountGetsItsInviteBack(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	inviteID := h.Invite("late@example.test", "user")
	h.RequestOTP("late@example.test")
	h.Mail()
	user, err := h.App.FindAuthRecordByEmail("users", "late@example.test")
	if err != nil || user.Verified() {
		t.Fatalf("pending account: %v", err)
	}
	if err := h.App.Delete(user); err != nil {
		t.Fatal(err)
	}
	invite, _ := h.App.FindRecordById("invites", inviteID)
	if !invite.GetDateTime("used_at").IsZero() || invite.GetString("used_by") != "" {
		t.Fatal("the invite of a pruned account stayed used")
	}
	if allowed, _ := dropserver.AllowSignIn(h.App, "late@example.test"); !allowed {
		t.Fatal("the reopened invite does not admit the address")
	}
}

// TestSuspendedAccountIsLockedOut: suspension ends every session at once and
// a new sign-in is refused; lifting it lets the account in again.
func TestSuspendedAccountIsLockedOut(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	user, token := h.User("gone@example.test", "user")
	owner := h.Token(h.Owner())
	h.MustOp(owner, "user.set", map[string]any{"user": "gone@example.test", "suspended": true})
	if r := h.Op(token, "me", nil); r.Status != 401 {
		t.Fatalf("suspended session: %d %s", r.Status, r.Raw)
	}
	otpID := h.RequestOTP("gone@example.test")
	m := h.Mail()
	w := h.Request(http.MethodPost, appserver.OTPCodePath, "", fmt.Sprintf(`{"otpId":%q,"code":%q}`, otpID, m.Code))
	if w.Code == 200 {
		t.Fatalf("a suspended account signed in: %s", w.Body)
	}
	h.MustOp(owner, "user.set", map[string]any{"user": user.Id, "suspended": false})
	if r := h.Op(token, "me", nil); r.Status != 401 {
		t.Fatalf("restoration revived a prior session: %d", r.Status)
	}
	w = h.Request(http.MethodPost, appserver.OTPCodePath, "", fmt.Sprintf(`{"otpId":%q,"code":%q}`, otpID, m.Code))
	if w.Code == http.StatusOK {
		t.Fatal("restoration revived a previously issued code")
	}
	// A second restored account can immediately request a fresh login.
	// The first address remains within its normal email cooldown.
	restored, prior := h.User("restored@example.test", "user")
	h.MustOp(owner, "user.set", map[string]any{"user": restored.Id, "suspended": true})
	h.MustOp(owner, "user.set", map[string]any{"user": restored.Id, "suspended": false})
	if r := h.Op(prior, "me", nil); r.Status != 401 {
		t.Fatalf("old session after restore: %d", r.Status)
	}
	fresh := h.SignIn("restored@example.test")
	if r := h.Op(fresh.Token, "me", nil); r.Status != 200 {
		t.Fatalf("unsuspended account: %d %s", r.Status, r.Raw)
	}
}
