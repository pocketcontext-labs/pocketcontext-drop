// Package droppolicy holds what makes this server Drop rather than another
// application of pocketcontext-core: its identity, public route allowlist,
// maintenance reads, budgets and copy. It carries no runtime state.
// tests/identity_check.py checks that no identity field carries the
// template's value.
package droppolicy

import (
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appidentity"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/maintenance"
	"github.com/pocketcontext-labs/pocketcontext-core/mcpobserve"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
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

// Text is the copy shown to people. Hosted accounts are all named AccountName
// by core, so the reader shows email addresses, never account names.
var Text = appserver.Text{
	AccountName:        "Drop",
	LegacyRecreateHint: "Use the release that created it, or start a new project with `drop init --db NEW_PATH`",
	OAuthResourceName:  "Drop",
	OAuthConsent: appserver.OAuthConsentText{
		ConnectionsHTML: `<p>These apps can use Drop's tools with your account. Revoke access to disconnect an app.</p>`,
		RequestHTML:     `<p><strong>{{.Name}}</strong> is asking to use Drop's tools with your account.</p><p>It gets only what your account may do. It does not get server administration, shell or file access.</p>`,
		// Unused while Page.SignInPath and Page.ConnectionsPath replace both
		// places core would show it; kept so the plain pages still explain sign-in.
		SignInHTML: `If you are not signed in, <a href="/" target="_blank" rel="noopener">open {{.Product}}</a> in another tab of this browser, sign in with your email, then return here.`,
		Page: appserver.OAuthPage{
			SignInPath:      "/#/signin",
			ConnectionsPath: "/#/connect",
		},
	},
	BudgetExhausted: "Drop request budget exhausted. Retry after the indicated delay.",
	OTPSubject:      "Log in to Drop",
	OTPSenderName:   "Drop",
	OTPLoginPath:    "/#/login", // the reader route of the link in sign-in emails
	OTPCodeEmail:    signInCodeEmail,
}

// OAuthScope is the one scope of the MCP endpoint. Connected clients store it,
// so it must never change after the first release.
const OAuthScope = "drop"

// HostedBudget is one hosted account's request budget. Clients may match the
// label in 429 responses, so it must never change after the first release.
var HostedBudget = appserver.HostedBudget{Label: "drop_requests", Burst: 600, PerSecond: 50}

// ReleaseMaxBytes caps one release upload (90 MiB, dropapi.MaxReleaseBytes):
// Cloudflare limits request bodies to 100 MB, and the public body limit sits
// just above it.
const ReleaseMaxBytes = dropapi.MaxReleaseBytes

// Observe lists the diagnostic labels of the MCP tools and the API route they
// call; anything else is reported as unknown. Diagnostics carry these fixed
// labels, statuses and counts only: never an argument, an operation's input
// or result, or an error message (a secret.set value never reaches them).
var Observe = mcpobserve.Policy{
	Tools:     []string{"drop_read", "drop_write"},
	APIRoutes: []string{"/api/drop/op"},
}

// Policy returns the accounts mode, public routes, maintenance reads, budgets
// and observability lists.
func Policy() appserver.Policy {
	return appserver.Policy{
		// Several accounts beside the owner; hosted mode only. Local serving
		// keeps exactly one account and is meant for developing the reader.
		Accounts:         appserver.AccountsMulti,
		AllowPublicUIDev: true,
		PublicRoute:      func(core.App) func(method, path string) bool { return PublicRoute },
		// The SQL and search reads stay usable with the owner's local key only:
		// they read whole tables and are never public.
		Maintenance: maintenance.Policy{
			ReadPosts: map[string]bool{"/api/context/query": true, "/api/context/search": true, "/api/files/token": true},
			ReadGets:  map[string]bool{"/api/context/schema": true},
		},
		Observe:         Observe,
		OAuthScope:      OAuthScope,
		PublicBodyLimit: ReleaseMaxBytes + 2<<20,
		// Drop has no earlier schemas. A database whose migrations this
		// release does not ship is refused regardless.
		Legacy: appserver.LegacySchema{Migrations: []string{}, Tables: []string{}},
		Hosted: HostedBudget,
		// Hosted sign-ins last 30 days; the reader renews the token while in use.
		HostedSession: 30 * 24 * time.Hour,
	}
}
