// Package appconfig constructs the Drop runtime on pocketcontext-core from
// Drop's policy (internal/droppolicy) and hooks (internal/dropserver).
package appconfig

import (
	"net/http"
	"strings"

	"github.com/pocketcontext-labs/pocketcontext-core/appidentity"
	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
	"github.com/pocketcontext-labs/pocketcontext-core/profiles"

	assets "github.com/pocketcontext-labs/pocketcontext-drop"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/buildinfo"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droppolicy"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
)

// Identity is Drop's validated identity.
var Identity appidentity.Identity = droppolicy.Identity

// Runtime is the one validated runtime of this process.
var Runtime = mustRuntime()

// Profiles is this application's private profile registry.
var Profiles *profiles.Store = Runtime.Profiles()

func mustRuntime() *appserver.Runtime {
	rt, err := appserver.New(appserver.Config{
		Identity: droppolicy.Identity, Version: buildinfo.Version, Commit: buildinfo.Commit, Assets: assets.Assets,
		Policy: droppolicy.Policy(), Hooks: dropserver.Hooks(), Text: droppolicy.Text,
	})
	if err != nil {
		panic(err)
	}
	return rt
}

// ValidatedMCPPublicRequest identifies a request that passed the configured
// public host and origin boundary. A client or forwarded header cannot supply it.
func ValidatedMCPPublicRequest(r *http.Request) bool {
	origin, ok := appserver.PublicOrigin(r)
	if !ok || !appserver.IsPublicRequest(r) {
		return false
	}
	if "https://"+strings.TrimSuffix(r.Host, ":443") != origin {
		return false
	}
	return r.Header.Get("Origin") == "" || r.Header.Get("Origin") == origin
}
