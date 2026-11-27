package dropapi

import (
	"regexp"
	"time"
)

// FirstRun is a short-lived guest bootstrap credential. Only the owner of the
// matching app and deployment may read it. Value is never a destination URL.
type FirstRun struct {
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	ExpiresAt string `json:"expires_at"`
}

var installerToken = regexp.MustCompile(`^[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+$`)
var pairingCode = regexp.MustCompile(`^[a-zA-Z0-9]{32}$`)

func (f *FirstRun) Valid() bool {
	if f == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, f.ExpiresAt); err != nil || len(f.ExpiresAt) > 40 {
		return false
	}
	switch f.Kind {
	case "pocketbase_installer":
		return len(f.Value) <= 768 && installerToken.MatchString(f.Value)
	case "zeroclaw_pairing":
		return pairingCode.MatchString(f.Value)
	default:
		return false
	}
}

func (f *FirstRun) Available(now time.Time) bool {
	if f == nil || !f.Valid() {
		return false
	}
	until, _ := time.Parse(time.RFC3339, f.ExpiresAt)
	return now.Before(until) && !until.After(now.Add(31*time.Minute))
}
