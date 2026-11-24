// Package agent is the node agent, `drop agent run|check|render` (milestone
// M9, manual docs/agent.md). It runs as root on a nibrunner host beside the
// Drop server, signs in as its node, follows its desired document (realtime
// wake-ups on nodes/<self> plus a full pull on every event, every reconnect
// and a 60-second ticker), stages releases by sha256, reads secret versions
// from SSM, renders nibrunner's whole desired.json with internal/render and
// writes it atomically, keeping the previous file as desired.json.drop-prev.
// It reports a sanitised view of nibrunner's reported.json (never cron).
//
// Safety rules (decision 12 in the Drop decisions log):
//   - Drop owns the whole desired.json: the agent refuses to start, and
//     `check` fails, when the file holds any entry whose appId does not
//     start with "d-", and names those entries. It never deletes what it
//     did not create; the operator removes them.
//   - It never writes desired.json when the server is unreachable or answers
//     an error: what runs keeps running.
//   - Secret values and tokens live in memory only; state.json holds
//     metadata (docs/agent.md documents its shape).
package agent

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
)

// Environment variables the agent reads (systemd EnvironmentFile
// /etc/drop/agent.env). The node secret is never one of them: it comes from
// a file (systemd LoadCredential=node-secret, or DROP_AGENT_SECRET_FILE).
const (
	EnvServer            = "DROP_AGENT_SERVER"
	EnvHost              = "DROP_AGENT_HOST"
	EnvNode              = "DROP_AGENT_NODE"
	EnvSecretFile        = "DROP_AGENT_SECRET_FILE"
	EnvDesired           = "DROP_AGENT_DESIRED"
	EnvReported          = "DROP_AGENT_REPORTED"
	EnvArtifacts         = "DROP_AGENT_ARTIFACTS"
	EnvStateDir          = "DROP_AGENT_STATE_DIR"
	EnvAllowedHostSuffix = "DROP_AGENT_ALLOWED_HOST_SUFFIX"
	EnvPullInterval      = "DROP_AGENT_PULL_INTERVAL"
	EnvReportInterval    = "DROP_AGENT_REPORT_INTERVAL"
	// EnvCredentialsDirectory is set by systemd for LoadCredential=.
	EnvCredentialsDirectory = "CREDENTIALS_DIRECTORY"
	// EnvStateDirectory is set by systemd for StateDirectory=.
	EnvStateDirectory = "STATE_DIRECTORY"
)

// Defaults.
const (
	DefaultServer   = "http://127.0.0.1:8100"
	DefaultStateDir = "/var/lib/drop-agent"
	// CredentialName is the systemd credential holding the node secret.
	CredentialName        = "node-secret"
	DefaultPullInterval   = 60 * time.Second
	DefaultReportInterval = 30 * time.Second
	// RefreshInterval renews the node token (tokens last one hour).
	RefreshInterval = 30 * time.Minute
	// PullTimeout bounds one GET of the desired document.
	PullTimeout = 10 * time.Second
	// StaleReport is when nibrunner's reported.json counts as stale.
	StaleReport = 60 * time.Second
	// StateFileName is the agent's state file inside the state directory.
	StateFileName = "state.json"
	// BackupSuffix names the copy of the previous desired.json.
	BackupSuffix = ".drop-prev"
)

var (
	nodeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	hostPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// Config is the agent's configuration. Paths are absolute.
type Config struct {
	identity          identityConfig
	Server            string // http(s) URL of the Drop server, no trailing slash
	Host              string // the Drop domain, sent as the Host header
	Node              string // node name (sign-in identity)
	SecretFile        string // file holding the node secret
	Desired           string // nibrunner's desired.json
	Reported          string // nibrunner's reported.json
	Artifacts         string // nibrunner's artifact store (objectKey = sha256)
	StateDir          string // holds state.json
	AllowedHostSuffix string // for example .bigconfig.space
	SSMPrefix         string // for example /drop
	FakeAWS           string // --fake-aws DIR (loopback server only)
	PullInterval      time.Duration
	ReportInterval    time.Duration
}

// StatePath is the full path of state.json.
func (c Config) StatePath() string { return filepath.Join(c.StateDir, StateFileName) }

// BackupPath is the full path of desired.json.drop-prev.
func (c Config) BackupPath() string { return c.Desired + BackupSuffix }

// Overrides are command-line values that take precedence over the
// environment; empty means unset.
type Overrides map[string]string

// LoadConfig builds the configuration from the environment (getenv) and
// command-line overrides (flag names without dashes: server, host, node,
// secret-file, desired, reported, artifacts, state-dir, allowed-host-suffix,
// ssm-prefix, fake-aws). needServer is false for render, which
// needs no server, node, secret or nibrunner paths.
func LoadConfig(getenv func(string) string, o Overrides, needServer bool) (Config, error) {
	pick := func(flag, env, fallback string) string {
		if v := o[flag]; v != "" {
			return v
		}
		if env != "" {
			if v := getenv(env); v != "" {
				return v
			}
		}
		return fallback
	}
	stateFallback := DefaultStateDir
	if dir := getenv(EnvStateDirectory); dir != "" {
		// systemd may list several directories separated by colons.
		stateFallback = strings.Split(dir, ":")[0]
	}
	secretFallback := ""
	if dir := getenv(EnvCredentialsDirectory); dir != "" {
		secretFallback = filepath.Join(dir, CredentialName)
	}
	c := Config{
		identity:          identityConfig{issuer: getenv("DROP_IDENTITY_ISSUER"), audience: pick("", "DROP_IDENTITY_AUDIENCE", "sts.amazonaws.com"), role: getenv("DROP_IDENTITY_ROLE_ARN"), keyFile: getenv("DROP_IDENTITY_KEY_FILE"), roleDir: "/etc/nibrunner/workload-identity", sourceDir: "/run/drop-agent-identity"},
		Server:            strings.TrimRight(pick("server", EnvServer, DefaultServer), "/"),
		Host:              pick("host", EnvHost, ""),
		Node:              pick("node", EnvNode, ""),
		SecretFile:        pick("secret-file", EnvSecretFile, secretFallback),
		Desired:           pick("desired", EnvDesired, ""),
		Reported:          pick("reported", EnvReported, ""),
		Artifacts:         pick("artifacts", EnvArtifacts, ""),
		StateDir:          pick("state-dir", EnvStateDir, stateFallback),
		AllowedHostSuffix: pick("allowed-host-suffix", EnvAllowedHostSuffix, ""),
		SSMPrefix:         pick("ssm-prefix", awsx.EnvSSMPrefix, awsx.DefaultSSMPrefix),
		FakeAWS:           o["fake-aws"],
		PullInterval:      DefaultPullInterval,
		ReportInterval:    DefaultReportInterval,
	}
	var err error
	if c.PullInterval, err = seconds(getenv(EnvPullInterval), DefaultPullInterval, 5, 3600, EnvPullInterval); err != nil {
		return c, err
	}
	if c.ReportInterval, err = seconds(getenv(EnvReportInterval), DefaultReportInterval, 5, 300, EnvReportInterval); err != nil {
		return c, err
	}
	if !strings.HasPrefix(c.AllowedHostSuffix, ".") || !hostPattern.MatchString("x"+c.AllowedHostSuffix) {
		return c, fmt.Errorf("%s must be a dot followed by a domain, for example .apps.example.com", EnvAllowedHostSuffix)
	}
	if err := awsx.ValidateSSMPrefix(c.SSMPrefix); err != nil {
		return c, fmt.Errorf("%s: %v", awsx.EnvSSMPrefix, err)
	}
	if !needServer {
		if c.Desired != "" && !filepath.IsAbs(c.Desired) {
			return c, fmt.Errorf("%s must be an absolute path", EnvDesired)
		}
		return c, nil
	}
	for _, p := range []struct{ name, value string }{{EnvDesired, c.Desired}, {EnvReported, c.Reported}, {EnvArtifacts, c.Artifacts}, {EnvStateDir, c.StateDir}} {
		if p.value == "" || !filepath.IsAbs(p.value) {
			return c, fmt.Errorf("%s must be an absolute path", p.name)
		}
	}
	u, err := url.Parse(c.Server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return c, fmt.Errorf("%s must be an http(s) URL such as %s", EnvServer, DefaultServer)
	}
	if u.Scheme == "http" && !awsx.IsLoopbackHost(u.Hostname()) {
		return c, fmt.Errorf("%s requires HTTPS for a remote server", EnvServer)
	}
	if !hostPattern.MatchString(c.Host) {
		return c, fmt.Errorf("%s must be the Drop domain (sent as the Host header), for example drop.example.com", EnvHost)
	}
	if !nodeNamePattern.MatchString(c.Node) {
		return c, fmt.Errorf("%s must be the node's name (lower-case letters, digits and hyphens)", EnvNode)
	}
	if c.SecretFile == "" || !filepath.IsAbs(c.SecretFile) {
		return c, fmt.Errorf("the node secret file is not set: run under systemd with LoadCredential=%s:FILE, or set %s to an absolute path", CredentialName, EnvSecretFile)
	}
	if c.FakeAWS != "" {
		if err := awsx.CheckFakeAllowedURL(c.Server); err != nil {
			return c, err
		}
	}
	return c, nil
}

func seconds(raw string, fallback time.Duration, lo, hi int, name string) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be a number of seconds from %d to %d", name, lo, hi)
	}
	return time.Duration(n) * time.Second, nil
}

// ReadSecret reads the node secret from its file. The file must not be
// readable by group or others; the value is never logged.
func ReadSecret(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("node secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("node secret file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("node secret file %s must not be readable by group or others (chmod 600)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("node secret file: %w", err)
	}
	secret := strings.TrimRight(string(data), "\r\n")
	if len(secret) < 32 || len(secret) > 256 || strings.ContainsAny(secret, "\r\n") {
		return "", fmt.Errorf("node secret file %s must hold one line of 32 to 256 characters (the password from `drop node create`)", path)
	}
	return secret, nil
}
