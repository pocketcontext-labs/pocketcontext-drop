package dropserver

import (
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() {
	register("catalog.list", officialList)
	register("catalog.upload", officialUpload)
	register("catalog.publish", officialPublish)
	register("catalog.archive", officialArchive)
	register("catalog.activate", officialActivate)
	register("workspace.create", workspaceCreate)
	register("workspace.resize", workspaceResize)
	register("workspace.upgrade", workspaceUpgrade)
}

type officialRelease struct {
	Archived  bool   `db:"archived" json:"archived"`
	Minimum   int    `db:"min_size" json:"minimum_multiplier"`
	Rehearsed bool   `db:"rehearsed" json:"rehearsed,omitempty"`
	Product   string `db:"product" json:"product"`
	Version   string `db:"version" json:"version"`
	Digest    string `db:"digest" json:"sha256"`
	Size      int64  `db:"size" json:"size_bytes"`
	Published int64  `db:"published" json:"published"`
}

func officialEnabled(app core.App) (bool, error) {
	var enabled bool
	err := app.DB().NewQuery("SELECT enabled FROM drop_official_config WHERE id=1").Row(&enabled)
	return enabled, err
}

func officialList(c *Ctx, in *dropapi.CatalogListInput) (any, error) {
	rows := []officialRelease{}
	// Only current published releases are public to customers. Operators also
	// see the last 50 candidate and superseded versions.
	query := `SELECT * FROM drop_official_releases r WHERE archived=0 AND published>0 AND NOT EXISTS
	(SELECT 1 FROM drop_official_releases n WHERE n.product=r.product AND n.archived=0 AND n.published>r.published) ORDER BY product`
	if c.Admin {
		query = `SELECT r.*,EXISTS(SELECT 1 FROM drop_official_rehearsals h WHERE h.candidate=r.digest AND h.baseline=
	    COALESCE((SELECT digest FROM drop_official_releases p WHERE p.product=r.product AND archived=0 AND published>0 ORDER BY published DESC LIMIT 1),'')) rehearsed
	    FROM drop_official_releases r WHERE r.digest IN (SELECT digest FROM drop_official_releases ORDER BY rowid DESC LIMIT 50)
	    OR (r.published>0 AND NOT EXISTS(SELECT 1 FROM drop_official_releases n WHERE n.product=r.product AND n.archived=0 AND n.published>r.published)) ORDER BY r.rowid DESC`
	}
	if in.AllVersions || in.IncludeArchived {
		query = `SELECT * FROM drop_official_releases r WHERE published>0
		AND (archived=0 OR {:include_archived})
		AND ({:all_versions} OR NOT EXISTS (SELECT 1 FROM drop_official_releases n
		  WHERE n.product=r.product AND n.published>r.published AND (n.archived=0 OR {:include_archived})))
		ORDER BY product,published DESC`
	}
	if err := c.App.DB().NewQuery(query).Bind(dbx.Params{"all_versions": in.AllVersions, "include_archived": in.IncludeArchived}).All(&rows); err != nil {
		return nil, err
	}
	enabled, err := officialEnabled(c.App)
	return map[string]any{"enabled": enabled, "releases": rows}, err
}

func officialUpload(c *Ctx, in *dropapi.OfficialUpload) (any, error) {
	if !slices.Contains([]string{"atlas", "crew"}, in.Product) || !betaVersionPattern.MatchString(in.Version) {
		return nil, Invalid("version", "Choose Crew or Atlas and enter a release version.")
	}
	if servicesOf(c.App).Releases == nil {
		return nil, releasesUnavailable()
	}
	token := security.RandomString(dropapi.UploadTokenLength)
	err := waitExec(c.App, `INSERT INTO drop_official_uploads(token_hash,owner,product,version,expires_at)
	VALUES({:hash},{:owner},{:product},{:version},{:expires})
	ON CONFLICT(owner) DO UPDATE SET token_hash=excluded.token_hash,product=excluded.product,version=excluded.version,expires_at=excluded.expires_at`,
		dbx.Params{"hash": tokenHash(token), "owner": c.Auth.Id, "product": in.Product, "version": in.Version, "expires": time.Now().Add(dropapi.UploadLinkTTL).Unix()})
	c.Event(Event{Detail: map[string]any{"product": in.Product, "version": in.Version}})
	return map[string]string{"path": dropapi.UploadRoutePrefix + token}, err
}

func officialPublish(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	p := dbx.Params{"digest": in.SHA256}
	var r officialRelease
	if err := c.App.DB().NewQuery("SELECT * FROM drop_official_releases WHERE digest={:digest}").Bind(p).One(&r); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFound("Candidate")
		}
		return nil, err
	}
	if r.Archived {
		return nil, Conflict("release_archived", "This release is archived.")
	}
	if _, err := betaRelease(c.App, r.Digest); err != nil {
		return nil, err
	}
	// A repeated publish of an older version never moves it ahead of newer releases.
	if r.Published == 0 {
		var baseline string
		if err := c.App.DB().NewQuery("SELECT COALESCE((SELECT digest FROM drop_official_releases WHERE product={:p} AND archived=0 AND published>0 ORDER BY published DESC LIMIT 1),'')").Bind(dbx.Params{"p": r.Product}).Row(&baseline); err != nil {
			return nil, err
		}
		if err := requireRehearsal(c.App, r.Digest, baseline); err != nil {
			return nil, err
		}
		if err := waitExec(c.App, `UPDATE drop_official_releases SET published=(SELECT MAX(COALESCE(MAX(published),0)+1,unixepoch()) FROM drop_official_releases) WHERE digest={:digest}`, p); err != nil {
			return nil, err
		}
	}
	// Adopt verified legacy deployments only when the exact running release,
	// product executable and managed volume agree. Their runtime stays pinned.
	if err := waitExec(c.App, `UPDATE apps SET official_product={:p},official_target={:d},official_minimum={:minimum} WHERE developer_private=0 AND official_product='' AND executable_name={:p}
	AND crew_enrollment!='' AND crew_owner_email!='' AND generic_beta=0 AND current_release IN
	(SELECT id FROM releases WHERE sha256={:d})`, dbx.Params{"p": r.Product, "d": r.Digest, "minimum": r.Minimum}); err != nil {
		return nil, err
	}
	c.Event(Event{Detail: map[string]any{"product": r.Product, "version": r.Version, "sha256": r.Digest}})
	return map[string]bool{"published": true}, nil
}

func officialArchive(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	result, err := c.App.DB().NewQuery("UPDATE drop_official_releases SET archived=1 WHERE digest={:d} AND published>0").Bind(dbx.Params{"d": in.SHA256}).Execute()
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, NotFound("Published release")
	}
	c.Event(Event{Detail: map[string]any{"sha256": in.SHA256}})
	return map[string]bool{"archived": true}, nil
}

func officialActivate(c *Ctx, _ *dropapi.MeInput) (any, error) {
	for _, product := range []string{"crew", "atlas"} {
		r, err := currentOfficial(c.App, product)
		if err != nil {
			return nil, err
		}
		if _, err = betaRelease(c.App, r.Digest); err != nil {
			return nil, err
		}
	}
	if err := waitExec(c.App, "UPDATE drop_official_config SET enabled=1 WHERE id=1", nil); err != nil {
		return nil, err
	}
	// Links handed out under the old deployment policy cannot cross cutover.
	if err := waitExec(c.App, "DELETE FROM upload_links WHERE used_at=''", nil); err != nil {
		return nil, err
	}
	if err := waitExec(c.App, "DELETE FROM drop_manifest_uploads", nil); err != nil {
		return nil, err
	}
	return map[string]bool{"enabled": true}, nil
}

func currentOfficial(app core.App, product string) (officialRelease, error) {
	var r officialRelease
	err := app.DB().NewQuery("SELECT * FROM drop_official_releases WHERE product={:product} AND archived=0 AND published>0 ORDER BY published DESC LIMIT 1").Bind(dbx.Params{"product": product}).One(&r)
	if errors.Is(err, sql.ErrNoRows) {
		err = Conflict("release_unavailable", "Publish a checked release for this product first.")
	}
	return r, err
}

func selectedOfficial(app core.App, in *dropapi.WorkspaceCreate) (officialRelease, error) {
	if in.SHA256 == "" {
		return currentOfficial(app, in.Product)
	}
	var r officialRelease
	err := app.DB().NewQuery("SELECT * FROM drop_official_releases WHERE product={:p} AND digest={:d} AND published>0").
		Bind(dbx.Params{"p": in.Product, "d": in.SHA256}).One(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return r, Conflict("release_unavailable", "Choose a published release of the selected app.")
	}
	if err == nil && r.Archived && !in.AllowArchived {
		return r, Conflict("release_archived", "Confirm the archived release to install it.")
	}
	return r, err
}

func workspaceCreate(c *Ctx, in *dropapi.WorkspaceCreate) (any, error) {
	enabled, err := officialEnabled(c.App)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, Conflict("catalog_pending", "The workspace catalog is not enabled yet.")
	}
	if !slices.Contains(crewBetaSizes, in.Multiplier) {
		return nil, Invalid("multiplier", "Choose a resource multiplier from 1 to 8.")
	}
	r, err := selectedOfficial(c.App, in)
	if err != nil {
		return nil, err
	}
	m, err := betaRelease(c.App, r.Digest)
	if err != nil {
		return nil, err
	}
	if m.App != r.Product {
		return nil, Conflict("candidate_invalid", "The release did not pass manifest checks.")
	}
	if in.Multiplier < r.Minimum {
		return nil, Invalid("multiplier", "This release requires at least "+strconv.Itoa(r.Minimum)+"× resources.")
	}
	port, hook := m.HTTPPort, m.HookPort
	cpu, mem, disk := (in.Multiplier+3)/4, in.Multiplier*256, in.Multiplier*2048
	if disk < m.Minimum.DiskMiB {
		return nil, Invalid("multiplier", "Choose a larger size for this release.")
	}
	raw, err := createApp(c, &dropapi.AppCreateInput{Name: in.Name, Slug: r.Product + "-" + core.GenerateDefaultRandomId(), Kind: "crew", ExecutableName: r.Product, VCPU: &cpu, MemoryMiB: &mem, DiskMiB: &disk, Env: map[string]string{"PORT": strconv.Itoa(port)}, HTTPPort: &port, HookPort: &hook, HealthKind: "tcp"}, runtimeFeatures{tieredDisk: true, storage: true, signInEmail: true, ownerEmail: true})
	if err != nil {
		return nil, err
	}
	a, err := c.App.FindRecordById("apps", raw.(dropapi.AppResult).App.ID)
	if err != nil {
		return nil, err
	}
	a.Set("official_product", r.Product)
	a.Set("official_target", r.Digest)
	a.Set("beta_digest", r.Digest)
	if err = c.App.Save(a); err != nil {
		return nil, err
	}
	if err = betaPlacement(c, a); err != nil {
		return nil, err
	}
	a, err = c.App.FindRecordById("apps", a.Id)
	if err != nil {
		return nil, err
	}
	return installOfficial(c, a, r)
}

func installOfficial(c *Ctx, a *core.Record, r officialRelease) (any, error) {
	key, err := awsx.ReleaseKey(r.Digest)
	if err != nil {
		return nil, err
	}
	release, _, err := findOrCreateRelease(c.App, a, r.Digest, key, r.Size, r.Version, c.Auth.Id)
	if err != nil {
		return nil, err
	}
	a.Set("official_target", r.Digest)
	a.Set("official_minimum", max(r.Minimum, a.GetInt("official_minimum")))
	// Operator-managed legacy workspaces keep their old renderer/quota mode
	// until the owner explicitly chooses a new compute size.
	if a.GetString("beta_digest") != "" {
		a.Set("beta_digest", r.Digest)
	}
	bumped, err := setCurrentRelease(c.App, a, release)
	if err != nil {
		return nil, err
	}
	if !bumped {
		raw, err := appStatus(c, &dropapi.StatusInput{App: a.Id})
		if err != nil {
			return nil, err
		}
		status := raw.(dropapi.Status)
		if status.ReportedDeploymentID == status.DeploymentID && failurePhases[status.Phase] {
			if err := bumpDeployment(a); err != nil {
				return nil, err
			}
		}
	}
	if err := c.App.Save(a); err != nil {
		return nil, err
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"product": r.Product, "version": r.Version, "sha256": r.Digest}})
	return dropapi.AppResult{App: c.appOut(a, nil)}, nil
}

func workspaceResize(c *Ctx, in *dropapi.WorkspaceResize) (any, error) {
	a, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	if a.GetString("official_product") == "" {
		return nil, Invalid("app", "Select an official workspace.")
	}
	if !slices.Contains(crewBetaSizes, in.Multiplier) {
		return nil, Invalid("multiplier", "Choose a resource multiplier from 1 to 8.")
	}
	if in.Multiplier < a.GetInt("official_minimum") {
		return nil, Invalid("multiplier", "The installed workspace requires a larger size. Upgrade it first to use smaller sizes.")
	}
	if r := workspaceDiskRequest(a); r != nil && r.Outcome == "pending" {
		if r.Target == in.Multiplier {
			return dropapi.AppResult{App: c.appOut(a, nil)}, nil
		}
		return nil, resizeBusy()
	}
	if in.Multiplier*2048 != a.GetInt("disk_mib") {
		p, err := placementOf(c.App, a.Id)
		if err != nil {
			return nil, err
		}
		supported, err := nodeSupportsResize(c.App, p)
		if err != nil {
			return nil, err
		}
		if supported {
			return startDiskResize(c, a, p, in.Multiplier)
		}
	}
	if in.Multiplier*256 < a.GetInt("memory_mib") && in.Multiplier*2048 < a.GetInt("disk_mib") {
		return nil, volumeDowngradeError(c, a, in.Multiplier*2048)
	}
	if a.GetInt("memory_mib") != in.Multiplier*256 || a.GetInt("vcpu") != (in.Multiplier+3)/4 {
		a.Set("beta_digest", a.GetString("official_target"))
		a.Set("vcpu", (in.Multiplier+3)/4)
		a.Set("memory_mib", in.Multiplier*256)
		if err := betaPlacement(c, a); err != nil {
			return nil, err
		}
		if err := bumpDeployment(a); err != nil {
			return nil, err
		}
		if err := c.App.Save(a); err != nil {
			return nil, err
		}
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"multiplier": in.Multiplier}})
	return dropapi.AppResult{App: c.appOut(a, nil)}, nil
}

func workspaceUpgrade(c *Ctx, in *dropapi.WorkspaceUpgrade) (any, error) {
	a, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	product := a.GetString("official_product")
	if product == "" {
		return nil, Invalid("app", "Select an official workspace.")
	}
	desired := a.GetString("official_target")
	var r officialRelease
	if in.SHA256 == desired {
		err = c.App.DB().NewQuery("SELECT * FROM drop_official_releases WHERE digest={:d} AND product={:p} AND published>0").Bind(dbx.Params{"d": desired, "p": product}).One(&r)
	} else {
		r, err = currentOfficial(c.App, product)
	}
	if err != nil {
		return nil, err
	}
	if in.SHA256 != r.Digest {
		return nil, Conflict("release_changed", "Review the current release before installing.")
	}
	if r.Digest != desired {
		if a.GetString("official_installed") != desired {
			return nil, Conflict("upgrade_pending", "The current deployment must be healthy before another release can be installed.")
		}
		if err := requireRehearsal(c.App, r.Digest, desired); err != nil {
			return nil, err
		}
	}
	m, err := betaRelease(c.App, r.Digest)
	if err != nil {
		return nil, err
	}
	cpuQuarters := betaFractionalQuota(a)
	if cpuQuarters == 0 {
		cpuQuarters = a.GetInt("vcpu") * 4
	}
	if cpuQuarters < m.Minimum.CPUQuarters || a.GetInt("memory_mib") < m.Minimum.MemoryMiB {
		return nil, Conflict("resize_required", "Increase workspace resources to at least "+strconv.Itoa(r.Minimum)+"× before installing this release.")
	}
	if m.App != product || m.HTTPPort != a.GetInt("http_port") || m.HookPort != a.GetInt("hook_port") || m.Minimum.DiskMiB > a.GetInt("disk_mib") {
		return nil, Conflict("incompatible_upgrade", "The new release requires a different workspace configuration.")
	}
	return installOfficial(c, a, r)
}
