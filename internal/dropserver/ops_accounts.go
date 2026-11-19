package dropserver

import (
	"database/sql"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droppolicy"
)

// Account and invite operations (M3).

func init() {
	register("me", func(c *Ctx, _ *dropapi.MeInput) (any, error) { return me(c), nil })
	register("user.list", userList)
	register("user.set", userSet)
	register("invite.create", inviteCreate)
	register("invite.list", inviteList)
	register("invite.revoke", inviteRevoke)
}

func me(c *Ctx) dropapi.Me {
	role := dropapi.Role(c.Auth.GetString("role"))
	if c.Admin {
		role = dropapi.RoleAdmin
	}
	return dropapi.Me{ID: c.Auth.Id, Email: c.Auth.Email(), Role: role, Admin: c.Admin, Owner: c.Owner, AppQuota: c.Auth.GetInt("app_quota")}
}

func userOut(app core.App, r *core.Record) dropapi.User {
	return dropapi.User{
		ID: r.Id, Email: r.Email(), Role: dropapi.Role(r.GetString("role")), AppQuota: r.GetInt("app_quota"),
		Suspended: r.GetBool("suspended"), Owner: isOwner(app, r), SignedIn: r.Verified(),
		InvitedBy: r.GetString("invited_by"), Created: r.GetDateTime("created").String(),
	}
}

func inviteOut(r *core.Record) dropapi.Invite {
	state := dropapi.InviteStateOpen
	switch {
	case !r.GetDateTime("revoked_at").IsZero():
		state = dropapi.InviteStateRevoked
	case !r.GetDateTime("used_at").IsZero():
		state = dropapi.InviteStateUsed
	case !r.GetDateTime("expires_at").Time().After(time.Now()):
		state = dropapi.InviteStateExpired
	}
	date := func(field string) string {
		d := r.GetDateTime(field)
		if d.IsZero() {
			return ""
		}
		return d.String()
	}
	return dropapi.Invite{
		ID: r.Id, Email: r.GetString("email"), Role: dropapi.Role(r.GetString("role")), AppQuota: r.GetInt("app_quota"),
		State: state, InvitedBy: r.GetString("invited_by"), ExpiresAt: date("expires_at"), UsedAt: date("used_at"),
		UsedBy: r.GetString("used_by"), RevokedAt: date("revoked_at"), Created: date("created"),
	}
}

// page checks a limit and offset; zero limit means the default.
func page(limit, offset int) (int, int, error) {
	if limit == 0 {
		limit = dropapi.DefaultListLimit
	}
	if limit < 1 || limit > dropapi.MaxListLimit {
		return 0, 0, Invalid("limit", "limit must be 1-500.")
	}
	if offset < 0 || offset > 10000 {
		return 0, 0, Invalid("offset", "offset must be 0-10000.")
	}
	return limit, offset, nil
}

func userList(c *Ctx, in *dropapi.UserListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	records, err := c.App.FindRecordsByFilter("users", "", "-created,-id", limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := dropapi.UserList{Users: []dropapi.User{}, HasMore: len(records) > limit}
	for i, r := range records {
		if i == limit {
			break
		}
		out.Users = append(out.Users, userOut(c.App, r))
	}
	return out, nil
}

// findUser finds an account by record id or email address.
func findUser(app core.App, ref string) (*core.Record, error) {
	ref = strings.TrimSpace(ref)
	if recordID.MatchString(ref) {
		if r, err := app.FindRecordById("users", ref); err == nil {
			return r, nil
		}
	}
	if strings.Contains(ref, "@") {
		var records []*core.Record
		err := app.RecordQuery("users").AndWhere(dbx.NewExp("LOWER([[email]]) = {:email}", dbx.Params{"email": lowerASCII(ref)})).Limit(2).All(&records)
		if err != nil {
			return nil, err
		}
		if len(records) == 1 {
			return records[0], nil
		}
	}
	return nil, NotFound("Account")
}

func validRole(role dropapi.Role) bool { return role == dropapi.RoleAdmin || role == dropapi.RoleUser }

func userSet(c *Ctx, in *dropapi.UserSetInput) (any, error) {
	if in.User == "" {
		return nil, Invalid("user", "Name the account by id or email.")
	}
	if in.Role == nil && in.AppQuota == nil && in.Suspended == nil {
		return nil, Invalid("user", "Change at least one of role, app_quota or suspended.")
	}
	target, err := findUser(c.App, in.User)
	if err != nil {
		return nil, err
	}
	self, owner := target.Id == c.Auth.Id, isOwner(c.App, target)
	changes := map[string]any{}
	if in.Role != nil {
		if !validRole(*in.Role) {
			return nil, Invalid("role", "role is admin or user.")
		}
		from := dropapi.Role(target.GetString("role"))
		if *in.Role != from {
			if self {
				return nil, Forbidden("You cannot change your own role.")
			}
			if owner {
				return nil, Forbidden("The owner is always an admin.")
			}
			target.Set("role", string(*in.Role))
			changes["role"] = map[string]any{"from": from, "to": *in.Role}
		}
	}
	if in.AppQuota != nil {
		if *in.AppQuota < 0 || *in.AppQuota > dropapi.MaxAppQuota {
			return nil, Invalid("app_quota", "app_quota must be 0-100.")
		}
		if from := target.GetInt("app_quota"); *in.AppQuota != from {
			target.Set("app_quota", *in.AppQuota)
			changes["app_quota"] = map[string]any{"from": from, "to": *in.AppQuota}
		}
	}
	if in.Suspended != nil && *in.Suspended != target.GetBool("suspended") {
		if self {
			return nil, Forbidden("You cannot suspend your own account.")
		}
		if owner {
			return nil, Forbidden("The owner cannot be suspended.")
		}
		target.Set("suspended", *in.Suspended)
		if *in.Suspended {
			// Every existing session of the account stops working at once.
			target.RefreshTokenKey()
		}
		changes["suspended"] = map[string]any{"from": !*in.Suspended, "to": *in.Suspended}
	}
	if len(changes) > 0 {
		if err := c.App.Save(target); err != nil {
			return nil, err
		}
	}
	c.Event(Event{SubjectUser: target.Id, Detail: map[string]any{"email": target.Email(), "changes": changes}})
	return dropapi.UserResult{User: userOut(c.App, target)}, nil
}

// canonicalEmail checks an invited address: printable ASCII (core accepts no
// other sign-in address), one plain address, at most 254 characters,
// lower-cased.
func canonicalEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if email == "" || len(email) > dropapi.MaxEmailLength {
		return "", Invalid("email", "Enter an email address of at most 254 characters.")
	}
	for i := 0; i < len(email); i++ {
		if email[i] < 0x21 || email[i] > 0x7e {
			return "", Invalid("email", "An email address here uses plain ASCII letters, digits and symbols, without spaces.")
		}
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || parsed.Name != "" || !strings.Contains(email, "@") {
		return "", Invalid("email", "Enter one plain email address, like name@example.com.")
	}
	email = lowerASCII(email)
	if email == lowerASCII(droppolicy.Identity.LocalAccountEmail) {
		return "", Invalid("email", "That address is reserved.")
	}
	return email, nil
}

// publicURL is the origin people open: the hosted domain, or locally the
// address the request came to.
func (c *Ctx) publicURL() string {
	if accountsOf(c.App).hosted {
		if u := strings.TrimRight(c.App.Settings().Meta.AppURL, "/"); u != "" {
			return u
		}
	}
	return "http://" + c.Request.Host
}

// SignInURL is the reader's sign-in page with an address filled in. It
// carries no credential.
func SignInURL(base, email string) string {
	return base + "/#/invite?email=" + url.QueryEscape(email)
}

func inviteCreate(c *Ctx, in *dropapi.InviteCreateInput) (any, error) {
	email, err := canonicalEmail(in.Email)
	if err != nil {
		return nil, err
	}
	role := in.Role
	if role == "" {
		role = dropapi.RoleUser
	}
	if !validRole(role) {
		return nil, Invalid("role", "role is admin or user.")
	}
	quota := dropapi.DefaultAppQuota
	if in.AppQuota != nil {
		quota = *in.AppQuota
	}
	if quota < 0 || quota > dropapi.MaxAppQuota {
		return nil, Invalid("app_quota", "app_quota must be 0-100.")
	}
	days := in.ExpiresDays
	if days == 0 {
		days = dropapi.DefaultInviteDays
	}
	if days < 1 || days > dropapi.MaxInviteDays {
		return nil, Invalid("expires_days", "expires_days must be 1-30.")
	}
	var notFound *dropapi.Error
	if _, err := findUser(c.App, email); err == nil {
		return nil, Conflict(dropapi.CodeAccountExists, "This address already has an account.")
	} else if !errors.As(err, &notFound) || notFound.Code != dropapi.CodeNotFound {
		return nil, err
	}
	// The open invite of the address, expired or not (the unique index allows one).
	open, err := c.App.FindFirstRecordByFilter("invites", "email_lower = {:email} && used_at = '' && revoked_at = ''", dbx.Params{"email": email})
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case open.GetDateTime("expires_at").Time().After(time.Now()):
		return nil, Conflict(dropapi.CodeInviteExists, "This address already has an open invite. Revoke it to invite again.")
	default:
		// An expired invite is closed so the new one can be open.
		open.Set("revoked_at", types.NowDateTime())
		if err := c.App.Save(open); err != nil {
			return nil, err
		}
	}
	collection, err := c.App.FindCachedCollectionByNameOrId("invites")
	if err != nil {
		return nil, err
	}
	invite := core.NewRecord(collection)
	invite.Set("email", email)
	invite.Set("email_lower", email)
	invite.Set("role", string(role))
	invite.Set("app_quota", quota)
	invite.Set("invited_by", c.Auth.Id)
	invite.Set("expires_at", time.Now().UTC().Add(time.Duration(days)*24*time.Hour))
	if err := c.App.Save(invite); err != nil {
		return nil, err
	}
	out := inviteOut(invite)
	c.Event(Event{Detail: map[string]any{"invite": invite.Id, "email": email, "role": role, "app_quota": quota, "expires_at": out.ExpiresAt}})
	return dropapi.InviteCreateResult{Invite: out, SignInURL: SignInURL(c.publicURL(), email)}, nil
}

func inviteList(c *Ctx, in *dropapi.InviteListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	filter := ""
	switch in.State {
	case "", dropapi.InviteListFilterAll:
	case dropapi.InviteListFilterOpen:
		filter = "used_at = '' && revoked_at = '' && expires_at > @now"
	default:
		return nil, Invalid("state", "state is all or open.")
	}
	records, err := c.App.FindRecordsByFilter("invites", filter, "-created,-id", limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := dropapi.InviteList{Invites: []dropapi.Invite{}, HasMore: len(records) > limit}
	for i, r := range records {
		if i == limit {
			break
		}
		out.Invites = append(out.Invites, inviteOut(r))
	}
	return out, nil
}

func inviteRevoke(c *Ctx, in *dropapi.InviteRevokeInput) (any, error) {
	if !recordID.MatchString(in.Invite) {
		return nil, NotFound("Invite")
	}
	invite, err := c.App.FindRecordById("invites", in.Invite)
	if err != nil {
		return nil, NotFound("Invite")
	}
	if !invite.GetDateTime("used_at").IsZero() || !invite.GetDateTime("revoked_at").IsZero() {
		return nil, Conflict(dropapi.CodeInviteClosed, "Only an open invite can be revoked; suspend the account instead.")
	}
	invite.Set("revoked_at", types.NowDateTime())
	if err := c.App.Save(invite); err != nil {
		return nil, err
	}
	c.Event(Event{Detail: map[string]any{"invite": invite.Id, "email": invite.GetString("email")}})
	return dropapi.InviteResult{Invite: inviteOut(invite)}, nil
}
