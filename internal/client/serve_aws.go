package client

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dnsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropserver"
)

// EnvAppDomain names the domain under which apps get their hostnames.
const EnvAppDomain = "DROP_APP_DOMAIN"

// EnvDNSZoneID and EnvDNSTokenFile turn on per-app DNS records: the
// Cloudflare zone of DROP_APP_DOMAIN and a file holding an API token with
// Zone DNS Edit on it (a systemd credential, for example).
const (
	EnvDNSZoneID    = "DROP_DNS_ZONE_ID"
	EnvDNSTokenFile = "DROP_DNS_TOKEN_FILE"
)

// EnvReleaseMaxBytes optionally lowers the release upload cap below 90 MiB.
const EnvReleaseMaxBytes = "DROP_RELEASE_MAX_BYTES"

// serveServices opens the stores for serve and returns what the server's
// operations get: only the secret store's write side (the server never reads
// a secret), its SSM prefix, the release store's write side, the release cap
// (DROP_RELEASE_MAX_BYTES, optional, at most 90 MiB) and the app domain
// (DROP_APP_DOMAIN, optional).
func serveServices(a *args, hosted bool, listenAddr string, getenv func(string) string) (dropserver.Services, error) {
	s := dropserver.Services{CrewReplicaBucket: getenv("DROP_CREW_REPLICA_BUCKET"), AWSRegion: getenv(awsx.EnvRegion)}
	s.BetaLegal = dropserver.BetaLegal{TermsURL: getenv("DROP_TERMS_URL"), PrivacyURL: getenv("DROP_PRIVACY_URL"), TermsVersion: getenv("DROP_TERMS_VERSION")}
	assigned := map[string]bool{}
	for _, pool := range []struct {
		variable string
		nodes    *[]string
	}{{"DROP_BETA_NODES", &s.BetaNodes}, {"DROP_BETA_OWNER_NODES", &s.BetaOwnerNodes}} {
		if raw := getenv(pool.variable); raw != "" {
			for _, entry := range strings.Split(raw, ",") {
				name := strings.TrimSpace(entry)
				if !dropapi.NodeNamePattern.MatchString(name) || assigned[name] {
					return s, fail(2, "%s requires distinct node names, each in only one Beta pool", pool.variable)
				}
				assigned[name] = true
				*pool.nodes = append(*pool.nodes, name)
			}
		}
	}
	if domain := getenv(EnvAppDomain); domain != "" {
		if !dropserver.ValidAppDomain(domain) {
			return s, fail(2, "%s must be a lower-case domain name such as apps.example.com", EnvAppDomain)
		}
		s.AppDomain = domain
	}
	if zone, tokenFile := getenv(EnvDNSZoneID), getenv(EnvDNSTokenFile); zone != "" || tokenFile != "" {
		if s.AppDomain == "" || zone == "" || tokenFile == "" {
			return s, fail(2, "%s and %s go together and need %s", EnvDNSZoneID, EnvDNSTokenFile, EnvAppDomain)
		}
		cf, err := dnsx.NewCloudflare(zone, tokenFile)
		if err != nil {
			return s, fail(2, "%v", err)
		}
		s.DNS = cf
	}
	if raw := getenv(EnvReleaseMaxBytes); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1<<20 || n > dropapi.MaxReleaseBytes {
			return s, fail(2, "%s must be a number of bytes from 1048576 to %d (90 MiB)", EnvReleaseMaxBytes, dropapi.MaxReleaseBytes)
		}
		s.ReleaseMaxBytes = n
	}
	usageCfg, err := awsx.UsageConfigFromEnv(getenv, s.AWSRegion, s.CrewReplicaBucket)
	if err != nil {
		return s, fail(2, "%v", err)
	}
	stores, err := openServeStores(a, hosted, listenAddr)
	if err != nil || stores == nil {
		if err == nil && usageCfg.Enabled() {
			log.Printf("drop: usage metering needs hosted mode or --fake-aws; it is off")
		}
		return s, err
	}
	s.Secrets, s.SSMPrefix, s.Releases = stores.Secrets, stores.SSMPrefix, stores.Blobs
	if stores.Fake {
		s.Backups, err = awsx.NewDirBlobStore(filepath.Join(a.flags["fake-aws"], "backups"))
	} else if bucket := getenv("DROP_BACKUP_BUCKET"); bucket != "" {
		if bucket == getenv(awsx.EnvBucket) || bucket == s.CrewReplicaBucket {
			return s, fail(2, "DROP_BACKUP_BUCKET must be a dedicated control-plane bucket")
		}
		cfg := awsx.Config{Region: s.AWSRegion, Bucket: bucket, SSMPrefix: s.SSMPrefix}
		var bs *awsx.Stores
		bs, err = awsx.OpenAWS(context.Background(), cfg)
		if err == nil {
			s.Backups = bs.Blobs.(*awsx.S3BlobStore)
		}
	}
	if err != nil {
		return s, err
	}
	if usageCfg.Enabled() {
		s.UsageConfig = usageCfg
		if stores.Fake {
			s.Usage = stores.Usage
		} else {
			store, err := awsx.OpenUsageAWS(context.Background(), usageCfg)
			if err != nil {
				return s, fail(2, "%v", err)
			}
			s.Usage = store
		}
		log.Printf("drop: usage metering on (log bucket %s, prefix %s)", usageCfg.LogBucket, usageCfg.LogPrefix)
	}
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
