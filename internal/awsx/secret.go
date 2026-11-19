package awsx

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// MaxSecretBytes is the largest secret value: the SSM standard tier limit.
const MaxSecretBytes = 4096

// SecretWriter is the server's view of the secret store. It never reads a
// value back: the server holds only a SecretWriter.
type SecretWriter interface {
	// Put stores value as a new version of the parameter (creating it at
	// version 1) and returns that version. value is 1..MaxSecretBytes bytes.
	Put(ctx context.Context, name, value string) (version int64, err error)
	// Delete removes the parameter and all its versions. Deleting a missing
	// parameter succeeds. A later Put starts again at version 1.
	Delete(ctx context.Context, name string) error
}

// SecretReader is the agent's view of the secret store.
type SecretReader interface {
	// Get returns the value of one exact version (>= 1), or ErrNotFound when
	// the parameter or that version does not exist.
	Get(ctx context.Context, name string, version int64) (string, error)
}

// SecretStore is the full contract implemented by SSM and the fakes.
type SecretStore interface {
	SecretWriter
	SecretReader
}

var (
	paramSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
	prefixPattern       = regexp.MustCompile(`^(/[a-z0-9][a-z0-9-]{0,62}){1,4}$`)
	appIDPattern        = regexp.MustCompile(`^d-[a-z0-9]{15}$`)
	secretNamePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// DefaultSSMPrefix is the parameter prefix used when DROP_SSM_PREFIX is unset.
const DefaultSSMPrefix = "/drop"

// ValidateParameterName accepts fully qualified SSM names: "/" followed by 1
// to 15 segments of letters, digits, '_', '.' and '-' (never starting with
// '.'), at most 1011 bytes, and not under the reserved /aws or /ssm roots.
func ValidateParameterName(name string) error {
	if len(name) < 2 || len(name) > 1011 || name[0] != '/' {
		return fmt.Errorf("%w: parameter name must start with / and be at most 1011 bytes", ErrInvalid)
	}
	parts := strings.Split(name[1:], "/")
	if len(parts) > 15 {
		return fmt.Errorf("%w: parameter name has more than 15 levels", ErrInvalid)
	}
	for _, p := range parts {
		if !paramSegmentPattern.MatchString(p) {
			return fmt.Errorf("%w: parameter name segment %q", ErrInvalid, p)
		}
	}
	first := strings.ToLower(parts[0])
	if strings.HasPrefix(first, "aws") || strings.HasPrefix(first, "ssm") {
		return fmt.Errorf("%w: parameter names under aws or ssm are reserved", ErrInvalid)
	}
	return nil
}

// ValidateSSMPrefix accepts 1 to 4 lower-case segments such as "/drop".
func ValidateSSMPrefix(prefix string) error {
	if !prefixPattern.MatchString(prefix) {
		return fmt.Errorf("%w: SSM prefix must look like /drop", ErrInvalid)
	}
	return nil
}

// SecretParameter returns the parameter of an app secret:
// <prefix>/apps/<appID without "d-">/<NAME>. appID is `d-` plus a 15-character
// record id; name is an environment variable name (A-Z, 0-9, _; at most 64).
// The agent compares desired-state parameters against this exact value.
func SecretParameter(prefix, appID, name string) (string, error) {
	if err := ValidateSSMPrefix(prefix); err != nil {
		return "", err
	}
	if !appIDPattern.MatchString(appID) {
		return "", fmt.Errorf("%w: app id must be d- plus 15 lower-case letters or digits", ErrInvalid)
	}
	if !secretNamePattern.MatchString(name) {
		return "", fmt.Errorf("%w: secret name must match [A-Z][A-Z0-9_]{0,63}", ErrInvalid)
	}
	return prefix + "/apps/" + appID[2:] + "/" + name, nil
}

func checkSecretValue(value string) error {
	if value == "" || len(value) > MaxSecretBytes {
		return fmt.Errorf("%w: secret value must be 1 to %d bytes", ErrInvalid, MaxSecretBytes)
	}
	return nil
}

func checkVersion(version int64) error {
	if version < 1 {
		return fmt.Errorf("%w: secret version must be at least 1", ErrInvalid)
	}
	return nil
}
