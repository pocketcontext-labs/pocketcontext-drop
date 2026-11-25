package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/hostservices"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// Host services (docs/agent.md "Host services"). `drop agent run` with
// DROP_AGENT_HOST_SERVICES=s3 gives every managed Crew app the node's S3
// proxy instead of a workload identity token and writes
// <state dir>/host-services.json (apps, buckets, limits) before desired.json.
// `drop agent host-services`, a separate process and unit, serves the proxy
// on the root-only socket nibrunnerd forwards guest connections to; it signs
// each app's web-identity token itself and exchanges it for that app's STS
// session, so no credential ever enters a guest.
const (
	EnvHostServices       = "DROP_AGENT_HOST_SERVICES"
	EnvHostServicesSocket = "DROP_HOST_SERVICES_SOCKET"
	EnvHostServicesPeer   = "DROP_HOST_SERVICES_PEER_UID"
	EnvHostServicesS3     = "DROP_HOST_SERVICES_S3_ENDPOINT"
	EnvHostServicesSTS    = "DROP_HOST_SERVICES_STS_ENDPOINT"
	EnvHostServicesRegion = "DROP_HOST_SERVICES_STS_REGION"

	HostServicesAppsFile  = "host-services.json"
	HostServicesUsageFile = "host-services-usage.json"
)

// hostServicesEnabled reads DROP_AGENT_HOST_SERVICES: empty (off) or s3.
func hostServicesEnabled(getenv func(string) string) (bool, error) {
	switch getenv(EnvHostServices) {
	case "":
		return false, nil
	case "s3":
		return true, nil
	}
	return false, fmt.Errorf("%s must be empty or s3", EnvHostServices)
}

// HostServicesConfig configures `drop agent host-services`.
type HostServicesConfig struct {
	identity    identityConfig
	Desired     string
	StateDir    string
	Socket      string
	PeerUID     uint32
	S3Endpoint  string
	STSEndpoint string
	STSRegion   string
}

func (c HostServicesConfig) appsPath() string { return filepath.Join(c.StateDir, HostServicesAppsFile) }
func (c HostServicesConfig) usagePath() string {
	return filepath.Join(c.StateDir, HostServicesUsageFile)
}

// endpointOK accepts an https URL, or http on loopback (offline tests).
func endpointOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && awsx.IsLoopbackHost(u.Hostname()))
}

// LoadHostServicesConfig reads the same environment file as `drop agent run`.
func LoadHostServicesConfig(getenv func(string) string, o Overrides) (HostServicesConfig, error) {
	base, err := LoadConfig(getenv, o, false)
	if err != nil {
		return HostServicesConfig{}, err
	}
	c := HostServicesConfig{identity: base.identity, Desired: base.Desired, StateDir: base.StateDir,
		Socket: getenv(EnvHostServicesSocket), S3Endpoint: getenv(EnvHostServicesS3), STSEndpoint: getenv(EnvHostServicesSTS),
		STSRegion: getenv(EnvHostServicesRegion)}
	if v := o["socket"]; v != "" {
		c.Socket = v
	}
	if c.Socket == "" {
		c.Socket = hostservices.DefaultSocketPath
	}
	if c.Desired == "" || !filepath.IsAbs(c.Desired) || !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Socket) {
		return c, fmt.Errorf("%s, the state directory and %s must be absolute paths", EnvDesired, EnvHostServicesSocket)
	}
	if raw := getenv(EnvHostServicesPeer); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return c, fmt.Errorf("%s must be nibrunnerd's numeric uid", EnvHostServicesPeer)
		}
		c.PeerUID = uint32(n)
	}
	for name, v := range map[string]string{EnvHostServicesS3: c.S3Endpoint, EnvHostServicesSTS: c.STSEndpoint} {
		if v != "" && !endpointOK(v) {
			return c, fmt.Errorf("%s must be an https URL (http only on loopback)", name)
		}
	}
	if c.STSRegion == "" {
		c.STSRegion = getenv(awsx.EnvRegion)
	}
	if c.identity.issuer == "" {
		return c, errors.New("host services need the workload identity settings (DROP_IDENTITY_ISSUER, DROP_IDENTITY_ROLE_ARN, DROP_IDENTITY_KEY_FILE)")
	}
	return c, nil
}

// stsSessions exchanges the agent's own per-app web-identity token for the
// app's STS session and caches it until five minutes before it expires.
type stsSessions struct {
	signer   *workloadSigner
	region   string
	endpoint string
	client   *http.Client
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	mu    sync.Mutex
	creds aws.Credentials
}

const sessionRefreshWindow = 5 * time.Minute

func (s *stsSessions) Credentials(ctx context.Context, appID string) (aws.Credentials, error) {
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*session{}
	}
	sess := s.sessions[appID]
	if sess == nil {
		sess = &session{}
		s.sessions[appID] = sess
	}
	s.mu.Unlock()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	now := s.now()
	if sess.creds.AccessKeyID != "" && sess.creds.Expires.Sub(now) > sessionRefreshWindow {
		return sess.creds, nil
	}
	s.signer.mu.Lock()
	token, err := s.signer.token(appID, now)
	s.signer.mu.Unlock()
	if err != nil {
		return aws.Credentials{}, err
	}
	client := sts.New(sts.Options{Region: s.region, Credentials: aws.AnonymousCredentials{}, HTTPClient: s.client}, func(o *sts.Options) {
		if s.endpoint != "" {
			o.BaseEndpoint = aws.String(s.endpoint)
		}
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := client.AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn: aws.String(s.signer.role), RoleSessionName: aws.String(appID), WebIdentityToken: aws.String(string(token)),
		DurationSeconds: aws.Int32(3600),
	})
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("STS refused the app's web identity: %s", awsErrorCode(err))
	}
	c := out.Credentials
	if c == nil || c.AccessKeyId == nil || c.SecretAccessKey == nil || c.SessionToken == nil || c.Expiration == nil {
		return aws.Credentials{}, errors.New("STS answered without credentials")
	}
	sess.creds = aws.Credentials{AccessKeyID: *c.AccessKeyId, SecretAccessKey: *c.SecretAccessKey, SessionToken: *c.SessionToken,
		CanExpire: true, Expires: *c.Expiration, Source: "drop-host-services"}
	return sess.creds, nil
}

// RunHostServices serves host services until ctx ends.
func RunHostServices(ctx context.Context, c HostServicesConfig, logf func(string, ...any)) error {
	signer, err := c.identity.load()
	if err != nil {
		return err
	}
	if signer == nil {
		return errors.New("host services need the workload identity settings")
	}
	return runHostServices(ctx, c, signer, nil, logf)
}

func runHostServices(ctx context.Context, c HostServicesConfig, signer *workloadSigner, client *http.Client, logf func(string, ...any)) error {
	usage, err := hostservices.OpenUsage(c.usagePath())
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 64,
			IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: 2 * time.Second}}
	}
	region := c.STSRegion
	creds := &stsSessions{signer: signer, region: region, endpoint: c.STSEndpoint, client: client, now: time.Now}
	registry := &hostservices.Registry{DesiredPath: c.Desired, AppsPath: c.appsPath()}
	srv := &hostservices.Server{Socket: c.Socket, PeerUID: c.PeerUID, Lookup: registry.Lookup, Logf: logf,
		Proxy: &hostservices.Proxy{Upstream: hostservices.Upstream{Endpoint: c.S3Endpoint, Client: client}, Creds: creds, Usage: usage, Logf: logf}}
	l, err := srv.Listen()
	if err != nil {
		return err
	}
	logf("host services: serving S3 on %s for uid %d", c.Socket, c.PeerUID)
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				if err := usage.Flush(time.Now()); err != nil {
					logf("host services: writing usage: %v", err)
				}
				return
			case <-t.C:
				if err := usage.Flush(time.Now()); err != nil {
					logf("host services: writing usage: %v", err)
				}
			}
		}
	}()
	err = srv.Serve(ctx, l)
	<-flushed
	return err
}

// hostAppEntry is the host-services.json entry of one rendered app.
func hostAppEntry(app dropapi.DesiredApp) hostservices.AppEntry {
	e := hostservices.AppEntry{AppID: app.AppID, DeploymentID: app.DeploymentID, Bucket: app.CrewReplicaBucket, Region: app.AWSRegion}
	if l := app.Limits; l != nil {
		e.Limits = hostservices.FileLimits{RPS: l.RPS, Burst: l.Burst, MaxObjectBytes: l.MaxObjectBytes, StoredBytesQuota: l.StoredBytesQuota, MonthlyBytesOut: l.MonthlyBytesOut}
	}
	return e
}

// hostUsage reads the proxy's counters of each app's deployment for a report.
func hostUsage(path string) map[string]*dropapi.HostUsage {
	f, err := hostservices.ReadUsageFile(path)
	if err != nil {
		return nil
	}
	out := map[string]*dropapi.HostUsage{}
	for _, d := range f.Deployments {
		if d == nil || d.Seq <= 0 {
			continue
		}
		c := d.Counters
		u := &dropapi.HostUsage{DeploymentID: d.DeploymentID, Seq: d.Seq, Counters: dropapi.UsageCounters{
			ReadRequests: c.ReadRequests, WriteRequests: c.WriteRequests, ListRequests: c.ListRequests, DeleteRequests: c.DeleteRequests,
			RejectedRequests: c.RejectedRequests, BytesIn: c.BytesIn, BytesOut: c.BytesOut}}
		if !u.Counters.Valid() {
			continue
		}
		out[d.DeploymentID] = u
	}
	return out
}

// awsErrorCode names an AWS error by its code only (never its message,
// which can echo request values).
func awsErrorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return "request failed"
}

// writeHostApps rewrites host-services.json when it changes: the apps
// rendered with host S3 (refused ones aside), plus the previous entries of
// apps whose previous desired.json entry is kept. It is written before
// desired.json, so a new deployment is known to the proxy when it starts.
func (a *Agent) writeHostApps(apps map[string]dropapi.DesiredApp, rejected []render.AppError, keep []string) error {
	path := filepath.Join(a.cfg.StateDir, HostServicesAppsFile)
	var previous hostservices.AppsFile
	if raw, err := readOptional(path); err == nil && raw != nil {
		_ = json.Unmarshal(raw, &previous)
	}
	refused := map[string]bool{}
	for _, r := range rejected {
		refused[r.AppID] = true
	}
	entries := []hostservices.AppEntry{}
	for _, id := range slices.Sorted(maps.Keys(apps)) {
		if !refused[id] {
			entries = append(entries, hostAppEntry(apps[id]))
		}
	}
	for _, e := range previous.Apps {
		if slices.Contains(keep, e.AppID) {
			entries = append(entries, e)
		}
	}
	slices.SortFunc(entries, func(x, y hostservices.AppEntry) int { return strings.Compare(x.AppID, y.AppID) })
	if previous.Schema == hostservices.AppsSchema && reflect.DeepEqual(previous.Apps, entries) {
		return nil
	}
	if len(entries) == 0 && previous.Schema == "" {
		return nil
	}
	return hostservices.WriteAppsFile(path, entries)
}
