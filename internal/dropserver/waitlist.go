package dropserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"golang.org/x/time/rate"
)

const waitTerms = "2026-11-26-waitlist"

type waitEntry struct {
	ID        string `db:"id" json:"id"`
	Email     string `db:"email" json:"email"`
	Terms     string `db:"terms" json:"-"`
	Requested int64  `db:"requested" json:"-"`
	Attempts  int    `db:"attempts" json:"-"`
	Activated string `db:"activated_at" json:"activated_at"`
	Verified  string `db:"verified_at" json:"verified_at"`
	Invite    string `db:"invite_id" json:"invite_id"`
}
type waitPublic struct {
	Email string `json:"email"`
	Terms string `json:"terms_version"`
	Code  string `json:"code"`
	Token string `json:"token"`
}

func waitExec(app core.App, query string, params dbx.Params) error {
	_, err := app.DB().NewQuery(query).Bind(params).Execute()
	return err
}
func waitKey(app core.App, kind, id string, generation int64) ([]byte, error) {
	var key []byte
	if err := app.DB().NewQuery("SELECT secret FROM drop_waitlist_key").Row(&key); err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "%s:%s:%d", kind, id, generation)
	return h.Sum(nil), nil
}
func waitProof(app core.App, kind, id string, generation int64) (string, error) {
	mac, err := waitKey(app, kind, id, generation)
	if err != nil {
		return "", err
	}
	if kind == "code" {
		return fmt.Sprintf("%06d", binary.BigEndian.Uint64(mac)%1000000), nil
	}
	return id + "." + hex.EncodeToString(mac), nil
}
func waitQueue(app core.App, entry, kind string, generation int64) error {
	return waitExec(app, "INSERT INTO drop_waitlist_mail(id,entry,kind,generation,due) VALUES({:id},{:entry},{:kind},{:generation},unixepoch())", dbx.Params{"id": core.GenerateDefaultRandomId(), "entry": entry, "kind": kind, "generation": generation})
}

func registerWaitlist(e *core.ServeEvent) {
	limiter := newClientLimiter(rate.Every(5*time.Second), 20)
	for _, action := range []string{"request", "verify", "leave"} {
		e.Router.POST("/api/drop/waitlist/"+action, func(re *core.RequestEvent) error {
			re.Response.Header().Set("Cache-Control", "no-store")
			if !accountsOf(re.App).hosted {
				return re.NotFoundError("", nil)
			}
			if !limiter.allow(re.Request.RemoteAddr, time.Now()) {
				return rateLimited(re, "Try again later.")
			}
			var in waitPublic
			if err := re.BindBody(&in); err != nil {
				return writeError(re, Invalid("request", "Enter valid request details."))
			}
			var rejected error
			err := re.App.RunInTransaction(func(tx core.App) error {
				switch action {
				case "request":
					return waitRequest(tx, in)
				case "verify":
					var err error
					rejected, err = waitVerify(tx, in)
					return err
				default:
					return waitLeave(tx, in.Token)
				}
			})
			if err != nil {
				return writeError(re, err)
			}
			if rejected != nil {
				return writeError(re, rejected)
			}
			status := http.StatusOK
			if action == "request" {
				status = http.StatusAccepted
			}
			return re.JSON(status, map[string]bool{"ok": true})
		}).Bind(apis.BodyLimit(2048))
	}
}

// Eligible sign-ins receive Core's ordinary OTP. Both public request routes
// keep their normal response even when they have no mail to send.
func waitlistSignInAddress(app core.App, email string) (bool, error) {
	var account bool
	if err := app.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM users WHERE email={:email} COLLATE NOCASE)").Bind(dbx.Params{"email": email}).Row(&account); err != nil || account {
		return account, err
	}
	invite, err := openInvite(app, email)
	return invite != nil, err
}

func waitRequest(app core.App, in waitPublic) error {
	if in.Terms != waitTerms {
		return Conflict("terms_required", "Read and accept the current Beta terms.")
	}
	email, err := canonicalEmail(in.Email)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	var entry waitEntry
	err = app.DB().NewQuery("SELECT * FROM drop_waitlist WHERE email={:email}").Bind(dbx.Params{"email": email}).One(&entry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if entry.Requested > now-60 {
		return nil
	}
	var count int
	if err := app.DB().NewQuery("SELECT count(*) FROM drop_waitlist_mail WHERE kind='code' AND generation > unixepoch()-3600").Row(&count); err != nil {
		return err
	}
	if count >= 100 {
		return nil
	}
	if entry.ID != "" {
		if err := app.DB().NewQuery("SELECT count(*) FROM drop_waitlist_mail WHERE kind='code' AND entry={:id} AND generation>unixepoch()-86400").Bind(dbx.Params{"id": entry.ID}).Row(&count); err != nil {
			return err
		}
		if count >= 5 {
			return nil
		}
	}
	if admitted, err := waitlistSignInAddress(app, email); err != nil || admitted {
		return err
	}
	if entry.ID == "" {
		entry.ID = core.GenerateDefaultRandomId()
	}
	if err := waitExec(app, `INSERT INTO drop_waitlist(id,email,terms,requested) VALUES({:id},{:email},{:terms},{:now})
 ON CONFLICT(email) DO UPDATE SET requested=excluded.requested,terms=excluded.terms,attempts=0`, dbx.Params{"id": entry.ID, "email": email, "terms": in.Terms, "now": now}); err != nil {
		return err
	}
	return waitQueue(app, entry.ID, "code", now)
}

func waitVerify(app core.App, in waitPublic) (error, error) {
	invalid := Invalid("code", "This code is invalid or expired.")
	email, err := canonicalEmail(in.Email)
	if err != nil || len(in.Code) != 6 {
		return invalid, nil
	}
	var row waitEntry
	err = app.DB().NewQuery("SELECT * FROM drop_waitlist WHERE email={:email}").Bind(dbx.Params{"email": email}).One(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid, nil
	}
	if err != nil {
		return nil, err
	}
	// A previously verified member can reconfirm with a fresh challenge
	// without losing their original place. Codes issued before that first
	// verification are spent, including those issued by an older Drop.
	joined, _ := time.Parse(time.RFC3339Nano, row.Verified)
	if row.Requested <= joined.Unix() || row.Attempts >= 5 || row.Requested <= time.Now().Unix()-600 {
		return invalid, nil
	}
	if row.Terms != waitTerms {
		return Conflict("terms_required", "Read and accept the current Beta terms."), nil
	}
	code, err := waitProof(app, "code", row.ID, row.Requested)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal([]byte(code), []byte(in.Code)) {
		return invalid, waitExec(app, "UPDATE drop_waitlist SET attempts=attempts+1 WHERE id={:id}", dbx.Params{"id": row.ID})
	}
	if admitted, err := waitlistSignInAddress(app, email); err != nil {
		return nil, err
	} else if admitted {
		return invalid, nil
	}
	return nil, waitExec(app, "UPDATE drop_waitlist SET verified_at=CASE WHEN verified_at='' THEN {:now} ELSE verified_at END,attempts=5 WHERE id={:id}", dbx.Params{"id": row.ID, "now": time.Now().UTC().Format(time.RFC3339Nano)})
}

func waitLeave(app core.App, token string) error {
	id, _, ok := strings.Cut(token, ".")
	invalid := Invalid("token", "This link is invalid.")
	if !ok || !recordID.MatchString(id) || len(token) != 80 {
		return invalid
	}
	expected, err := waitProof(app, "leave", id, 0)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(token)) {
		return invalid
	}
	var row waitEntry
	err = app.DB().NewQuery("SELECT * FROM drop_waitlist WHERE id={:id}").Bind(dbx.Params{"id": id}).One(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Invite != "" {
		if err := withdrawWaitInvite(app, row.Invite); err != nil {
			return err
		}
	}
	for _, q := range []string{"DELETE FROM drop_waitlist_mail WHERE entry={:id}", "DELETE FROM drop_waitlist WHERE id={:id}"} {
		if err := waitExec(app, q, dbx.Params{"id": id}); err != nil {
			return err
		}
	}
	return nil
}

// Core reserves an invitation when issuing the first sign-in code. Revoking
// during that window must also invalidate the provisional account and its OTP.
func withdrawWaitInvite(app core.App, id string) error {
	if id == "" {
		return NotFound("Invite")
	}
	r, err := app.FindRecordById("invites", id)
	if err != nil {
		return err
	}
	if r.GetString("used_by") != "" {
		u, err := app.FindRecordById("users", r.GetString("used_by"))
		if err != nil {
			return err
		}
		if u.Verified() {
			return nil
		}
		if err := app.Delete(u); err != nil {
			return err
		}
		r, err = app.FindRecordById("invites", id)
		if err != nil {
			return err
		}
	}
	if r.GetDateTime("revoked_at").IsZero() {
		r.Set("revoked_at", time.Now().UTC())
		return app.Save(r)
	}
	return nil
}

// An unused first sign-in code may already have reserved the underlying invite.
func waitInviteOpen(app core.App, r *core.Record) (bool, error) {
	if !r.GetDateTime("revoked_at").IsZero() || !r.GetDateTime("expires_at").Time().After(time.Now()) {
		return false, nil
	}
	account := r.GetString("used_by")
	if account == "" {
		return true, nil
	}
	u, err := app.FindRecordById("users", account)
	if err != nil {
		return false, err
	}
	return !u.Verified(), nil
}
