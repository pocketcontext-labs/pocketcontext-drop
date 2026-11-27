package dropserver_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestWaitlistMembershipAndInvitation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	owner := h.Token(h.Owner())
	_, user := h.User("other@example.test", "user")
	const address = "join@example.test"
	request := func(action, body string, want int) {
		t.Helper()
		r := h.Request("POST", "/api/drop/waitlist/"+action, "", body)
		if r.Code != want {
			t.Fatalf("%s: %d %s", action, r.Code, r.Body.String())
		}
	}
	list := func() []any {
		return h.MustOp(owner, "waitlist.list", map[string]any{"search": "JOIN@"})["entries"].([]any)
	}
	body := `{"email":"join@example.test","terms_version":"2026-11-26-waitlist"}`
	request("request", body, 202)
	request("request", body, 202)
	if rows := list(); len(rows) != 0 {
		t.Fatal(rows)
	}
	var id string
	var created int64
	var key []byte
	if err := h.App.DB().NewQuery("SELECT id,requested FROM drop_waitlist WHERE email={:email}").Bind(dbx.Params{"email": address}).Row(&id, &created); err != nil {
		t.Fatal(err)
	}
	if err := h.App.DB().NewQuery("SELECT secret FROM drop_waitlist_key").Row(&key); err != nil {
		t.Fatal(err)
	}
	proof := func(purpose string, generation int64) []byte {
		m := hmac.New(sha256.New, key)
		fmt.Fprintf(m, "%s:%s:%d", purpose, id, generation)
		return m.Sum(nil)
	}
	verify := fmt.Sprintf(`{"email":%q,"code":"%06d"}`, address, binary.BigEndian.Uint64(proof("code", created))%1000000)
	request("verify", verify, 200)
	request("verify", verify, 400)
	rows := list()
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	row := rows[0].(map[string]any)
	if row["id"] != id || row["email"] != address || row["state"] != "waiting" {
		t.Fatal(row)
	}
	for _, op := range []string{"waitlist.list", "waitlist.invite", "waitlist.resend", "waitlist.revoke"} {
		if h.Op(user, op, map[string]any{}).Status != 403 {
			t.Fatal(op)
		}
	}
	args := map[string]any{"id": id}
	inviteArgs := map[string]any{"id": id, "role": "developer"}
	h.MustOp(owner, "waitlist.invite", inviteArgs)
	h.MustOp(owner, "waitlist.invite", inviteArgs)
	if got := h.Op(owner, "waitlist.invite", map[string]any{"id": id, "role": "admin"}).Status; got != 409 {
		t.Fatalf("changing an open invitation role returned %d", got)
	}
	var count int
	if err := h.App.DB().NewQuery("SELECT count(*) FROM drop_waitlist_mail WHERE kind='welcome'").Row(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	row = list()[0].(map[string]any)
	if row["state"] != "open" || row["mail_state"] != "queued" {
		t.Fatal(row)
	}
	if h.Op(owner, "waitlist.resend", args).Status != 409 {
		t.Fatal("resend cooldown")
	}
	h.MustOp(owner, "waitlist.revoke", args)
	if list()[0].(map[string]any)["state"] != "revoked" {
		t.Fatal(list())
	}
	h.MustOp(owner, "waitlist.invite", args)
	token := fmt.Sprintf("%s.%x", id, proof("leave", 0))
	v, _ := json.Marshal(map[string]string{"token": token})
	request("leave", string(v), 200)
	request("leave", string(v), 200)
	if rows := list(); len(rows) != 0 {
		t.Fatal(rows)
	}
	request("request", strings.Replace(body, "-waitlist", "-old", 1), 409)
	if time.Now().Unix()-created > 60 {
		t.Fatal("unexpected challenge timestamp")
	}
}

func TestWaitlistWorkerRetriesCommittedMail(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	message := make(chan string, 4)
	first := true
	h.App.OnMailerSend().BindFunc(func(e *core.MailerEvent) error {
		if first {
			first = false
			return errors.New("synthetic SMTP outage")
		}
		message <- e.Message.Subject + " " + e.Message.Text
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { dropserver.Hooks().Background(ctx, h.App); close(finished) }()
	defer func() { cancel(); <-finished }()
	if r := h.Request("POST", "/api/drop/waitlist/request", "", `{"email":"worker@example.test","terms_version":"2026-11-26-waitlist"}`); r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	deadline := time.Now().Add(12 * time.Second)
	for {
		var count int
		err := h.App.DB().NewQuery("SELECT count(*) FROM drop_waitlist_mail WHERE state='queued' AND attempts=1 AND due BETWEEN unixepoch()+1 AND unixepoch()+31").Row(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SMTP failure was not scheduled for retry")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := h.App.DB().NewQuery("UPDATE drop_waitlist_mail SET due=0").Execute(); err != nil {
		t.Fatal(err)
	}
	var text string
	select {
	case text = <-message:
	case <-time.After(10 * time.Second):
		t.Fatal("no verification mail after retry")
	}
	codes := regexp.MustCompile(`code is ([0-9]{6})`).FindStringSubmatch(text)
	if len(codes) != 2 || !strings.Contains(text, "/#/waitlist/leave?token=") {
		t.Fatal("missing verification or withdrawal")
	}
	if r := h.Request("POST", "/api/drop/waitlist/verify", "", fmt.Sprintf(`{"email":"worker@example.test","code":%q}`, codes[1])); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	owner := h.Token(h.Owner())
	row := h.MustOp(owner, "waitlist.list", nil)["entries"].([]any)[0].(map[string]any)
	h.MustOp(owner, "waitlist.invite", map[string]any{"id": row["id"]})
	select {
	case text = <-message:
	case <-time.After(10 * time.Second):
		t.Fatal("no welcome email")
	}
	if !strings.HasPrefix(text, "Welcome to Drop Beta") || !strings.Contains(text, "/#/invite?email=worker%40example.test") {
		t.Fatal("incorrect welcome invitation")
	}
}

func TestUnifiedEntryKeepsAdmissionPrivate(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	h.Invite("invited@example.test", "user")
	h.User("existing@example.test", "user")
	for _, email := range []string{"invited@example.test", "existing@example.test", "unknown@example.test"} {
		r := h.Request("POST", "/api/drop/waitlist/request", "", fmt.Sprintf(`{"email":%q,"check_access":true}`, email))
		if r.Code != 200 || !strings.Contains(r.Body.String(), fmt.Sprintf(`"admitted":%t`, email != "unknown@example.test")) {
			t.Fatal(email, r.Body.String())
		}
	}
	var pending int
	if err := h.App.DB().NewQuery("SELECT count(*) FROM drop_waitlist").Row(&pending); err != nil || pending != 0 {
		t.Fatal("access check stored an address", pending, err)
	}
	var generic string
	for _, email := range []string{"unknown@example.test", "invited@example.test", "existing@example.test"} {
		r := h.Request("POST", "/api/drop/waitlist/request", "", fmt.Sprintf(`{"email":%q,"terms_version":"2026-11-26-waitlist"}`, email))
		if r.Code != 202 {
			t.Fatal(r.Code, r.Body)
		}
		if generic == "" {
			generic = r.Body.String()
		} else if generic != r.Body.String() {
			t.Fatal("admission leaked in public response")
		}
		reject := h.Request("POST", "/api/drop/waitlist/verify", "", fmt.Sprintf(`{"email":%q,"code":"xxxxxx"}`, email))
		if reject.Code != 400 || !strings.Contains(reject.Body.String(), "invalid_input") {
			t.Fatal(reject.Body)
		}
		id := h.RequestOTP(email)
		if email == "unknown@example.test" {
			h.NoMail()
			continue
		}
		m := h.Mail()
		if m.Email != email {
			t.Fatal("unexpected sign-in recipient")
		}
		if h.RedeemCode(id, m.Code).Record["email"] != email {
			t.Fatal("sign-in failed")
		}
	}
	var count int
	if err := h.App.DB().NewQuery("SELECT count(*) FROM drop_waitlist_mail WHERE kind='code'").Row(&count); err != nil || count != 1 {
		t.Fatal("only unknown address should receive a waitlist code", count, err)
	}
}
func TestReturningWaitlistMemberRetainsVerificationDate(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	const joined = "2026-01-01T00:00:00Z"
	const address = "returning@example.test"
	const id = "returning000001"
	_, err := h.App.DB().NewQuery("INSERT INTO drop_waitlist(id,email,terms,requested,verified_at) VALUES({:id},{:email},'2026-11-26-waitlist',0,{:joined})").Bind(dbx.Params{"id": id, "email": address, "joined": joined}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	r := h.Request("POST", "/api/drop/waitlist/request", "", fmt.Sprintf(`{"email":%q,"terms_version":"2026-11-26-waitlist"}`, address))
	if r.Code != 202 {
		t.Fatal(r.Body)
	}
	var key []byte
	var gen int64
	if err := h.App.DB().NewQuery("SELECT generation,secret FROM drop_waitlist_mail,drop_waitlist_key WHERE kind='code'").Row(&gen, &key); err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "code:%s:%d", id, gen)
	body := fmt.Sprintf(`{"email":%q,"code":"%06d"}`, address, binary.BigEndian.Uint64(m.Sum(nil))%1000000)
	for _, want := range []int{200, 400} {
		if r := h.Request("POST", "/api/drop/waitlist/verify", "", body); r.Code != want {
			t.Fatal(r.Code, r.Body)
		}
	}
	var actual string
	if err := h.App.DB().NewQuery("SELECT verified_at FROM drop_waitlist").Row(&actual); err != nil || actual != joined {
		t.Fatal(actual, err)
	}
}

func TestLoginAccessRequiresOnlyApplicableConsent(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{BetaNodes: []string{"fixture-node"}})
	h.Invite("invited@example.test", "user")
	h.Invite("pending@example.test", "user")
	h.RequestOTP("pending@example.test")
	returning, _ := h.User("returning@example.test", "user")
	returning.Set("beta_terms_version", "fixture-terms")
	returning.Set("beta_terms_pending", false)
	if err := h.App.Save(returning); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		address           string
		admitted, consent bool
	}{
		{"invited@example.test", true, true}, {"pending@example.test", true, true}, {returning.Email(), true, false}, {droptest.OwnerEmail, true, false}, {"uninvited@example.test", false, false},
	} {
		w := h.Request("POST", "/api/drop/waitlist/request", "", fmt.Sprintf(`{"email":%q,"check_access":true,"login_flow":true}`, tc.address))
		var result map[string]bool
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result) != 2 || result["admitted"] != tc.admitted || result["needs_consent"] != tc.consent {
			t.Fatalf("%s: %d %s", tc.address, w.Code, w.Body)
		}
	}
	var count int
	if err := h.App.DB().NewQuery("SELECT count(*) FROM drop_waitlist").Row(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
