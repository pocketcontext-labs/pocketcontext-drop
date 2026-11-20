package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Secrets resolves secret versions from SSM (name and exact version, with
// decryption). The in-memory cache is scoped to a deployment, parameter and
// version. The resolved values go into desired.json.
type Secrets struct {
	Store  awsx.SecretReader
	Prefix string
	cache  map[secretKey]string
}

// SSM version numbers restart when a parameter is deleted and recreated.
// Drop increments the deployment on every secret change.
type secretKey struct {
	deployment string
	parameter  string
	version    int64
}

// Resolve returns the values of an app's secrets by name. It refuses a
// parameter that is not exactly <prefix>/apps/<appId without d->/<NAME>:
// the server never chooses which parameter the node reads. Errors name
// parameters, never values.
func (s *Secrets) Resolve(ctx context.Context, appID, deploymentID string, secrets []dropapi.DesiredSecret) (map[string]string, error) {
	if s.cache == nil {
		s.cache = map[secretKey]string{}
	}
	values := make(map[string]string, len(secrets))
	for _, sec := range secrets {
		want, err := awsx.SecretParameter(s.Prefix, appID, sec.Name)
		if err != nil || sec.Parameter != want {
			return nil, fmt.Errorf("secret %s: parameter is not %s/apps/<app>/%s", sec.Name, s.Prefix, sec.Name)
		}
		if sec.Version < 1 {
			return nil, fmt.Errorf("secret %s: version must be at least 1", sec.Name)
		}
		if _, dup := values[sec.Name]; dup {
			return nil, fmt.Errorf("secret %s named twice", sec.Name)
		}
		key := secretKey{deploymentID, sec.Parameter, sec.Version}
		if v, ok := s.cache[key]; ok {
			values[sec.Name] = v
			continue
		}
		v, err := s.Store.Get(ctx, sec.Parameter, sec.Version)
		if err != nil {
			if errors.Is(err, awsx.ErrNotFound) {
				return nil, fmt.Errorf("secret %s: parameter %s version %d not found", sec.Name, sec.Parameter, sec.Version)
			}
			return nil, fmt.Errorf("secret %s: reading parameter %s version %d failed", sec.Name, sec.Parameter, sec.Version)
		}
		s.cache[key] = v
		values[sec.Name] = v
	}
	return values, nil
}

// Forget drops cached values of an app's parameters that the current
// document no longer names.
func (s *Secrets) Forget(keep map[secretKey]bool) {
	for k := range s.cache {
		if !keep[k] {
			delete(s.cache, k)
		}
	}
}
