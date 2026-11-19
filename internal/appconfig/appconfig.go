// Package appconfig holds everything that makes this server Drop
// rather than another application of pocketcontext-core: identity, public route
// allowlist, maintenance reads, copy and hooks. tests/identity_check.py checks
// that no identity field carries the template's value.
package appconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appidentity"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/maintenance"
	"github.com/pocketcontext-labs/pocketcontext-core/mcpobserve"
	"github.com/pocketcontext-labs/pocketcontext-core/profiles"

	assets "github.com/pocketcontext-labs/pocketcontext-drop"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/buildinfo"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/pocketcontext/server"
)

// Identity names this application everywhere. Its values are persisted (cache
// folders, profile registry, database and configuration files): never change one
// after the first release.
var Identity = appidentity.Identity{
	AppID:             "drop",
	DisplayName:       "Drop",
	Command:           "drop",
	SkillName:         "drop",
	Marker:            ".drop",
	RouterMarker:      ".drop-router",
	RegistryNamespace: "pocketcontext-drop",
	CacheRoot:         "pocketcontext-drop",
	SocketHost:        "drop.internal",
	SocketDirPrefix:   "drop",
	DefaultPort:       8100,
	DatabaseFile:      "drop.db",
	ConfigFile:        "drop.json",
	EnvPrefix:         "DROP",
	LocalKeyHeader:    "X-Drop-Local-Key",
	LocalAccountEmail: "account@drop.invalid", // persisted; nobody chooses or sees it, and .invalid never receives mail
	ReaderStorageKey:  "drop.reader.auth",
	HookGlobal:        "$drop",
}

// Text is the copy shown to people. The OAuth consent text is shown by public
// mode with --mcp; hosted mode (email sign-in, budgets) is not offered here.
var Text = appserver.Text{
	AccountName:        "Drop",
	LegacyRecreateHint: "Use the release that created it, or start a new project with `drop init --db NEW_PATH`",
	OAuthResourceName:  "Drop",
	OAuthConsent: appserver.OAuthConsentText{
		ConnectionsHTML: `<p>These apps can read, search, query and change this application's records through its MCP tools. Revoke access to disconnect an app.</p>`,
		RequestHTML:     `<p><strong>{{.Name}}</strong> is asking to read, search, query and change (including delete) this application's records.</p><p>This does not grant server administration, shell or file access.</p>`,
		// Shown on the consent and connected-apps pages to a person who is not signed in.
		SignInHTML: `If you are not signed in, open your fresh {{.Product}} reader sign-in link in another tab of this browser, then return here. Your setup agent can issue one with <code>open --public</code>. You can also <a href="/" target="_blank" rel="noopener">open the reader</a> and use your password if you set one.`,
	},
	BudgetExhausted: "Request budget exhausted. Retry after the indicated delay.",
	OTPSubject:      "Log in to Drop",
	OTPSenderName:   "Drop",
	OTPLoginPath:    "/#/login", // hosted sign-in email link; unused without hosted mode
}

// Observe lists the diagnostic labels of the MCP tools and the routes they call.
var Observe = mcpobserve.Policy{
	Tools:     strings.Fields("read search schema query write"),
	APIRoutes: strings.Fields("/api/context/schema /api/context/query /api/context/search /api/collections/{collection}/records /api/collections/{collection}/records/{id}"),
}

// Policy returns the public routes, maintenance reads and observability lists.
func Policy() appserver.Policy {
	return appserver.Policy{
		PublicRoute: PublicRoute,
		Maintenance: maintenance.Policy{
			ReadPosts: map[string]bool{"/api/context/query": true, "/api/context/search": true, "/api/files/token": true},
			ReadGets:  map[string]bool{"/api/context/schema": true},
		},
		Observe: Observe,
		// Connected MCP clients store the scope: never change it after the
		// first release.
		OAuthScope:      "drop",
		PublicBodyLimit: 110 << 20,
		// Drop has no earlier schemas. A database whose migrations this
		// release does not ship is refused regardless.
		Legacy: appserver.LegacySchema{Migrations: []string{}, Tables: []string{}},
		// Hosted stays zero: hosted mode is not offered here, and core refuses it.
	}
}

// Hooks returns the product code at each point the server diverges.
func Hooks() appserver.Hooks {
	return appserver.Hooks{
		PreRegister: func(app core.App, _ *appserver.Project, opts appserver.ServeOptions) error {
			if opts.HTTPSDomain == "" {
				return nil
			}
			cfg, err := LoadPublicConfig()
			if err != nil {
				return fmt.Errorf("public mode refused: %w", err)
			}
			app.OnServe().BindFunc(func(e *core.ServeEvent) error {
				if err := checkPublicReady(e.App, cfg); err != nil {
					return fmt.Errorf("public mode refused: %w", err)
				}
				return e.Next()
			})
			return nil
		},
		Register: func(app core.App, ctx appserver.RegisterContext) error {
			server.RegisterDatabase(app, filepath.Join(ctx.AppDir, "server.json"), ctx.LivePath)
			bindPublicGuards(app)
			return nil
		},
	}
}

// Runtime is the one validated runtime of this process.
var Runtime = mustRuntime()

// Profiles is this application's private profile registry.
var Profiles *profiles.Store = Runtime.Profiles()

func mustRuntime() *appserver.Runtime {
	rt, err := appserver.New(appserver.Config{
		Identity: Identity, Version: buildinfo.Version, Commit: buildinfo.Commit, Assets: assets.Assets,
		Policy: Policy(), Hooks: Hooks(), Text: Text,
	})
	if err != nil {
		panic(err)
	}
	return rt
}
