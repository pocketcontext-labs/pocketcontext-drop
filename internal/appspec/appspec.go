// Package appspec holds the rules of an app's configuration that both sides
// of Drop enforce: the server's operations (internal/dropserver) refuse a bad
// value up front, and the node's renderer (internal/render) checks the same
// rules again before anything reaches nibrunner's desired.json (plan 7.3).
// Keeping them in one place means the server never accepts a value the node
// would reject. Standard library only; messages name fields and variables,
// never values.
package appspec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MiB is one mebibyte.
const MiB = 1 << 20

// Bounds and defaults of the data model (plan section 3, decision 15).
const (
	MinDiskMiB, MaxDiskMiB, DefaultDiskMiB       = 256, 16384, 1024
	MinIdleMs, MaxIdleMs, DefaultIdleMs          = 60000, 86400000, 300000
	MinPort, MaxPort, DefaultPort                = 1, 65535, 8080
	MinVCPU, MaxVCPU, DefaultVCPU                = 1, 2, 1
	MinMemoryMiB, MaxMemoryMiB, DefaultMemoryMiB = 128, 4096, 512
	// MaxReleaseBytes is the largest release (90 MiB, under Cloudflare's
	// 100 MB request body cap).
	MaxReleaseBytes = 90 * MiB
	// Arguments: at most MaxArgs, each at most MaxArgBytes with no NUL, and at
	// most MaxArgsTotal bytes in all.
	MaxArgs, MaxArgBytes, MaxArgsTotal = 64, 4096, 4096
	// Environment: at most MaxEnvEntries variables, plain and secret
	// together, each value at most MaxEnvValueBytes with no NUL.
	MaxEnvEntries, MaxEnvValueBytes = 64, 4096
	// MaxHealthPath bounds an HTTP health check path.
	MaxHealthPath = 1024
	// MaxDeploymentSeq is the largest deployment number (9 digits).
	MaxDeploymentSeq = 999999999
	// DefaultExecutable is the default executable name.
	DefaultExecutable = "server"
)

// Desired states of an app.
const (
	StateRunning   = "running"
	StateOnRequest = "on-request"
	StateStopped   = "stopped"
)

// Health check kinds.
const (
	HealthTCP  = "tcp"
	HealthHTTP = "http"
)

var (
	// AppIDPattern is nibrunner's app id of a Drop app: "d-" and the apps
	// record id.
	AppIDPattern = regexp.MustCompile(`^d-[a-z0-9]{15}$`)
	// ExecutablePattern is an executable name (the file at /app/<name>).
	ExecutablePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// EnvNamePattern is an environment variable or secret name.
	EnvNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// HealthPathPattern is an HTTP health check path: printable, no spaces.
	HealthPathPattern = regexp.MustCompile(`^/[\x21-\x7e]*$`)
	// SHA256Pattern is a lower-case hex sha256.
	SHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

	deployTail = regexp.MustCompile(`^-dep-[0-9]{1,9}$`)
	nibrunRef  = regexp.MustCompile(`\$\{NIBRUN_[^}]*\}`)
)

var crewLocalPart = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$")
var mailDomainLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ValidSignInMailbox accepts ASCII dot-atom mailboxes with DNS domains.
func ValidSignInMailbox(address string) bool {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || len(address) > 254 || len(local) > 64 || !crewLocalPart.MatchString(local) {
		return false
	}
	for label := range strings.SplitSeq(domain, ".") {
		if !mailDomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// ValidDesiredState reports whether s is running, on-request or stopped.
func ValidDesiredState(s string) bool {
	return s == StateRunning || s == StateOnRequest || s == StateStopped
}

// AppID is nibrunner's app id of an apps record id.
func AppID(recordID string) string { return "d-" + recordID }

// VolumeID is the data volume id of an app id.
func VolumeID(appID string) string { return appID + "-vol" }

// DeploymentID is the deployment id of an app id and deployment number.
func DeploymentID(appID string, seq int) string { return appID + "-dep-" + strconv.Itoa(seq) }

// DeploymentIDOK reports whether id is <appID>-dep-<1 to 9 digits>.
func DeploymentIDOK(appID, id string) bool {
	tail, ok := strings.CutPrefix(id, appID)
	return ok && deployTail.MatchString(tail)
}

// ReservedEnv reports names an app may not set: nibrunner's own (NIBRUN_*),
// Drop's (DROP_*) and the ones the renderer fixes (HOME, PATH).
func ReservedEnv(name string) bool {
	return name == "HOME" || name == "PATH" || strings.HasPrefix(name, "NIBRUN_") || strings.HasPrefix(name, "DROP_")
}

// CheckEnvName returns why name cannot be an environment variable or secret
// name, or "" when it can.
func CheckEnvName(name string) string {
	if !EnvNamePattern.MatchString(name) {
		return "names must match ^[A-Z][A-Z0-9_]{0,63}$"
	}
	if ReservedEnv(name) {
		return name + " is reserved"
	}
	return ""
}

// CheckEnvValue returns why value cannot be the value of the variable name,
// or "" when it can. The reason names the variable, never the value.
func CheckEnvValue(name, value string) string {
	if len(value) > MaxEnvValueBytes || strings.ContainsRune(value, 0) {
		return fmt.Sprintf("the value of %s must be at most %d bytes with no NUL", name, MaxEnvValueBytes)
	}
	// nibrunner fills ${NIBRUN_HOSTNAME} and ${NIBRUN_HTTP_PORT} and refuses
	// the whole document for any other complete ${NIBRUN_...} reference.
	for _, ref := range nibrunRef.FindAllString(value, -1) {
		if ref != "${NIBRUN_HOSTNAME}" && ref != "${NIBRUN_HTTP_PORT}" {
			return fmt.Sprintf("the value of %s references an unknown NIBRUN_ variable", name)
		}
	}
	return ""
}

// CheckArgs returns the field and reason of the first problem with args, or
// two empty strings.
func CheckArgs(args []string) (field, reason string) {
	if len(args) > MaxArgs {
		return "args", fmt.Sprintf("at most %d arguments", MaxArgs)
	}
	total := 0
	for i, a := range args {
		if len(a) > MaxArgBytes || strings.ContainsRune(a, 0) {
			return fmt.Sprintf("args[%d]", i), fmt.Sprintf("at most %d bytes, no NUL", MaxArgBytes)
		}
		total += len(a)
	}
	if total > MaxArgsTotal {
		return "args", fmt.Sprintf("at most %d bytes in all", MaxArgsTotal)
	}
	return "", ""
}

// CheckHealth returns the field and reason of a bad health check, or two
// empty strings. A tcp check has no path; an http check needs one.
func CheckHealth(kind, path string) (field, reason string) {
	switch kind {
	case HealthTCP:
		if path != "" {
			return "path", "a tcp check has no path"
		}
	case HealthHTTP:
		if len(path) > MaxHealthPath || !HealthPathPattern.MatchString(path) {
			return "path", fmt.Sprintf("must start with / and hold at most %d printable characters without spaces", MaxHealthPath)
		}
	default:
		return "kind", "must be tcp or http"
	}
	return "", ""
}
