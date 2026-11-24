package client

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
)

// EnvAppDomain names the domain under which apps get their hostnames.
const EnvAppDomain = "DROP_APP_DOMAIN"

// EnvReleaseMaxBytes optionally lowers the release upload cap below 90 MiB.
const EnvReleaseMaxBytes = "DROP_RELEASE_MAX_BYTES"

// serveServices opens the stores for serve and returns what the server's
// operations get: only the secret store's write side (the server never reads
// a secret), its SSM prefix, the release store's write side, the release cap
// (DROP_RELEASE_MAX_BYTES, optional, at most 90 MiB) and the app domain
// (DROP_APP_DOMAIN, optional).
func serveServices(a *args, hosted bool, listenAddr string, getenv func(string) string) (dropserver.Services, error) {
	s := dropserver.Services{CrewReplicaBucket: getenv("DROP_CREW_REPLICA_BUCKET"), AWSRegion: getenv(awsx.EnvRegion)}
	if domain := getenv(EnvAppDomain); domain != "" {
		if !dropserver.ValidAppDomain(domain) {
			return s, fail(2, "%s must be a lower-case domain name such as apps.example.com", EnvAppDomain)
		}
		s.AppDomain = domain
	}
	if raw := getenv(EnvReleaseMaxBytes); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1<<20 || n > dropapi.MaxReleaseBytes {
			return s, fail(2, "%s must be a number of bytes from 1048576 to %d (90 MiB)", EnvReleaseMaxBytes, dropapi.MaxReleaseBytes)
		}
		s.ReleaseMaxBytes = n
	}
	stores, err := openServeStores(a, hosted, listenAddr)
	if err != nil || stores == nil {
		return s, err
	}
	s.Secrets, s.SSMPrefix, s.Releases = stores.Secrets, stores.SSMPrefix, stores.Blobs
	return s, nil
}

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
