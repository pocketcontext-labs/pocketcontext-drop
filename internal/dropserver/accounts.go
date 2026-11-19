package dropserver

import (
	"database/sql"
	"errors"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droppolicy"
)

// Accounts. Drop is invite-only (decision 3): core creates the owner
// (OWNER_EMAIL, on --hosted-init) and the local account (`drop init`), and
// any other account only when AllowSignIn admits its address, which needs an
// open (unused, unrevoked) and unexpired invite. Every account has a role
// (decision 4): the owner and the local account are admins; an invited
// account takes its invite's role and app quota.

// accountsStoreKey holds the accountsMode of one app in its store.
const accountsStoreKey = "dropAccounts"

// accountsMode is how the server was started: hosted with an owner address
// (canonical), or local with the local account.
type accountsMode struct {
	hosted bool
	owner  string
}

// rt is the runtime, for Runtime.IsOwner (UseRuntime).
var rt *appserver.Runtime

// UseRuntime gives the hooks the runtime they were built into. appconfig
// calls it right after appserver.New, before anything serves.
func UseRuntime(r *appserver.Runtime) { rt = r }

// isOwner reports whether record is the instance's owner: on a hosted
// instance the OWNER_EMAIL account, otherwise the local account.
func isOwner(app core.App, record *core.Record) bool {
	return rt != nil && rt.IsOwner(app, record)
}

// lowerASCII lower-cases ASCII letters only, as core and SQLite's LOWER do.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// rememberAccounts records the accounts mode before anything is created
// (Hooks.PreRegister). Core creates the owner before it records who the owner
// is, so the users create hook cannot ask Runtime.IsOwner yet.
func rememberAccounts(app core.App, opts appserver.ServeOptions) {
	mode := accountsMode{hosted: opts.Hosted}
	if opts.Hosted && opts.HostedConfig != nil {
		mode.owner = lowerASCII(strings.TrimSpace(opts.HostedConfig.OwnerEmail))
	}
	app.Store().Set(accountsStoreKey, mode)
}

func accountsOf(app core.App) accountsMode {
	mode, _ := app.Store().Get(accountsStoreKey).(accountsMode)
	return mode
}

// adminAddress reports whether an address is made an admin at creation: the
// hosted owner, or locally the local account.
func adminAddress(app core.App, email string) bool {
	mode := accountsOf(app)
	if mode.hosted {
		return mode.owner != "" && email == mode.owner
	}
	return email == lowerASCII(droppolicy.Identity.LocalAccountEmail)
}

// openInvite returns the open, unexpired invite of a canonical address, or nil.
func openInvite(app core.App, email string) (*core.Record, error) {
	record, err := app.FindFirstRecordByFilter("invites",
		"email_lower = {:email} && used_at = '' && revoked_at = '' && expires_at > @now", dbx.Params{"email": email})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

// AllowSignIn decides whether a hosted sign-in for an address without an
// account creates one: only an address with an open, unexpired invite. Core
// passes the canonical address (trimmed, lower-cased ASCII), asks for every
// address that passes its limits (existing accounts too, ignoring the answer)
// and answers every request the same way, so a refusal shows nothing.
func AllowSignIn(app core.App, email string) (bool, error) {
	invite, err := openInvite(app, lowerASCII(email))
	if err != nil {
		return false, err
	}
	return invite != nil, nil
}

// bindAccounts sets the role, quota and inviter of every new account and
// consumes its invite; it reopens the invite of an account core prunes
// because it never signed in. Model-level hooks, so they cover core's own
// account creation and every other save.
func bindAccounts(app core.App) {
	app.OnRecordCreate("users").BindFunc(func(e *core.RecordEvent) error {
		email := lowerASCII(strings.TrimSpace(e.Record.Email()))
		var invite *core.Record
		if adminAddress(e.App, email) {
			e.Record.Set("role", string(dropapi.RoleAdmin))
			e.Record.Set("app_quota", dropapi.DefaultAppQuota)
		} else {
			// Defence in depth: AllowSignIn already required the invite.
			found, err := openInvite(e.App, email)
			if err != nil {
				return err
			}
			if found == nil {
				return errors.New("an account needs an open invite")
			}
			invite = found
			e.Record.Set("role", invite.GetString("role"))
			e.Record.Set("app_quota", invite.GetInt("app_quota"))
			e.Record.Set("invited_by", invite.GetString("invited_by"))
		}
		e.Record.Set("suspended", false)
		e.Record.SetEmailVisibility(true)
		if err := e.Next(); err != nil {
			return err
		}
		if invite != nil {
			invite.Set("used_at", types.NowDateTime())
			invite.Set("used_by", e.Record.Id)
			if err := e.App.Save(invite); err != nil {
				// The account keeps the invite's role; the invite stays open
				// but cannot create a second account for the address.
				e.App.Logger().Error("Invite not marked used", "collection", "invites", "record", invite.Id)
			}
		}
		return nil
	})
	app.OnRecordDelete("users").BindFunc(func(e *core.RecordEvent) error {
		// Core deletes only accounts that never signed in (unverified). Give
		// their invite back, so the person can still sign in before it expires.
		if !e.Record.Verified() {
			if err := reopenInvites(e.App, e.Record); err != nil {
				return err
			}
		}
		return e.Next()
	})
}

func reopenInvites(app core.App, user *core.Record) error {
	invites, err := app.FindRecordsByFilter("invites", "used_by = {:id} && revoked_at = ''", "", 10, 0, dbx.Params{"id": user.Id})
	if err != nil {
		return err
	}
	for _, invite := range invites {
		other, err := openInvite(app, invite.GetString("email_lower"))
		if err != nil {
			return err
		}
		if other != nil {
			continue // a newer open invite exists; this one stays used
		}
		invite.Set("used_at", "")
		invite.Set("used_by", "")
		if err := app.Save(invite); err != nil {
			return err
		}
	}
	return nil
}

// ensureOwnerAdmin makes sure the owner (or locally the local account) has
// role admin, whatever an earlier release or a repair left behind.
func ensureOwnerAdmin(app core.App) error {
	mode := accountsOf(app)
	email := mode.owner
	if !mode.hosted {
		email = lowerASCII(droppolicy.Identity.LocalAccountEmail)
	}
	if email == "" {
		return nil
	}
	var records []*core.Record
	if err := app.RecordQuery("users").AndWhere(dbx.NewExp("LOWER([[email]]) = {:email}", dbx.Params{"email": email})).Limit(2).All(&records); err != nil {
		return err
	}
	for _, record := range records {
		if !isOwner(app, record) || record.GetString("role") == string(dropapi.RoleAdmin) {
			continue
		}
		record.Set("role", string(dropapi.RoleAdmin))
		if err := app.Save(record); err != nil {
			return err
		}
	}
	return nil
}
