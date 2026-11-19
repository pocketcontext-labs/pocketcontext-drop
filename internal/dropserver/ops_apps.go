package dropserver

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// App operations (M5a). Ownership: an app belongs to the account that created
// it; reads also show admins any app, writes only its owner, and another
// account's app is a 404 either way. Validation follows internal/appspec, the
// rules the node's renderer applies again, so the server refuses up front what
// the node would reject.

func init() {
	register("app.list", appList)
	register("app.get", appGet)
	register("app.create", appCreate)
	register("app.set", appSet)
	register("app.start", appStart)
	register("app.stop", appStop)
	register("app.archive", appArchive)
	register("app.restore", appRestore)
}

var slugPattern = regexp.MustCompile(dropapi.SlugPattern)

// findApp loads an app by record id or slug. Readers see their own apps and,
// for reads, admins any app; anything else is a 404.
func (c *Ctx) findApp(ref string, write bool) (*core.Record, error) {
	ref = strings.TrimSpace(ref)
	visible := func(r *core.Record) bool {
		return r.GetString("owner") == c.Auth.Id || (!write && c.Admin)
	}
	if recordID.MatchString(ref) {
		if r, err := c.App.FindRecordById("apps", ref); err == nil && visible(r) {
			return r, nil
		}
	}
	if slugPattern.MatchString(ref) {
		if r, err := c.App.FindFirstRecordByData("apps", "slug", ref); err == nil && visible(r) {
			return r, nil
		}
	}
	return nil, NotFound("App")
}

// writableApp loads an app its owner may change: not archived.
func (c *Ctx) writableApp(ref string) (*core.Record, error) {
	app, err := c.findApp(ref, true)
	if err != nil {
		return nil, err
	}
	if app.GetBool("archived") {
		return nil, Conflict(dropapi.CodeAppArchived, "This app is archived. Restore it first.")
	}
	return app, nil
}

func dateString(r *core.Record, field string) string {
	d := r.GetDateTime(field)
	if d.IsZero() {
		return ""
	}
	return d.String()
}

// appArgs and appEnv decode the JSON fields of an app record.
func appArgs(r *core.Record) []string {
	out := []string{}
	_ = r.UnmarshalJSONField("args", &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func appEnv(r *core.Record) map[string]string {
	out := map[string]string{}
	_ = r.UnmarshalJSONField("env", &out)
	if out == nil {
		out = map[string]string{}
	}
	return out
}

// appOut is the API form of an app record. emails caches owner addresses.
func (c *Ctx) appOut(r *core.Record, emails map[string]string) dropapi.App {
	owner := r.GetString("owner")
	email, ok := emails[owner]
	if !ok {
		if owner == c.Auth.Id {
			email = c.Auth.Email()
		} else if u, err := c.App.FindRecordById("users", owner); err == nil {
			email = u.Email()
		}
		if emails != nil {
			emails[owner] = email
		}
	}
	return dropapi.App{
		ID: r.Id, Name: r.GetString("name"), Slug: r.GetString("slug"), Owner: owner, OwnerEmail: email,
		NibAppID: r.GetString("nib_app_id"), VolumeID: r.GetString("volume_id"), Hostname: r.GetString("hostname"),
		DesiredState: r.GetString("desired_state"), IdleTimeoutMs: r.GetInt("idle_timeout_ms"),
		VCPU: r.GetInt("vcpu"), MemoryMiB: r.GetInt("memory_mib"), DiskMiB: r.GetInt("disk_mib"),
		HTTPPort: r.GetInt("http_port"), ExecutableName: r.GetString("executable_name"),
		Args: appArgs(r), Env: appEnv(r), HealthKind: r.GetString("health_kind"), HealthPath: r.GetString("health_path"),
		DeploymentSeq: r.GetInt("deployment_seq"), DeploymentID: deploymentID(r),
		Archived: r.GetBool("archived"), ArchivedAt: dateString(r, "archived_at"),
		Created: dateString(r, "created"), Updated: dateString(r, "updated"),
	}
}

// quota is the caller's app quota.
func (c *Ctx) quota() (dropapi.Quota, error) {
	used, err := c.App.CountRecords("apps", dbx.HashExp{"owner": c.Auth.Id, "archived": false})
	if err != nil {
		return dropapi.Quota{}, err
	}
	return dropapi.Quota{Used: int(used), Limit: c.Auth.GetInt("app_quota"), Unlimited: c.Admin}, nil
}

// checkQuota refuses one more non-archived app beyond the caller's quota.
// Operations run in one write transaction, so the count cannot race.
func (c *Ctx) checkQuota() error {
	q, err := c.quota()
	if err != nil || q.Unlimited {
		return err
	}
	if q.Used >= q.Limit {
		return Conflict(dropapi.CodeQuotaExceeded, fmt.Sprintf("Your app quota is %d and you have %d apps. Archive one, or ask an admin for a higher quota.", q.Limit, q.Used))
	}
	return nil
}

func appList(c *Ctx, in *dropapi.AppListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	if in.All && !c.Admin {
		return nil, Forbidden("Only admins list every account's apps.")
	}
	filter, params := "archived = {:archived}", dbx.Params{"archived": in.Archived}
	if !in.All {
		filter += " && owner = {:owner}"
		params["owner"] = c.Auth.Id
	}
	records, err := c.App.FindRecordsByFilter("apps", filter, "-created,-id", limit+1, offset, params)
	if err != nil {
		return nil, err
	}
	q, err := c.quota()
	if err != nil {
		return nil, err
	}
	out := dropapi.AppList{Apps: []dropapi.App{}, HasMore: len(records) > limit, Quota: q}
	emails := map[string]string{}
	for i, r := range records {
		if i == limit {
			break
		}
		out.Apps = append(out.Apps, c.appOut(r, emails))
	}
	return out, nil
}

func appGet(c *Ctx, in *dropapi.AppGetInput) (any, error) {
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	secrets, err := secretsOf(c.App, app)
	if err != nil {
		return nil, err
	}
	return dropapi.AppDetail{App: c.appOut(app, nil), Secrets: secrets}, nil
}

// settings is an app's configuration as the operations validate it.
type settings struct {
	desiredState, startState string
	idleTimeoutMs            int
	vcpu, memoryMiB, diskMiB int
	httpPort                 int
	executable               string
	args                     []string
	env                      map[string]string
	healthKind, healthPath   string
}

func settingsOf(r *core.Record) settings {
	return settings{
		desiredState: r.GetString("desired_state"), startState: r.GetString("start_state"),
		idleTimeoutMs: r.GetInt("idle_timeout_ms"), vcpu: r.GetInt("vcpu"), memoryMiB: r.GetInt("memory_mib"),
		diskMiB: r.GetInt("disk_mib"), httpPort: r.GetInt("http_port"), executable: r.GetString("executable_name"),
		args: appArgs(r), env: appEnv(r), healthKind: r.GetString("health_kind"), healthPath: r.GetString("health_path"),
	}
}

func (s settings) apply(r *core.Record) {
	if s.args == nil {
		s.args = []string{}
	}
	if s.env == nil {
		s.env = map[string]string{}
	}
	r.Set("desired_state", s.desiredState)
	r.Set("start_state", s.startState)
	r.Set("idle_timeout_ms", s.idleTimeoutMs)
	r.Set("vcpu", s.vcpu)
	r.Set("memory_mib", s.memoryMiB)
	r.Set("disk_mib", s.diskMiB)
	r.Set("http_port", s.httpPort)
	r.Set("executable_name", s.executable)
	r.Set("args", s.args)
	r.Set("env", s.env)
	r.Set("health_kind", s.healthKind)
	r.Set("health_path", s.healthPath)
}

// setDesiredState changes the desired state; a running or on-request state
// is also the one app.start returns to.
func (s *settings) setDesiredState(state string) {
	s.desiredState = state
	if state != appspec.StateStopped {
		s.startState = state
	}
}

func between(field string, v, lo, hi int, unit string) error {
	if v < lo || v > hi {
		return Invalid(field, fmt.Sprintf("%s must be %d-%d%s.", field, lo, hi, unit))
	}
	return nil
}

// check validates a configuration. secretNames are the app's secrets, which
// share the environment with the plain variables.
func (s settings) check(secretNames []string) error {
	if !appspec.ValidDesiredState(s.desiredState) {
		return Invalid("desired_state", "desired_state must be running, on-request or stopped.")
	}
	checks := []error{
		between("idle_timeout_ms", s.idleTimeoutMs, appspec.MinIdleMs, appspec.MaxIdleMs, " ms"),
		between("vcpu", s.vcpu, appspec.MinVCPU, appspec.MaxVCPU, ""),
		between("memory_mib", s.memoryMiB, appspec.MinMemoryMiB, appspec.MaxMemoryMiB, " MiB"),
		between("disk_mib", s.diskMiB, appspec.MinDiskMiB, appspec.MaxDiskMiB, " MiB"),
		between("http_port", s.httpPort, appspec.MinPort, appspec.MaxPort, ""),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	if !appspec.ExecutablePattern.MatchString(s.executable) {
		return Invalid("executable_name", "executable_name must be 1-32 lowercase letters, digits and hyphens, starting with a letter.")
	}
	if field, reason := appspec.CheckArgs(s.args); field != "" {
		return Invalid("args", field+": "+reason+".")
	}
	if field, reason := appspec.CheckHealth(s.healthKind, s.healthPath); field != "" {
		return Invalid("health_"+field, "health "+field+": "+reason+".")
	}
	for _, name := range slices.Sorted(maps.Keys(s.env)) {
		if reason := appspec.CheckEnvName(name); reason != "" {
			return Invalid("env", "env: "+reason+".")
		}
		if reason := appspec.CheckEnvValue(name, s.env[name]); reason != "" {
			return Invalid("env", "env: "+reason+".")
		}
		if slices.Contains(secretNames, name) {
			return Invalid("env", "env: "+name+" is a secret of this app; delete the secret first.")
		}
	}
	if len(s.env)+len(secretNames) > appspec.MaxEnvEntries {
		return Invalid("env", fmt.Sprintf("An app has at most %d environment variables and secrets together.", appspec.MaxEnvEntries))
	}
	if raw, _ := json.Marshal(s.env); len(raw) > dropapi.MaxEnvJSONBytes {
		return Invalid("env", fmt.Sprintf("The environment is at most %d KiB as JSON.", dropapi.MaxEnvJSONBytes>>10))
	}
	return nil
}

// cleanName checks an app name: 1-80 characters of text, trimmed.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	bad := !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > dropapi.MaxAppName
	for _, r := range name {
		bad = bad || unicode.IsControl(r)
	}
	if bad {
		return "", Invalid("name", fmt.Sprintf("The name must be 1-%d characters of text.", dropapi.MaxAppName))
	}
	return name, nil
}

func appCreate(c *Ctx, in *dropapi.AppCreateInput) (any, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return nil, err
	}
	if !slugPattern.MatchString(in.Slug) {
		return nil, Invalid("slug", "The slug must be "+dropapi.SlugRule+".")
	}
	if slices.Contains(dropapi.ReservedSlugs, in.Slug) {
		return nil, Invalid("slug", "The slug "+in.Slug+" is reserved.")
	}
	if err := c.checkQuota(); err != nil {
		return nil, err
	}
	if _, err := c.App.FindFirstRecordByData("apps", "slug", in.Slug); err == nil {
		return nil, Conflict(dropapi.CodeSlugTaken, "The slug "+in.Slug+" is taken. Choose another.")
	}
	orInt := func(v *int, fallback int) int {
		if v == nil {
			return fallback
		}
		return *v
	}
	orString := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}
	s := settings{
		idleTimeoutMs: orInt(in.IdleTimeoutMs, appspec.DefaultIdleMs), vcpu: orInt(in.VCPU, appspec.DefaultVCPU),
		memoryMiB: orInt(in.MemoryMiB, appspec.DefaultMemoryMiB), diskMiB: orInt(in.DiskMiB, appspec.DefaultDiskMiB),
		httpPort: orInt(in.HTTPPort, appspec.DefaultPort), executable: orString(in.ExecutableName, appspec.DefaultExecutable),
		args: in.Args, env: in.Env, healthKind: orString(in.HealthKind, appspec.HealthTCP), healthPath: in.HealthPath,
		startState: appspec.StateOnRequest,
	}
	s.setDesiredState(orString(in.DesiredState, appspec.StateOnRequest))
	if s.healthKind == appspec.HealthHTTP && s.healthPath == "" {
		s.healthPath = "/"
	}
	if err := s.check(nil); err != nil {
		return nil, err
	}
	collection, err := c.App.FindCachedCollectionByNameOrId("apps")
	if err != nil {
		return nil, err
	}
	app := core.NewRecord(collection)
	app.Id = core.GenerateDefaultRandomId()
	nibAppID := appspec.AppID(app.Id)
	app.Set("owner", c.Auth.Id)
	app.Set("name", name)
	app.Set("slug", in.Slug)
	app.Set("nib_app_id", nibAppID)
	app.Set("volume_id", appspec.VolumeID(nibAppID))
	if domain := servicesOf(c.App).AppDomain; domain != "" {
		app.Set("hostname", in.Slug+"."+domain)
	}
	s.apply(app)
	app.Set("deployment_seq", 1)
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"name": name, "slug": in.Slug}})
	return dropapi.AppResult{App: c.appOut(app, nil)}, nil
}

func appSet(c *Ctx, in *dropapi.AppSetInput) (any, error) {
	app, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	s := settingsOf(app)
	var changed []string
	if in.Name != nil {
		name, err := cleanName(*in.Name)
		if err != nil {
			return nil, err
		}
		app.Set("name", name)
		changed = append(changed, "name")
	}
	if in.DesiredState != nil {
		if !appspec.ValidDesiredState(*in.DesiredState) {
			return nil, Invalid("desired_state", "desired_state must be running, on-request or stopped.")
		}
		s.setDesiredState(*in.DesiredState)
		changed = append(changed, "desired_state")
	}
	ints := []struct {
		field string
		value *int
		into  *int
	}{
		{"idle_timeout_ms", in.IdleTimeoutMs, &s.idleTimeoutMs}, {"vcpu", in.VCPU, &s.vcpu},
		{"memory_mib", in.MemoryMiB, &s.memoryMiB}, {"disk_mib", in.DiskMiB, &s.diskMiB}, {"http_port", in.HTTPPort, &s.httpPort},
	}
	for _, f := range ints {
		if f.value != nil {
			*f.into = *f.value
			changed = append(changed, f.field)
		}
	}
	// TODO(M7): refuse a disk change once the app has been placed
	// (409 disk_locked): the volume's size is fixed at its first placement.
	if in.ExecutableName != nil {
		s.executable = *in.ExecutableName
		changed = append(changed, "executable_name")
	}
	if in.Args != nil {
		s.args = *in.Args
		changed = append(changed, "args")
	}
	detail := map[string]any{}
	if in.Env != nil {
		set, unset := in.Env.Set, in.Env.Unset
		for _, name := range unset {
			if _, both := set[name]; both {
				return nil, Invalid("env", "env: "+name+" is both set and unset.")
			}
			delete(s.env, name)
		}
		for name, value := range set {
			s.env[name] = value
		}
		// Names only: values may be sensitive even when not secret.
		if len(set) > 0 {
			detail["env_set"] = slices.Sorted(maps.Keys(set))
		}
		if len(unset) > 0 {
			detail["env_unset"] = slices.Sorted(slices.Values(unset))
		}
		changed = append(changed, "env")
	}
	if in.HealthKind != nil {
		s.healthKind = *in.HealthKind
		changed = append(changed, "health_kind")
		if in.HealthPath == nil {
			// Switching kinds without a path: tcp has none, http checks "/".
			switch s.healthKind {
			case appspec.HealthTCP:
				s.healthPath = ""
			case appspec.HealthHTTP:
				if s.healthPath == "" {
					s.healthPath = "/"
				}
			}
		}
	}
	if in.HealthPath != nil {
		s.healthPath = *in.HealthPath
		changed = append(changed, "health_path")
	}
	if len(changed) == 0 {
		return nil, Invalid("app", "Change at least one setting.")
	}
	names, err := secretNames(c.App, app)
	if err != nil {
		return nil, err
	}
	if err := s.check(names); err != nil {
		return nil, err
	}
	s.apply(app)
	bumped := instanceConfigChanged(app)
	if bumped {
		if err := bumpDeployment(app); err != nil {
			return nil, err
		}
	}
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	detail["changed"] = changed
	detail["deployment_bumped"] = bumped
	detail["deployment_seq"] = app.GetInt("deployment_seq")
	c.Event(Event{App: app.Id, Detail: detail})
	return dropapi.AppSetResult{App: c.appOut(app, nil), DeploymentBumped: bumped}, nil
}

// setState starts or stops an app. Neither bumps the deployment: nibrunner
// applies the desired state to the current one.
func setState(c *Ctx, ref string, start bool) (any, error) {
	app, err := c.writableApp(ref)
	if err != nil {
		return nil, err
	}
	state := appspec.StateStopped
	if start {
		state = app.GetString("start_state")
	}
	app.Set("desired_state", state)
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"desired_state": state}})
	return dropapi.AppResult{App: c.appOut(app, nil)}, nil
}

func appStart(c *Ctx, in *dropapi.AppStartInput) (any, error) { return setState(c, in.App, true) }
func appStop(c *Ctx, in *dropapi.AppStopInput) (any, error)   { return setState(c, in.App, false) }

func appArchive(c *Ctx, in *dropapi.AppArchiveInput) (any, error) {
	app, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	app.Set("archived", true)
	app.Set("archived_at", time.Now().UTC())
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"slug": app.GetString("slug")}})
	return dropapi.AppResult{App: c.appOut(app, nil)}, nil
}

func appRestore(c *Ctx, in *dropapi.AppRestoreInput) (any, error) {
	app, err := c.findApp(in.App, true)
	if err != nil {
		return nil, err
	}
	if !app.GetBool("archived") {
		return nil, Conflict(dropapi.CodeAppNotArchived, "This app is not archived.")
	}
	if err := c.checkQuota(); err != nil {
		return nil, err
	}
	app.Set("archived", false)
	app.Set("archived_at", "")
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"slug": app.GetString("slug")}})
	return dropapi.AppResult{App: c.appOut(app, nil)}, nil
}
