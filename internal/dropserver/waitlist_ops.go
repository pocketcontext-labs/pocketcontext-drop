package dropserver

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() {
	register("waitlist.list", waitList)
	register("waitlist.invite", func(c *Ctx, in *dropapi.WaitlistInvite) (any, error) {
		return waitAction(c, in.ID, "invite", in.Role)
	})
	for _, action := range []string{"resend", "revoke"} {
		register("waitlist."+action, func(c *Ctx, in *dropapi.WaitlistID) (any, error) { return waitAction(c, in.ID, action, "") })
	}
}

type waitListRow struct {
	waitEntry
	Invited   string `db:"invited_at" json:"invited_at"`
	Expires   string `db:"expires_at" json:"expires_at"`
	State     string `db:"state" json:"state"`
	Account   string `db:"account_id" json:"account_id"`
	Suspended bool   `db:"suspended" json:"suspended"`
	Mail      string `db:"mail_state" json:"mail_state"`
}

func waitList(c *Ctx, in *dropapi.WaitlistListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	search := strings.ToLower(strings.TrimSpace(in.Search))
	if len(search) > 254 {
		return nil, Invalid("search", "Use at most 254 characters.")
	}
	rows := []waitListRow{}
	err = c.App.DB().NewQuery(`SELECT w.*,COALESCE(i.created,'') AS invited_at,COALESCE(i.expires_at,'') AS expires_at,
 CASE WHEN u.verified=1 THEN 'used' WHEN i.revoked_at!='' THEN 'revoked' WHEN i.expires_at!='' AND i.expires_at<=strftime('%Y-%m-%d %H:%M:%fZ','now') THEN 'expired' WHEN i.id IS NOT NULL THEN 'open' ELSE 'waiting' END AS state,
 COALESCE(u.id,'') AS account_id,COALESCE(u.suspended,0) AS suspended,
 COALESCE((SELECT state FROM drop_waitlist_mail WHERE entry=w.id AND kind='welcome' ORDER BY generation DESC,id DESC LIMIT 1),'') AS mail_state
 FROM drop_waitlist w LEFT JOIN invites i ON i.id=w.invite_id LEFT JOIN users u ON u.id=i.used_by
 WHERE w.verified_at!='' AND instr(w.email,{:search})>0 ORDER BY w.verified_at,w.id LIMIT {:limit} OFFSET {:offset}`).Bind(dbx.Params{"search": search, "limit": limit + 1, "offset": offset}).All(&rows)
	if err != nil {
		return nil, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return map[string]any{"entries": rows, "has_more": more}, nil
}
func waitAction(c *Ctx, id, action string, role dropapi.Role) (any, error) {
	var w waitEntry
	err := c.App.DB().NewQuery("SELECT * FROM drop_waitlist WHERE id={:id} AND verified_at!=''").Bind(dbx.Params{"id": id}).One(&w)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFound("Waitlist entry")
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	switch action {
	case "invite":
		if w.Invite != "" {
			r, err := c.App.FindRecordById("invites", w.Invite)
			if err != nil {
				return nil, err
			}
			if state := inviteOut(r).State; state == dropapi.InviteStateOpen || state == dropapi.InviteStateUsed {
				if role != "" && string(role) != r.GetString("role") {
					return nil, Conflict("invite_role_conflict", "This invitation already has a different role.")
				}
				c.Event(Event{Detail: map[string]any{"entry": id, "invite": w.Invite, "unchanged": true}})
				return map[string]bool{"ok": true}, nil
			}
		}
		result, err := inviteCreate(c, &dropapi.InviteCreateInput{Email: w.Email, Role: role})
		if err != nil {
			return nil, err
		}
		w.Invite = result.(dropapi.InviteCreateResult).Invite.ID
		if err := waitExec(c.App, "UPDATE drop_waitlist SET invite_id={:invite} WHERE id={:id}", dbx.Params{"invite": w.Invite, "id": id}); err != nil {
			return nil, err
		}
	case "resend":
		r, err := c.App.FindRecordById("invites", w.Invite)
		if err != nil {
			return nil, Conflict("invite_closed", "Only an open invitation can be resent.")
		}
		open, err := waitInviteOpen(c.App, r)
		if err != nil {
			return nil, err
		}
		if !open {
			return nil, Conflict("invite_closed", "Only an open invitation can be resent.")
		}
		var last int64
		if err := c.App.DB().NewQuery("SELECT COALESCE(MAX(generation),0) FROM drop_waitlist_mail WHERE entry={:id} AND kind='welcome'").Bind(dbx.Params{"id": id}).Row(&last); err != nil {
			return nil, err
		}
		if now-last < 60 {
			return nil, Conflict("resend_limited", "Wait one minute before resending.")
		}
	case "revoke":
		if w.Invite == "" {
			return nil, Conflict("invite_closed", "There is no invitation to revoke.")
		}
		r, err := c.App.FindRecordById("invites", w.Invite)
		if err != nil {
			return nil, err
		}
		if id := r.GetString("used_by"); id != "" {
			u, err := c.App.FindRecordById("users", id)
			if err != nil {
				return nil, err
			}
			if u.Verified() {
				return nil, Conflict("invite_closed", "Suspend the active account instead.")
			}
		}
		if err := withdrawWaitInvite(c.App, w.Invite); err != nil {
			return nil, err
		}
	}
	if err := waitExec(c.App, "UPDATE drop_waitlist_mail SET state='cancelled' WHERE entry={:id} AND kind='welcome' AND state='queued'", dbx.Params{"id": id}); err != nil {
		return nil, err
	}
	if action != "revoke" {
		if err := waitQueue(c.App, id, "welcome", now); err != nil {
			return nil, err
		}
	}
	c.Event(Event{Detail: map[string]any{"entry": id, "invite": w.Invite}})
	return map[string]bool{"ok": true}, nil
}
