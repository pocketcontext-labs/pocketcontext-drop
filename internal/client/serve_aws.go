package client

import (
	"context"
	"log"
	"os"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
)

// serveStores holds the release and secret stores opened by serve. The server
// side (apps, secrets and releases, milestones M5a and M5b) reads it; until then
// serve only opens and checks them.
var serveStores *awsx.Stores

// openServeStores opens the stores for serve. --fake-aws DIR keeps releases and
// secrets in a local directory for testing, and only with a loopback listen
// address. Hosted mode without it uses AWS through the default credential
// chain (docs/aws.md); plain local serve without it opens nothing.
func openServeStores(a *args, hosted bool, listenAddr string) (*awsx.Stores, error) {
	if a.has("fake-aws") {
		dir := a.flags["fake-aws"]
		if dir == "" {
			return nil, fail(2, "--fake-aws needs a directory")
		}
		if err := awsx.CheckFakeAllowed(listenAddr); err != nil {
			return nil, fail(2, "%v", err)
		}
		stores, err := awsx.OpenFakeDir(dir)
		if err != nil {
			return nil, fail(1, "--fake-aws: %v", err)
		}
		log.Printf("drop: using %s", stores.Description)
		return stores, nil
	}
	if !hosted {
		return nil, nil
	}
	cfg, err := awsx.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, fail(2, "%v", err)
	}
	stores, err := awsx.OpenAWS(context.Background(), cfg)
	if err != nil {
		return nil, fail(1, "%v", err)
	}
	return stores, nil
}
