// Package dropserver is Drop's server-side product code: the hooks core calls
// at each point the server diverges, and (in later milestones) Drop's
// operations, agent routes and account rules.
package dropserver

import (
	"path/filepath"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/pocketcontext/server"
)

// Hooks returns the product code at each point the server diverges.
func Hooks() appserver.Hooks {
	return appserver.Hooks{
		Register: func(app core.App, ctx appserver.RegisterContext) error {
			// The SQL and search reads (owner's local key only; never public).
			server.RegisterDatabase(app, filepath.Join(ctx.AppDir, "server.json"), ctx.LivePath)
			bindAppOwnership(app)
			return nil
		},
		AllowSignIn: AllowSignIn,
	}
}

// AllowSignIn decides whether a hosted sign-in for an address without an
// account creates one. Core asks it for every address that passes its limits,
// existing accounts (the owner included) too, and ignores the answer for
// those, so the owner always signs in.
//
// TODO(M3): allow an address that holds an open, unexpired invite. Until
// invites exist nobody but the owner can sign in, so no other account is
// ever created.
func AllowSignIn(core.App, string) (bool, error) {
	return false, nil
}

// bindAppOwnership makes every app belong to the account that created it. The
// apps rules show and change only the requester's own apps; a create or update
// request cannot choose or move the owner, whatever its body says. Saves
// without a request (the local administration paths) keep what they set.
func bindAppOwnership(app core.App) {
	app.OnRecordCreateRequest("apps").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil || e.Auth.Collection().Name != "users" {
			return e.ForbiddenError("Sign in to add an app.", nil)
		}
		e.Record.Set("owner", e.Auth.Id)
		return e.Next()
	})
	app.OnRecordUpdateRequest("apps").BindFunc(func(e *core.RecordRequestEvent) error {
		e.Record.Set("owner", e.Record.Original().GetString("owner"))
		return e.Next()
	})
}
