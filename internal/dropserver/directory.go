package dropserver

import (
	"slices"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() { register("user.directory", userDirectory) }

type directoryRow struct {
	Developer bool   `db:"developer" json:"developer"`
	Owner     bool   `db:"owner" json:"owner"`
	Email     string `db:"email" json:"email"`
	Account   string `db:"account" json:"account"`
	Entry     string `db:"entry" json:"entry"`
	Invite    string `db:"invite" json:"invite"`
	Status    string `db:"status" json:"status"`
	Role      string `db:"role" json:"role"`
	Spaces    int    `db:"spaces" json:"workspaces"`
	Credits   int64  `db:"credits" json:"microcredits"`
	Joined    string `db:"joined" json:"joined"`
	Total     int    `db:"total" json:"-"`
}

func userDirectory(c *Ctx, in *dropapi.DirectoryInput) (any, error) {
	limit, _, err := page(in.Limit, 0)
	if err != nil {
		return nil, err
	}
	if in.Offset < 0 || len(in.Search) > 254 || !slices.Contains([]string{"", "active", "suspended", "waiting", "invited", "revoked", "expired"}, in.Status) || !slices.Contains([]string{"", "admin", "developer", "user"}, in.Role) || !slices.Contains([]string{"", "yes", "no"}, in.Workspaces) || !slices.Contains([]string{"", "yes", "no"}, in.Credits) {
		return nil, Invalid("filters", "Choose a supported directory filter.")
	}
	// Include direct invites, established accounts without waitlist records,
	// and verified applicants. Unverified mailbox challenges stay private.
	rows := []directoryRow{}
	err = c.App.DB().NewQuery(`WITH emails(email) AS (
	SELECT lower(email) FROM users WHERE verified=1 UNION SELECT email_lower FROM invites
	UNION SELECT email FROM drop_waitlist WHERE verified_at!=''),
	latest AS (SELECT id,email_lower,role,revoked_at,expires_at,created,
	row_number() OVER(PARTITION BY email_lower ORDER BY created DESC,id DESC) AS n FROM invites),
	workspaces AS (SELECT owner,COUNT(*) AS spaces FROM apps WHERE developer_private=0 OR owner={:viewer} GROUP BY owner),
	directory AS (SELECT e.email,COALESCE(u.id,'') account,COALESCE(w.id,'') entry,COALESCE(i.id,'') invite,
	CASE WHEN u.suspended=1 THEN 'suspended' WHEN u.id IS NOT NULL THEN 'active' WHEN i.revoked_at!='' THEN 'revoked'
	WHEN i.expires_at<=strftime('%Y-%m-%d %H:%M:%fZ','now') THEN 'expired' WHEN i.id IS NOT NULL THEN 'invited' ELSE 'waiting' END status,
	COALESCE(u.role,i.role,'user') role,COALESCE(u.role,i.role,'user')='developer' developer,COALESCE(s.spaces,0) spaces,COALESCE(wallet.balance,0) credits,
	COALESCE(w.verified_at,u.created,i.created) joined FROM emails e
	LEFT JOIN users u ON u.email=e.email COLLATE NOCASE AND u.verified=1
	LEFT JOIN drop_waitlist w ON w.email=e.email AND w.verified_at!=''
	LEFT JOIN latest i ON i.email_lower=e.email AND i.n=1
	LEFT JOIN workspaces s ON s.owner=u.id LEFT JOIN drop_credit_accounts wallet ON wallet.owner=u.id AND u.role!='admin')
	SELECT *,email={:ownerEmail} owner,COUNT(*) OVER() total FROM directory WHERE (instr(email,{:search}) OR instr(account,{:search}))
	AND (status={:status} OR {:status}='') AND (role={:role} OR {:role}='')
	AND ({:spaces}='' OR (spaces>0)=({:spaces}='yes')) AND ({:funds}='' OR (credits>0)=({:funds}='yes'))
	ORDER BY joined,email LIMIT {:limit} OFFSET {:offset}`).Bind(dbx.Params{"viewer": c.Auth.Id, "ownerEmail": accountsOf(c.App).owner, "search": strings.ToLower(strings.TrimSpace(in.Search)), "status": in.Status, "role": in.Role, "spaces": in.Workspaces, "funds": in.Credits, "limit": limit, "offset": in.Offset}).All(&rows)
	total := 0
	if len(rows) > 0 {
		total = rows[0].Total
	}
	return map[string]any{"users": rows, "total": total}, err
}
