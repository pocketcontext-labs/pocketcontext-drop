// Package dropserver is Drop's server-side product code: the hooks core calls
// at each point the server diverges, the account rules (accounts.go) and the
// operation framework behind POST /api/drop/op (ops.go), with its operations
// (ops_*.go). Later milestones add the agent routes.
package dropserver

import (
	"context"
	"path/filepath"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/pocketcontext/server"
)

// Hooks returns the product code at each point the server diverges.
func Hooks() appserver.Hooks {
	return appserver.Hooks{
		PreRegister: func(app core.App, _ *appserver.Project, opts appserver.ServeOptions) error {
			rememberAccounts(app, opts)
			return nil
		},
		Register: func(app core.App, ctx appserver.RegisterContext) error {
			// The SQL and search reads (owner's local key only; never public).
			server.RegisterDatabase(app, filepath.Join(ctx.AppDir, "server.json"), ctx.LivePath)
			bindAppOwnership(app)
			bindAccounts(app)
			app.OnServe().BindFunc(func(e *core.ServeEvent) error {
				registerOpRoutes(e)
				return e.Next()
			})
			return nil
		},
		// After core has checked or created the owner.
		OnServeStart: func(e *core.ServeEvent, _ *appserver.Project) error {
			return ensureOwnerAdmin(e.App)
		},
		Background: func(ctx context.Context, app core.App) {
			runOpKeyPruning(ctx, app)
		},
		AllowSignIn: AllowSignIn,
	}
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
