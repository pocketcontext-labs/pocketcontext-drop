package dropserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Bump the contract when changes to the guest probe or validation require
// reinspection. Approval of the content digest is independent of the cache.
const betaProbeContract = 1
const betaProbeLease = 5 * time.Minute
const maxBetaManifest = 16 << 10

var betaDigestPattern = regexp.MustCompile("^[0-9a-f]{64}$")
var betaVersionPattern = regexp.MustCompile("^[0-9A-Za-z][0-9A-Za-z.+-]{0,79}$")

func init() {
	register("beta.approve", betaApprove)
	register("beta.revoke", betaRevoke)
	register("beta.claim", betaClaim)
	register("beta.complete", betaComplete)
	register("beta.inspect", betaInspect)
	register("beta.prepare", betaPrepare)
	register("beta.deploy", betaDeploy)
}

func betaApproved(app core.App, digest string) error {
	if !betaDigestPattern.MatchString(digest) {
		return Invalid("sha256", "Select a supported PocketContext binary.")
	}
	var approved int
	err := app.DB().NewQuery("SELECT approved FROM drop_beta_catalog WHERE digest={:digest}").Bind(dbx.Params{"digest": digest}).Row(&approved)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && approved != 1) {
		return Conflict("unsupported_binary", "This binary is not an approved PocketContext release.")
	}
	return err
}

func betaApprove(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	if !betaDigestPattern.MatchString(in.SHA256) {
		return nil, Invalid("sha256", "Supply a lowercase SHA-256 digest.")
	}
	_, err := c.App.DB().NewQuery("INSERT INTO drop_beta_catalog(digest,approved) VALUES ({:d},1) ON CONFLICT(digest) DO UPDATE SET approved=1").Bind(dbx.Params{"d": in.SHA256}).Execute()
	c.Event(Event{Detail: map[string]any{"sha256": in.SHA256}})
	return map[string]any{"approved": true}, err
}

func betaRevoke(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	if err := betaApproved(c.App, in.SHA256); err != nil {
		return nil, err
	}
	p := dbx.Params{"d": in.SHA256}
	if _, err := c.App.DB().NewQuery("UPDATE drop_beta_catalog SET approved=0 WHERE digest={:d}").Bind(p).Execute(); err != nil {
		return nil, err
	}
	// Fence unfinished probes even if this digest is subsequently reapproved.
	if _, err := c.App.DB().NewQuery("DELETE FROM drop_beta_probes WHERE digest={:d} AND state='running'").Bind(p).Execute(); err != nil {
		return nil, err
	}
	apps, err := c.App.FindRecordsByFilter("apps", "beta_digest = {:d} && desired_state != 'stopped'", "", 0, 0, p)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		if err := stopApp(c.App, a); err != nil {
			return nil, err
		}
	}
	c.Event(Event{Detail: map[string]any{"sha256": in.SHA256, "stopped": len(apps)}})
	return map[string]any{"approved": false}, nil
}

type betaProbe struct {
	State    string `db:"state"`
	Attempt  string `db:"attempt"`
	Expires  int64  `db:"expires_at"`
	Manifest string `db:"manifest"`
}

func getBetaProbe(app core.App, digest string) (betaProbe, error) {
	var probe betaProbe
	err := app.DB().NewQuery("SELECT state,attempt,expires_at,manifest FROM drop_beta_probes WHERE digest={:d} AND contract={:v}").Bind(dbx.Params{"d": digest, "v": betaProbeContract}).One(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return probe, err
}

func betaClaim(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	if err := betaApproved(c.App, in.SHA256); err != nil {
		return nil, err
	}
	probe, err := getBetaProbe(c.App, in.SHA256)
	if err != nil {
		return nil, err
	}
	c.Event(Event{Detail: map[string]any{"sha256": in.SHA256, "contract": betaProbeContract}})
	now := time.Now().UTC()
	switch probe.State {
	case "valid", "invalid":
		return map[string]any{"cached": true, "state": probe.State}, nil
	case "running":
		if probe.Expires > now.Unix() {
			return nil, Conflict("probe_busy", "This release is being inspected.")
		}
	}
	attempt := security.RandomString(32)
	exp := now.Add(betaProbeLease)
	_, err = c.App.DB().NewQuery(`INSERT INTO drop_beta_probes(digest,contract,state,attempt,expires_at) VALUES ({:d},{:v},'running',{:a},{:e})
		ON CONFLICT(digest,contract) DO UPDATE SET state='running',attempt=excluded.attempt,expires_at=excluded.expires_at,manifest=''`).Bind(dbx.Params{"d": in.SHA256, "v": betaProbeContract, "a": attempt, "e": exp.Unix()}).Execute()
	return map[string]any{"attempt": attempt, "expires_at": exp.Format(time.RFC3339)}, err
}

func parseBetaManifest(raw []byte) (dropapi.BetaManifest, error) {
	var m dropapi.BetaManifest
	if len(raw) > maxBetaManifest || decodeStrict(raw, &m) != nil || m.Schema != "drop.manifest/v1" || m.App != "crew" || !betaVersionPattern.MatchString(m.Version) ||
		m.HTTPPort != 8080 || m.HookPort != 8081 || m.Health != "tcp" || m.Minimum.MemoryMiB < 1 || m.Minimum.CPUQuarters < 1 || m.Minimum.DiskMiB < 1 {
		return m, Invalid("manifest", "The manifest is incompatible with this Drop runtime.")
	}
	slices.Sort(m.Hooks)
	slices.Sort(m.Requires)
	if !slices.Equal(m.Hooks, []string{"resume", "suspend", "terminate"}) || !slices.Equal(m.Requires, []string{"persistent_volume", "s3_replica", "sign_in_email", "verified_owner_email"}) {
		return m, Invalid("manifest", "The manifest requests unsupported runtime features.")
	}
	return m, nil
}

func betaComplete(c *Ctx, in *dropapi.BetaCompleteInput) (any, error) {
	if err := betaApproved(c.App, in.SHA256); err != nil {
		return nil, err
	}
	probe, err := getBetaProbe(c.App, in.SHA256)
	if err != nil {
		return nil, err
	}
	if probe.State != "running" || probe.Expires <= time.Now().Unix() || probe.Attempt != in.Attempt {
		return nil, Conflict("probe_expired", "This probe lease is no longer current.")
	}
	state := ""
	var manifest []byte
	switch in.Outcome {
	case "success":
		m, err := parseBetaManifest(in.Manifest)
		if err != nil {
			return nil, err
		}
		manifest, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		state = "valid"
	case "invalid", "retry":
		if len(in.Manifest) > 0 {
			return nil, Invalid("manifest", "A failed probe has no manifest.")
		}
		state = in.Outcome
	default:
		return nil, Invalid("outcome", "Use success, invalid or retry.")
	}
	_, err = c.App.DB().NewQuery("UPDATE drop_beta_probes SET state={:s},attempt='',expires_at=0,manifest={:m} WHERE digest={:d} AND contract={:v}").Bind(dbx.Params{"d": in.SHA256, "v": betaProbeContract, "s": state, "m": string(manifest)}).Execute()
	c.Event(Event{Detail: map[string]any{"sha256": in.SHA256, "state": state, "contract": betaProbeContract}})
	return map[string]any{"state": state}, err
}

func betaRelease(app core.App, digest string) (dropapi.BetaManifest, error) {
	var m dropapi.BetaManifest
	if err := betaApproved(app, digest); err != nil {
		return m, err
	}
	p, err := getBetaProbe(app, digest)
	if err != nil {
		return m, err
	}
	switch p.State {
	case "valid":
		return parseBetaManifest([]byte(p.Manifest))
	case "invalid":
		return m, Conflict("unsupported_binary", "This release cannot run on Drop Beta.")
	default:
		return m, Conflict("probe_pending", "This release is being prepared. Try again later.")
	}
}
