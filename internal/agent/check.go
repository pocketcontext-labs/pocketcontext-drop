package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// CheckItem is one line of `drop agent check`.
type CheckItem struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// CheckResult is what `drop agent check` prints. Refused is true when
// desired.json holds something only the operator can fix (exit 3).
type CheckResult struct {
	OK      bool        `json:"ok"`
	Refused bool        `json:"refused"`
	Node    string      `json:"node"`
	HostID  string      `json:"hostId"`
	Checks  []CheckItem `json:"checks"`
}

func (r *CheckResult) add(name string, err error, okMessage string) bool {
	item := CheckItem{Name: name, OK: err == nil, Message: okMessage}
	if err != nil {
		item.Message = err.Error()
		r.OK = false
		if IsRefusal(err) {
			r.Refused = true
		}
	}
	r.Checks = append(r.Checks, item)
	return err == nil
}

// Check runs every check the agent's start depends on, and writes nothing:
// desired.json (no foreign entries, a document Drop can take over, the
// host), reported.json, the node secret, sign-in, the server's desired
// document for this host, and for each app its release in S3 (HeadObject)
// and its secret versions in SSM (values read and discarded).
func Check(ctx context.Context, cfg Config, stores Stores, version string) *CheckResult {
	res := &CheckResult{OK: true, Node: cfg.Node}
	limits := render.Limits{AllowedHostSuffix: cfg.AllowedHostSuffix, SSMPrefix: cfg.SSMPrefix}
	local, err := CheckLocal(cfg, limits)
	if res.add("desired.json", err, "") {
		res.HostID = local.HostID
		msg := fmt.Sprintf("%s: host %s, %d Drop apps, no foreign entries", cfg.Desired, local.HostID, len(local.AppIDs))
		if local.Desired == nil {
			msg = fmt.Sprintf("%s is missing; the agent will create it for host %s", cfg.Desired, local.HostID)
		}
		res.Checks[len(res.Checks)-1].Message = msg
	}
	rep, err := readReported(cfg.Reported)
	if err == nil && rep == nil {
		err = fmt.Errorf("%s is missing; is nibrunner running?", cfg.Reported)
	}
	if res.add("reported.json", err, "") {
		res.Checks[len(res.Checks)-1].Message = fmt.Sprintf("%s: host %s, state %s, reported at %s", cfg.Reported, rep.HostID, cut(rep.State, 64), rep.ReportedAt)
	}
	for _, dir := range []struct{ name, path string }{{"artifacts", cfg.Artifacts}, {"state", cfg.StateDir}} {
		info, err := os.Stat(dir.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			res.add(dir.name+" directory", nil, dir.path+" does not exist yet; the agent creates it (0700)")
		case err != nil:
			res.add(dir.name+" directory", err, "")
		case !info.IsDir():
			res.add(dir.name+" directory", fmt.Errorf("%s is not a directory", dir.path), "")
		default:
			res.add(dir.name+" directory", nil, dir.path)
		}
	}
	secret, err := ReadSecret(cfg.SecretFile)
	if !res.add("node secret", err, cfg.SecretFile) {
		return res
	}
	client := NewClient(cfg, secret, version)
	if !res.add("sign-in", client.Login(ctx), fmt.Sprintf("signed in to %s as node %s", cfg.Server, cfg.Node)) {
		return res
	}
	d, err := client.Desired(ctx)
	if err == nil && local != nil && d.HostID != local.HostID {
		err = &Refusal{Message: fmt.Sprintf("the server's node is for host %q but this host is %q", d.HostID, local.HostID)}
	}
	if !res.add("desired document", err, "") {
		return res
	}
	res.Checks[len(res.Checks)-1].Message = fmt.Sprintf("desiredSeq %d, state %s, %d apps, %d refused by the server", d.DesiredSeq, d.State, len(d.Apps), len(d.Rejected))
	if len(d.Apps) == 0 {
		res.add("releases", nil, "no apps placed on this node; nothing to probe in S3")
		res.add("secrets", nil, "no apps placed on this node; nothing to probe in SSM")
		return res
	}
	var heads, probes []error
	nSecrets := 0
	for _, app := range d.Apps {
		if app.PrivateRoute != nil && !cfg.PrivateIngress {
			heads = append(heads, fmt.Errorf("%s: private app ingress is disabled on this node", app.AppID))
			continue
		}
		if err := checkRelease(app.Release); err != nil {
			heads = append(heads, fmt.Errorf("%s: %w", app.AppID, err))
		} else if info, err := stores.Blobs.Head(ctx, app.Release.S3Key); err != nil {
			heads = append(heads, fmt.Errorf("%s: %s: %v", app.AppID, app.Release.S3Key, err))
		} else if info.Size != storedReleaseSize(app.Release) {
			heads = append(heads, fmt.Errorf("%s: %s has %d bytes, expected %d", app.AppID, app.Release.S3Key, info.Size, storedReleaseSize(app.Release)))
		}
		probe := &Secrets{Store: stores.Secrets, Prefix: cfg.SSMPrefix}
		if _, err := probe.Resolve(ctx, app.AppID, app.DeploymentID, app.Secrets); err != nil {
			probes = append(probes, fmt.Errorf("%s: %v", app.AppID, err))
		}
		nSecrets += len(app.Secrets)
	}
	res.add("releases", errors.Join(heads...), fmt.Sprintf("%d releases found in the blob store", len(d.Apps)))
	res.add("secrets", errors.Join(probes...), fmt.Sprintf("%d secret versions readable", nSecrets))
	return res
}

// DryRun is what `drop agent render` prints.
type DryRun struct {
	Revision    string            `json:"revision"`
	ContentHash string            `json:"contentHash"`
	Changed     bool              `json:"changed"`
	Rejected    []render.AppError `json:"rejected"`
	Kept        []string          `json:"kept"`
	Tombstoned  []string          `json:"tombstoned"`
	Document    json.RawMessage   `json:"document"`
}

// RedactedValue replaces every secret value in a dry run.
const RedactedValue = "REDACTED"

// RenderDry renders a drop.desired/v1 document (as GET /api/drop/agent/desired
// returns it) against previous (the current desired.json, or nil), offline:
// no server, no S3, no SSM. Secret values are RedactedValue and releases are
// not staged. Server-refused apps keep their previous entries as in run.
func RenderDry(input, previous []byte, limits render.Limits) (*DryRun, error) {
	d, err := decodeDesired(input)
	if err != nil {
		return nil, err
	}
	prevIDs := map[string]bool{}
	if len(bytes.TrimSpace(previous)) > 0 {
		ids, err := render.ForeignAppIDs(previous)
		if err != nil {
			return nil, &Refusal{Message: "the previous document is not a nibrunner desired document: " + err.Error()}
		}
		if len(ids) > 0 {
			return nil, foreignRefusal("the previous document", ids)
		}
		for _, id := range docAppIDs(previous) {
			prevIDs[id] = true
		}
	}
	apps := make([]render.App, 0, len(d.Apps))
	for _, app := range d.Apps {
		values := map[string]string{}
		for _, s := range app.Secrets {
			values[s.Name] = RedactedValue
		}
		apps = append(apps, app.RenderApp(values))
	}
	var keep []string
	for _, r := range d.Rejected {
		if prevIDs[r.AppID] {
			keep = append(keep, r.AppID)
		}
	}
	sort.Strings(keep)
	if len(bytes.TrimSpace(previous)) > 0 {
		previous, err = render.PreviewPrevious(previous, apps, RedactedValue)
		if err != nil {
			return nil, err
		}
	}
	out, err := render.Render(render.Input{HostID: d.HostID, DesiredSeq: d.DesiredSeq, Apps: apps, Keep: keep, Deleted: d.Deleted, Previous: previous, Limits: limits})
	if err != nil {
		var rerr *render.Error
		if errors.As(err, &rerr) && rerr.Code == render.CodeForeignEntries {
			return nil, &Refusal{Message: err.Error()}
		}
		return nil, err
	}
	changed := true
	if len(bytes.TrimSpace(previous)) > 0 {
		if h, err := render.ContentHash(previous); err == nil && h == out.ContentHash {
			changed = false
		}
	}
	return &DryRun{Revision: out.Revision, ContentHash: out.ContentHash, Changed: changed, Rejected: nonNil(out.Rejected),
		Kept: nonNil(keep), Tombstoned: nonNil(out.Tombstoned), Document: out.Document}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
