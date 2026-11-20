// Package dropserver is Drop's server-side product code: the hooks core calls
// at each point the server diverges, the account rules (accounts.go) and the
// operation framework behind POST /api/drop/op (ops.go), with its operations
// (ops_*.go): accounts and invites, apps, their secrets and releases, and the
// one-time release upload route (upload.go), nodes and placements
// (ops_nodes.go) and the agent routes (agent_routes.go, reports.go).
package dropserver

import (
	"context"
	"path/filepath"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/pocketcontext/server"
)

// Hooks returns the product code at each point the server diverges.
func Hooks() appserver.Hooks {
	return appserver.Hooks{
		PreRegister: func(app core.App, _ *appserver.Project, opts appserver.ServeOptions) error {
			rememberAccounts(app, opts)
			takeServices(app)
			return nil
		},
		Register: func(app core.App, ctx appserver.RegisterContext) error {
			// The SQL and search reads (owner's local key only; never public).
			server.RegisterDatabase(app, filepath.Join(ctx.AppDir, "server.json"), ctx.LivePath)
			bindAccounts(app)
			app.OnServe().BindFunc(func(e *core.ServeEvent) error {
				registerOpRoutes(e)
				registerUploadRoute(e)
				registerAgentRoutes(e)
				return e.Next()
			})
			return nil
		},
		// After core has checked or created the owner.
		OnServeStart: func(e *core.ServeEvent, _ *appserver.Project) error {
			return ensureOwnerAdmin(e.App)
		},
		Background: func(ctx context.Context, app core.App) {
			var workers sync.WaitGroup
			workers.Go(func() { runSecretCleanup(ctx, app) })
			workers.Go(func() { runUploadLinkPruning(ctx, app) })
			runOpKeyPruning(ctx, app)
			workers.Wait()
		},
		// --hosted-restore: core rotates the accounts' token keys; Drop adds
		// the nodes' (restore.go).
		OnHostedRestore: InvalidateNodeTokens,
		AllowSignIn:     AllowSignIn,
	}
}
