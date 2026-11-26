package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// Refusal is a condition only the operator can fix: entries in desired.json
// that Drop did not create, a desired.json Drop cannot read as its own, or
// a host whose hostId differs from the node's. The agent stops (exit 3) and
// writes nothing.
type Refusal struct{ Message string }

func (r *Refusal) Error() string { return r.Message }

// IsRefusal reports whether err is a Refusal.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

// Stores are the AWS views the agent uses: read only.
type Stores struct {
	Blobs   awsx.BlobReader
	Secrets awsx.SecretReader
}

// Local is what the agent learned from the host's files at start.
type Local struct {
	HostID  string
	Desired []byte // current desired.json, nil when missing
	AppIDs  []string
}

// docAppIDs returns the sorted distinct appIds of instances and volumes in a
// desired document read loosely.
func docAppIDs(doc []byte) []string {
	var loose struct {
		Instances []struct {
			AppID string `json:"appId"`
		} `json:"instances"`
		Volumes []struct {
			AppID string `json:"appId"`
		} `json:"volumes"`
	}
	if json.Unmarshal(doc, &loose) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, i := range loose.Instances {
		seen[i.AppID] = true
	}
	for _, v := range loose.Volumes {
		seen[v.AppID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func docField(doc []byte, field string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(doc, &m) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[field], &s)
	return s
}

// foreignRefusal names the entries Drop did not create.
func foreignRefusal(path string, ids []string) *Refusal {
	return &Refusal{Message: fmt.Sprintf("%s holds entries Drop did not create: %s. Drop owns the whole file and never deletes what it did not create; remove these entries (and their volumes) from desired.json, let nibrunner converge, then start the agent again",
		path, strings.Join(ids, ", "))}
}

// CheckLocal reads desired.json and reported.json and refuses what only the
// operator can fix. It writes nothing. A missing desired.json is accepted
// (the agent will create it) when reported.json names the host.
func CheckLocal(cfg Config, limits render.Limits) (*Local, error) {
	doc, err := readOptional(cfg.Desired)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", cfg.Desired, err)
	}
	rep, repErr := readReported(cfg.Reported)
	l := &Local{Desired: doc}
	if doc != nil {
		ids, err := render.ForeignAppIDs(doc)
		if err != nil {
			return nil, &Refusal{Message: fmt.Sprintf("%s is not a nibrunner desired document: %v", cfg.Desired, err)}
		}
		if len(ids) > 0 {
			return nil, foreignRefusal(cfg.Desired, ids)
		}
		l.HostID = docField(doc, "hostId")
		if l.HostID == "" {
			return nil, &Refusal{Message: cfg.Desired + " has no hostId"}
		}
		// Read it strictly, as Render will: only shapes Drop writes.
		if _, err := render.Render(render.Input{HostID: l.HostID, Previous: doc, Limits: limits}); err != nil {
			return nil, &Refusal{Message: fmt.Sprintf("%s is not a document Drop can take over: %v", cfg.Desired, err)}
		}
		l.AppIDs = docAppIDs(doc)
	}
	if repErr != nil {
		if l.HostID == "" {
			return nil, repErr
		}
	} else if rep != nil {
		if l.HostID == "" {
			l.HostID = rep.HostID
		} else if rep.HostID != "" && rep.HostID != l.HostID {
			return nil, &Refusal{Message: fmt.Sprintf("%s is for host %q but %s for %q", cfg.Desired, l.HostID, cfg.Reported, rep.HostID)}
		}
	}
	if l.HostID == "" {
		return nil, fmt.Errorf("neither %s nor %s names nibrunner's hostId; is nibrunner installed?", cfg.Desired, cfg.Reported)
	}
	return l, nil
}

// Agent is one running `drop agent run`.
type Agent struct {
	cfg     Config
	version string
	client  *Client
	stores  Stores
	secrets *Secrets
	stager  *Stager
	logf    func(string, ...any)
	now     func() time.Time

	hostID          string
	state           State
	deleted         []string
	intents         []appIntent
	appliedSeq      int64
	writtenRevision string
	pullSoon        bool
}

// New prepares an agent. version must match dropapi.AgentVersionPattern.
func New(cfg Config, secret, version string, stores Stores, logf func(string, ...any)) *Agent {
	if !dropapi.AgentVersionPattern.MatchString(version) {
		version = "dev"
	}
	return &Agent{
		cfg: cfg, version: version, client: NewClient(cfg, secret, version), stores: stores,
		secrets: &Secrets{Store: stores.Secrets, Prefix: cfg.SSMPrefix},
		logf:    logf, now: time.Now,
	}
}

func (a *Agent) limits(rep *nibReported) render.Limits {
	vcpu, mem := rep.allocatable()
	quarters := rep != nil && sanitizeVersions(rep.Versions)["cpuQuarterQuota"] == true
	return render.Limits{CPUQuarterQuota: quarters, AllowedHostSuffix: a.cfg.AllowedHostSuffix, SSMPrefix: a.cfg.SSMPrefix, AllocatableVCPU: vcpu, AllocatableMemoryMib: mem}
}

// Run starts the agent and blocks until ctx ends (nil) or a Refusal stops it.
func (a *Agent) Run(ctx context.Context) error {
	local, err := CheckLocal(a.cfg, a.limits(nil))
	if err != nil {
		return err
	}
	a.hostID = local.HostID
	state, err := loadState(a.cfg.StatePath())
	if err != nil {
		a.logf("%v", err)
	}
	a.state = state
	if a.state.HostID != "" && a.state.HostID != a.hostID {
		a.logf("state file was for host %q; starting a new one for %q", a.state.HostID, a.hostID)
		a.state = State{Schema: StateSchema, Artifacts: map[string]ArtifactInfo{}}
	}
	a.state.Node, a.state.HostID = a.cfg.Node, a.hostID
	a.appliedSeq = a.state.LastSeq
	a.writtenRevision = docField(local.Desired, "revision")
	a.stager = &Stager{Dir: a.cfg.Artifacts, Blobs: a.stores.Blobs, Cache: a.state.Artifacts}
	a.logf("starting for node %s on host %s (server %s, desired.json %s, %d apps)", a.cfg.Node, a.hostID, a.cfg.Server, a.cfg.Desired, len(local.AppIDs))

	if err := a.cfg.identity.clearGuestFiles(); err != nil {
		return err
	}
	if err := a.signIn(ctx); err != nil || ctx.Err() != nil {
		return err
	}
	wakes := make(chan Wake, 16)
	rtCtx, stopRealtime := context.WithCancel(ctx)
	defer stopRealtime()
	go (&Realtime{client: a.client, logf: a.logf, min: time.Second, max: 30 * time.Second}).Run(rtCtx, wakes)

	pull := time.NewTicker(a.cfg.PullInterval)
	defer pull.Stop()
	report := time.NewTicker(a.cfg.ReportInterval)
	defer report.Stop()
	refresh := time.NewTicker(RefreshInterval)
	defer refresh.Stop()

	if err := a.cycle(ctx); err != nil {
		return err
	}
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case w := <-wakes:
			if w.Connected || w.Seq > a.appliedSeq {
				err = a.cycle(ctx)
			}
		case <-pull.C:
			err = a.cycle(ctx)
		case <-report.C:
			err = a.report(ctx)
		case <-refresh.C:
			if rerr := a.client.Refresh(ctx); rerr != nil && ctx.Err() == nil {
				a.logf("token refresh: %v", rerr)
			}
		}
		for err == nil && a.pullSoon && ctx.Err() == nil {
			a.pullSoon = false
			err = a.cycle(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// signIn signs in, retrying until it works or ctx ends. A wrong secret or a
// disabled node keeps retrying slowly and never touches desired.json.
func (a *Agent) signIn(ctx context.Context) error {
	backoff := time.Second
	for {
		err := a.client.Login(ctx)
		if err == nil {
			a.state.NodeID = a.client.NodeID()
			a.logf("signed in as node %s", a.cfg.Node)
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		wait := backoff
		switch statusOf(err) {
		case http.StatusBadRequest:
			wait = time.Minute
			a.logf("sign-in refused: wrong node name or secret (rotated?); keeping desired.json, retrying in %s", wait)
		case http.StatusForbidden:
			wait = time.Minute
			a.logf("sign-in refused: the node is disabled; keeping desired.json, retrying in %s", wait)
		case http.StatusTooManyRequests:
			var api *APIError
			if errors.As(err, &api) && api.RetryAfter > 0 {
				wait = api.RetryAfter
			}
			a.logf("sign-in rate limited; retrying in %s", wait)
		default:
			a.logf("sign-in: %v; retrying in %s", err, wait)
			backoff = min(backoff*2, 30*time.Second)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// cycle is one reconcile followed by a report (the report carries each
// app's phase, which a pull can change without a write).
func (a *Agent) cycle(ctx context.Context) error {
	_, err := a.reconcile(ctx)
	if err != nil {
		if IsRefusal(err) {
			return err
		}
		a.state.LastError = err.Error()
		a.logf("pull: %v; desired.json left unchanged", err)
		a.saveState()
		return nil
	}
	return a.report(ctx)
}

func (a *Agent) saveState() {
	if err := saveState(a.cfg.StateDir, a.cfg.StatePath(), a.state); err != nil {
		a.logf("writing %s: %v", a.cfg.StatePath(), err)
	}
}

// checkEnvelope checks the whole snapshot before an omitted app can be treated as removed.
func checkEnvelope(d *dropapi.Desired) error {
	switch {
	case d.Schema != dropapi.DesiredSchema:
		return errors.New("unsupported desired document schema")
	case !dropapi.HostIDPattern.MatchString(d.HostID) || !dropapi.NodeNamePattern.MatchString(d.Node) || d.NodeID == "":
		return errors.New("invalid desired document identity")
	case d.State != "active" && d.State != "draining":
		return errors.New("invalid desired document state")
	case d.DesiredSeq < 1 || d.DesiredSeq > appspec.MaxDeploymentSeq:
		return errors.New("invalid desired sequence")
	case d.Apps == nil || d.Rejected == nil:
		return errors.New("desired document needs apps and rejected arrays")
	}
	hash, err := d.ComputeContentHash()
	if err != nil || hash != d.ContentHash {
		return errors.New("desired document content hash mismatch")
	}
	seen := map[string]bool{}
	for i, app := range d.Apps {
		if !appspec.AppIDPattern.MatchString(app.AppID) || (i > 0 && d.Apps[i-1].AppID >= app.AppID) {
			return errors.New("desired apps must have unique, sorted Drop app IDs")
		}
		seen[app.AppID] = true
	}
	for i, app := range d.Rejected {
		if !appspec.AppIDPattern.MatchString(app.AppID) || seen[app.AppID] || (i > 0 && d.Rejected[i-1].AppID >= app.AppID) {
			return errors.New("rejected apps must have unique, sorted Drop app IDs separate from apps")
		}
	}
	return nil
}

// reconcile pulls the whole desired document and, when the rendered file
// differs from desired.json, writes it. Any failure before the write leaves
// desired.json untouched.
func (a *Agent) reconcile(ctx context.Context) (wrote bool, err error) {
	d, err := a.client.Desired(ctx)
	if err != nil {
		if codeOf(err) == "node_disabled" {
			return false, errors.New("the node is disabled; keeping desired.json as it is")
		}
		return false, err
	}
	if d.HostID != a.hostID {
		return false, &Refusal{Message: fmt.Sprintf("the server's node %s is for host %q but this host is %q; check the node host_id", d.Node, d.HostID, a.hostID)}
	}
	a.state.LastPullAt = a.now().UTC().Format(time.RFC3339)

	current, err := readOptional(a.cfg.Desired)
	if err != nil {
		return false, err
	}
	previous := map[string]bool{}
	if current != nil {
		ids, err := render.ForeignAppIDs(current)
		if err != nil {
			return false, &Refusal{Message: fmt.Sprintf("%s is not a nibrunner desired document: %v", a.cfg.Desired, err)}
		}
		if len(ids) > 0 {
			return false, foreignRefusal(a.cfg.Desired, ids)
		}
		for _, id := range docAppIDs(current) {
			previous[id] = true
		}
	}

	var (
		apps    []render.App
		keep    []string
		intents []appIntent
		used    = map[secretKey]bool{}
		phases  = map[string]StateApp{}
	)
	fail := func(id, deployment, phase, message string) {
		intents = append(intents, appIntent{AppID: id, DeploymentID: deployment, AgentPhase: phase, Message: message})
		st := StateApp{AppID: id, DeploymentID: deployment, Phase: phase, Message: message}
		if previous[id] {
			keep = append(keep, id)
		}
		phases[id] = st
	}
	for _, r := range d.Rejected {
		fail(r.AppID, "", "failed", "The server rejected this app's configuration.")
	}
	hostApps := map[string]dropapi.DesiredApp{}
	revokedS3 := map[string]bool{}
	for _, app := range d.Apps {
		id := app.AppID
		revokedS3[id] = app.LocalOnly || app.DesiredState == "stopped"
		if !app.Limits.Valid() {
			fail(id, app.DeploymentID, "failed", "refused on the node: invalid host services limits")
			continue
		}
		if app.ManagedCrew && !app.LocalOnly && !a.cfg.HostServices {
			fail(id, app.DeploymentID, "failed", "Platform storage needs the node S3 proxy.")
			continue
		}
		if a.cfg.HostServices && app.ManagedCrew && !app.LocalOnly {
			app.HostS3 = true
		}
		if err := a.stager.Stage(ctx, app.Release); err != nil {
			fail(id, app.DeploymentID, "staging_failed", cut(err.Error(), dropapi.MaxReportMessage))
			continue
		}
		values, err := a.secrets.Resolve(ctx, id, app.DeploymentID, app.Secrets)
		if err != nil {
			fail(id, app.DeploymentID, "secret_failed", cut(err.Error(), dropapi.MaxReportMessage))
			continue
		}
		for _, s := range app.Secrets {
			used[secretKey{app.DeploymentID, s.Parameter, s.Version}] = true
		}
		apps = append(apps, app.RenderApp(values))
		if app.HostS3 && !revokedS3[id] {
			hostApps[id] = app
		}
		intents = append(intents, appIntent{GenericBeta: app.GenericBeta, AppID: id, DeploymentID: app.DeploymentID})
		phases[id] = StateApp{AppID: id, DeploymentID: app.DeploymentID, Phase: "rendered"}
	}
	a.secrets.Forget(used)

	rep, repErr := readReported(a.cfg.Reported)
	if repErr != nil {
		a.logf("%v", repErr)
	}
	if rep != nil && rep.HostID != a.hostID {
		rep = nil
	}
	sort.Strings(keep)
	out, err := render.Render(render.Input{HostID: a.hostID, DesiredSeq: d.DesiredSeq, Apps: apps, Keep: keep, Deleted: d.Deleted, Previous: current, Limits: a.limits(rep)})
	if err != nil {
		var rerr *render.Error
		if errors.As(err, &rerr) && (rerr.Code == render.CodeForeignEntries || rerr.Code == render.CodeHostMismatch ||
			rerr.Code == render.CodeInvalidPrevious || rerr.Code == render.CodeUnsupportedEntries) {
			return false, &Refusal{Message: fmt.Sprintf("%s: %v", a.cfg.Desired, err)}
		}
		return false, fmt.Errorf("render: %w", err)
	}
	for _, r := range out.Rejected {
		msg := cut(fmt.Sprintf("refused on the node: %s %s: %s", r.Code, r.Field, r.Message), dropapi.MaxReportMessage)
		for i := range intents {
			if intents[i].AppID == r.AppID && intents[i].AgentPhase == "" {
				intents[i].AgentPhase, intents[i].Message = "failed", msg
			}
		}
		phases[r.AppID] = StateApp{AppID: r.AppID, DeploymentID: phases[r.AppID].DeploymentID, Phase: "failed", Message: msg}
	}
	for _, id := range keep {
		st := phases[id]
		st.Phase, st.Message = "kept", st.Phase+": "+st.Message
		phases[id] = st
	}
	for _, id := range out.Tombstoned {
		phases[id] = StateApp{AppID: id, Phase: "tombstoned"}
	}

	if err := a.writeHostApps(hostApps, out.Rejected, keep, revokedS3); err != nil {
		return false, fmt.Errorf("writing %s: %w", HostServicesAppsFile, err)
	}

	currentHash := ""
	if current != nil {
		currentHash, _ = render.ContentHash(current)
	}
	if currentHash != out.ContentHash {
		if err := writeDesired(a.cfg.Desired, a.cfg.BackupPath(), current, out.Document); err != nil {
			return false, fmt.Errorf("writing %s: %w", a.cfg.Desired, err)
		}
		wrote = true
		a.writtenRevision = out.Revision
		a.state.LastWriteAt = a.now().UTC().Format(time.RFC3339)
		a.logf("wrote %s revision %s (%d apps, %d kept, %d tombstoned, %d refused)", a.cfg.Desired, out.Revision, len(apps), len(keep), len(out.Tombstoned), len(out.Rejected))
	} else {
		a.writtenRevision = docField(current, "revision")
	}
	a.deleted = d.Deleted
	a.appliedSeq = d.DesiredSeq
	a.intents = intents
	a.state.NodeID = a.client.NodeID()
	a.state.LastSeq = d.DesiredSeq
	a.state.LastRevision = a.writtenRevision
	a.state.LastContentHash = out.ContentHash
	a.state.LastError = ""
	a.state.Apps = a.state.Apps[:0]
	for _, st := range phases {
		a.state.Apps = append(a.state.Apps, st)
	}
	sort.Slice(a.state.Apps, func(i, j int) bool { return a.state.Apps[i].AppID < a.state.Apps[j].AppID })
	a.saveState()
	return wrote, nil
}

// report sends the heartbeat: the host part of reported.json and each app's
// phase.
func (a *Agent) report(ctx context.Context) error {
	rep, err := readReported(a.cfg.Reported)
	if err != nil {
		a.logf("%v", err)
	}
	body := buildReport(a.hostID, a.version, a.appliedSeq, a.intents, rep, a.writtenRevision, a.now())
	body.Deleted = confirmedVolumeRemovals(a.deleted, rep, a.hostID, a.writtenRevision)
	logs := filepath.Join(filepath.Dir(a.cfg.Reported), "logs")
	for _, intent := range a.intents {
		if !intent.GenericBeta {
			continue
		}
		for i := range body.Apps {
			app := &body.Apps[i]
			if app.AppID == intent.AppID && app.DeploymentID == intent.DeploymentID {
				app.LogTail = genericLogTail(logs, app.AppID, app.DeploymentID)
				break
			}
		}
	}
	if usage := hostUsage(filepath.Join(a.cfg.StateDir, HostServicesUsageFile)); len(usage) > 0 {
		for i := range body.Apps {
			if u := usage[body.Apps[i].DeploymentID]; u != nil && body.Apps[i].DeploymentID != "" {
				body.Apps[i].Usage = u
			}
		}
	}
	res, err := a.client.Report(ctx, body)
	if err != nil {
		if statusOf(err) == http.StatusConflict && codeOf(err) == "host_mismatch" {
			return &Refusal{Message: fmt.Sprintf("the server says this host (%s) is not node %s's host; stopping", a.hostID, a.cfg.Node)}
		}
		if ctx.Err() == nil {
			a.logf("report: %v", err)
		}
		return nil
	}
	a.state.LastReportAt = a.now().UTC().Format(time.RFC3339)
	if res.DesiredSeq > a.appliedSeq {
		a.pullSoon = true
	}
	return nil
}
