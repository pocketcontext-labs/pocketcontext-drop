package dropserver

import (
	"context"
	"net/http"
	"regexp"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Services are what the server's operations use outside the database.
//
// The server holds only write access to secrets: Secrets is an
// awsx.SecretWriter (Put and Delete), never a reader, so no operation can
// read a value back (TestServerNeverReadsSecrets keeps it that way).
type Services struct {
	// Secrets stores app secret values in SSM, or a fake; nil means this
	// server has no secret store (plain local serve), and secret writes
	// answer 503 secrets_unavailable.
	Secrets awsx.SecretWriter
	// SSMPrefix is the parameter prefix (awsx.DefaultSSMPrefix when empty).
	SSMPrefix string
	// Releases stores uploaded release executables in S3, or a fake, by
	// content at awsx.ReleaseKey(sha256). The server only writes (and may
	// check that a release exists); nil means this server has no release
	// store (plain local serve), and upload links answer 503
	// releases_unavailable.
	Releases awsx.BlobWriter
	// ReleaseMaxBytes caps one release upload: dropapi.MaxReleaseBytes when
	// zero, and never more (DROP_RELEASE_MAX_BYTES may only lower it).
	ReleaseMaxBytes int64
	// AppDomain is the domain under which apps get their hostname,
	// <slug>.<AppDomain> (DROP_APP_DOMAIN); empty leaves new apps without a
	// hostname.
	AppDomain                    string
	CrewReplicaBucket, AWSRegion string
	// Usage reads the replica bucket's access logs and inventory and manages
	// its per-app request-metrics filters (usage.go); nil, or a UsageConfig
	// that is not Enabled, turns metering off.
	Usage       awsx.UsageStore
	UsageConfig awsx.UsageConfig
}

// metering reports whether the usage collector runs on this server.
func (s Services) metering() bool { return s.Usage != nil && s.UsageConfig.Enabled() }

// metricsFilters reports whether the server manages per-app request-metrics
// filters: only with metering on and a replica bucket.
func (s Services) metricsFilters() bool { return s.metering() && s.UsageConfig.ReplicaBucket != "" }

const servicesStoreKey = "dropServices"

var (
	pendingMu sync.Mutex
	pending   Services
)

// UseServices gives the next server this process starts its services. serve
// calls it after opening the stores and before appserver.Serve; PreRegister
// hands them to the app.
func UseServices(s Services) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pending = s
}

// SetServices sets the services of one app directly (tests).
func SetServices(app core.App, s Services) {
	app.Store().Set(servicesStoreKey, s.withDefaults())
}

func (s Services) withDefaults() Services {
	if s.SSMPrefix == "" {
		s.SSMPrefix = awsx.DefaultSSMPrefix
	}
	if s.ReleaseMaxBytes <= 0 || s.ReleaseMaxBytes > dropapi.MaxReleaseBytes {
		s.ReleaseMaxBytes = dropapi.MaxReleaseBytes
	}
	return s
}

// takeServices stores the pending services in a new app (Hooks.PreRegister).
func takeServices(app core.App) {
	pendingMu.Lock()
	s := pending
	pendingMu.Unlock()
	SetServices(app, s)
}

func servicesOf(app core.App) Services {
	s, _ := app.Store().Get(servicesStoreKey).(Services)
	return s.withDefaults()
}

var appDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// ValidAppDomain reports whether domain can hold app hostnames: a lower-case
// DNS name with at least two labels, short enough for a 32-character slug.
func ValidAppDomain(domain string) bool {
	return len(domain) <= 253-33 && appDomainPattern.MatchString(domain)
}

// secretsUnavailable is the answer when the server has no secret store.
func secretsUnavailable() *dropapi.Error {
	return &dropapi.Error{Status: http.StatusServiceUnavailable, Code: dropapi.CodeSecretsUnavailable,
		Message: "This server has no secret store. Secrets work on a hosted Drop (or with --fake-aws)."}
}

// Where an operation call came from, for a secret's set_via. Only code in this
// process can mark a request: the MCP tool drop_write (M6) calls the operation
// route in-process with a request marked by WithVia.
type viaKey struct{}

// WithVia marks a request as coming through via (dropapi.SetViaMCP).
func WithVia(r *http.Request, via string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), viaKey{}, via))
}

func (c *Ctx) via() string {
	if c.Request != nil {
		if v, _ := c.Request.Context().Value(viaKey{}).(string); v == dropapi.SetViaMCP {
			return dropapi.SetViaMCP
		}
	}
	return dropapi.SetViaUI
}
