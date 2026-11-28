package dropserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/mailer"
)

type waitJob struct {
	ID         string `db:"id"`
	Entry      string `db:"entry"`
	Kind       string `db:"kind"`
	Generation int64  `db:"generation"`
	Attempts   int    `db:"attempts"`
}

func waitMessage(app core.App, j waitJob) (*mailer.Message, error) {
	base := strings.TrimRight(app.Settings().Meta.AppURL, "/")
	var email, subject, body string
	if j.Kind == "admin_invite" {
		r, err := app.FindRecordById("invites", j.Entry)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		open, err := waitInviteOpen(app, r)
		if !open || err != nil {
			return nil, err
		}
		email = r.GetString("email")
		subject = "Your Drop administrator invitation"
		body = "You have been invited to administer Drop. Sign in with this email address at " + SignInURL(base, email) + ". Your invitation expires " + inviteOut(r).ExpiresAt + "."
	} else if j.Kind == "restore" {
		u, err := app.FindRecordById("users", j.Entry)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if u.GetBool("suspended") || !u.Verified() {
			return nil, nil
		}
		email = u.Email()
		subject = "Your Drop Beta access is restored"
		body = "You can sign in to your Drop account again at " + base + "/."
	} else {
		var w waitEntry
		err := app.DB().NewQuery("SELECT * FROM drop_waitlist WHERE id={:id}").Bind(dbx.Params{"id": j.Entry}).One(&w)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		email = w.Email
		switch j.Kind {
		case "code":
			if w.Attempts >= 5 || w.Requested != j.Generation || time.Now().Unix()-j.Generation >= 600 {
				return nil, nil
			}
			if admitted, err := waitlistSignInAddress(app, w.Email); err != nil || admitted {
				return nil, err
			}
			proof, err := waitProof(app, "code", w.ID, j.Generation)
			if err != nil {
				return nil, err
			}
			subject = "Verify your Drop Beta waitlist request"
			body = "Your verification code is " + proof + ". It expires in 10 minutes. Enter it on the waitlist form to confirm your address."
		case "welcome":
			r, err := app.FindRecordById("invites", w.Invite)
			if err != nil {
				return nil, err
			}
			open, err := waitInviteOpen(app, r)
			if err != nil {
				return nil, err
			}
			if !open {
				return nil, nil
			}
			subject = "Welcome to Drop Beta"
			body = "You are invited to Drop Beta. Sign in with this email address at " + SignInURL(base, email) + ". Your invitation expires " + inviteOut(r).ExpiresAt + "."
		default:
			return nil, fmt.Errorf("unknown waitlist message kind")
		}
		leave, err := waitProof(app, "leave", w.ID, 0)
		if err != nil {
			return nil, err
		}
		body += "\n\nLeave the waitlist: " + base + "/#/waitlist/leave?token=" + leave
	}
	return &mailer.Message{From: mail.Address{Name: app.Settings().Meta.SenderName, Address: app.Settings().Meta.SenderAddress}, To: []mail.Address{{Address: email}}, Subject: subject, Text: body}, nil
}
func deliverWaitMail(app core.App) (bool, error) {
	var job waitJob
	// A lease makes a crash retryable. SMTP acceptance and the SQLite receipt cannot share a transaction.
	err := app.DB().NewQuery(`UPDATE drop_waitlist_mail SET due=unixepoch()+300, attempts=attempts+1
 WHERE id=(SELECT id FROM drop_waitlist_mail WHERE state='queued' AND due<=unixepoch() ORDER BY due,id LIMIT 1)
 RETURNING id,entry,kind,generation,attempts`).One(&job)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m, err := waitMessage(app, job)
	state := "cancelled"
	if err == nil && m != nil {
		err = app.NewMailClient().Send(m)
		state = "sent"
	}
	if err != nil {
		state = "queued"
		if job.Attempts >= 8 {
			state = "failed"
		}
	}
	delay := min(3600, 15<<min(job.Attempts, 8))
	return true, waitExec(app, "UPDATE drop_waitlist_mail SET state={:state},due=unixepoch()+{:delay} WHERE id={:id} AND attempts={:n} AND state='queued'", dbx.Params{"state": state, "id": job.ID, "n": job.Attempts, "delay": delay})
}
func runWaitlistMail(ctx context.Context, app core.App) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for range 16 {
				if ctx.Err() != nil {
					return
				}
				did, err := deliverWaitMail(app)
				if err != nil {
					app.Logger().Error("Waitlist mail processing failed")
					break
				}
				if !did {
					break
				}
			}
		case <-prune.C:
			err := app.RunInTransaction(func(tx core.App) error {
				if err := waitExec(tx, "DELETE FROM drop_waitlist WHERE verified_at='' AND requested<unixepoch()-86400", nil); err != nil {
					return err
				}
				return waitExec(tx, "DELETE FROM drop_waitlist_mail WHERE (kind='code' AND generation<unixepoch()-86400) OR (kind='restore' AND generation<unixepoch()-2592000)", nil)
			})
			if err != nil {
				app.Logger().Error("Waitlist pruning failed")
			}
		}
	}
}
