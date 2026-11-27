package dropserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Release operations (M5b). A release is one uploaded executable of an app;
// its bytes live in the release store at releases/<sha256> (Services.Releases)
// and its row in releases. People never send a file through an operation:
// release.upload_link returns a one-time link for PUT /api/drop/upload/<token>
// (upload.go), and release.deploy makes a release the app's current one.
// Ownership follows the apps: reads show admins every app's releases, writes
// only the owner's, and anything else is a 404.

func init() {
	register("release.list", releaseList)
	register("release.upload_link", releaseUploadLink)
	register("release.deploy", releaseDeploy)
	register("developer.upload_link", releaseUploadLink)
	register("developer.deploy", releaseDeploy)
}

// releasesUnavailable is the answer when the server has no release store.
func releasesUnavailable() *dropapi.Error {
	return &dropapi.Error{Status: http.StatusServiceUnavailable, Code: dropapi.CodeReleasesUnavailable,
		Message: "This server has no release store. Releases work on a hosted Drop (or with --fake-aws)."}
}

func releaseOut(r *core.Record, current string) dropapi.Release {
	return dropapi.Release{
		ArchiveSHA256: r.GetString("archive_sha256"), ExecutableSHA256: r.GetString("executable_sha256"), ExecutablePath: r.GetString("executable_path"),
		ID: r.Id, App: r.GetString("app"), SHA256: r.GetString("sha256"), SizeBytes: int64(r.GetInt("size_bytes")),
		S3Key: r.GetString("s3_key"), Note: r.GetString("note"), CreatedBy: r.GetString("created_by"),
		Created: dateString(r, "created"), Current: r.Id == current,
	}
}

// cleanNote checks a release note: at most 500 characters of one line of
// text, trimmed; empty is fine.
func cleanNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	bad := !utf8.ValidString(note) || utf8.RuneCountInString(note) > dropapi.MaxReleaseNote
	for _, r := range note {
		bad = bad || unicode.IsControl(r)
	}
	if bad {
		return "", Invalid("note", fmt.Sprintf("A note is at most %d characters of one line of text.", dropapi.MaxReleaseNote))
	}
	return note, nil
}

// setCurrentRelease makes release the app's current release and bumps its
// deployment when that changes it (the caller saves the app). It reports
// whether the deployment went up.
func setCurrentRelease(tx core.App, app, release *core.Record) (bool, error) {
	if pendingDiskRequest(app) {
		return false, resizeBusy()
	}
	if app.GetString("current_release") == release.Id {
		var needsStorage bool
		err := tx.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_credit_local_deploy l JOIN drop_credit_accounts c ON c.owner={:owner} WHERE l.app={:app} AND l.seq={:seq} AND c.balance>0)").Bind(dbx.Params{"owner": app.GetString("owner"), "app": app.Id, "seq": app.GetInt("deployment_seq")}).Row(&needsStorage)
		if err != nil || !needsStorage {
			return false, err
		}
	}
	app.Set("current_release", release.Id)
	if err := bumpDeployment(app); err != nil {
		return false, err
	}
	return true, nil
}

func releaseList(c *Ctx, in *dropapi.ReleaseListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	records, err := c.App.FindRecordsByFilter("releases", "app = {:app}", "-created,-id", limit+1, offset, dbx.Params{"app": app.Id})
	if err != nil {
		return nil, err
	}
	current := app.GetString("current_release")
	out := dropapi.ReleaseList{Releases: []dropapi.Release{}, HasMore: len(records) > limit, CurrentRelease: current}
	for i, r := range records {
		if i == limit {
			break
		}
		out.Releases = append(out.Releases, releaseOut(r, current))
	}
	return out, nil
}

// tokenHash is the stored form of an upload token.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func releaseUploadLink(c *Ctx, in *dropapi.ReleaseUploadLinkInput) (any, error) {
	app, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	if (c.Spec.Name == "developer.upload_link") != app.GetBool("developer_private") {
		return nil, NotFound("App")
	}
	if app.GetBool("developer_private") && in.Activate {
		return nil, Invalid("activate", "Upload the executable first, then use developer.deploy.")
	}
	if app.GetString("beta_digest") != "" && in.Activate {
		return nil, Forbidden("Deploy this app through Drop Beta after upload.")
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return nil, err
	}
	services := servicesOf(c.App)
	if services.Releases == nil {
		return nil, releasesUnavailable()
	}
	collection, err := c.App.FindCachedCollectionByNameOrId("upload_links")
	if err != nil {
		return nil, err
	}
	token := security.RandomString(dropapi.UploadTokenLength)
	expires := time.Now().UTC().Add(dropapi.UploadLinkTTL).Truncate(time.Millisecond)
	link := core.NewRecord(collection)
	link.Set("app", app.Id)
	link.Set("token_hash", tokenHash(token))
	link.Set("created_by", c.Auth.Id)
	link.Set("note", note)
	link.Set("activate", in.Activate)
	link.Set("expires_at", expires)
	if err := c.App.Save(link); err != nil {
		return nil, err
	}
	path := dropapi.UploadRoutePrefix + token
	url := c.publicURL() + path
	expiresAt := expires.Format(time.RFC3339)
	// The event never holds the token or the URL.
	c.Event(Event{App: app.Id, Detail: map[string]any{"link": link.Id, "expires_at": expiresAt, "activate": in.Activate}})
	return dropapi.UploadLink{
		URL: url, Path: path, Method: http.MethodPut, MaxBytes: services.ReleaseMaxBytes,
		ExpiresAt: expiresAt, Activate: in.Activate, Example: "curl -fT FILE '" + url + "'",
	}, nil
}

func releaseDeploy(c *Ctx, in *dropapi.ReleaseDeployInput) (any, error) {
	app, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	if (c.Spec.Name == "developer.deploy") != app.GetBool("developer_private") {
		return nil, NotFound("App")
	}
	if app.GetString("beta_digest") != "" {
		return nil, Forbidden("Deploy this app through Drop Beta.")
	}
	ref := strings.TrimSpace(in.Release)
	if !recordID.MatchString(ref) {
		return nil, NotFound("Release")
	}
	release, err := c.App.FindRecordById("releases", ref)
	if err != nil || release.GetString("app") != app.Id {
		return nil, NotFound("Release")
	}
	if app.GetBool("developer_private") {
		if err := betaPlacement(c, app); err != nil {
			return nil, err
		}
		// Placement fixes the volume size on its saved record.
		app, err = c.App.FindRecordById("apps", app.Id)
		if err != nil {
			return nil, err
		}
		if err := checkPrivateManifest(c.App, app, release); err != nil {
			return nil, err
		}
	}
	bumped, err := setCurrentRelease(c.App, app, release)
	if err != nil {
		return nil, err
	}
	if bumped {
		if err := c.App.Save(app); err != nil {
			return nil, err
		}
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{
		"release": release.Id, "sha256": release.GetString("sha256"),
		"deployment_bumped": bumped, "deployment_seq": app.GetInt("deployment_seq"),
	}})
	return dropapi.ReleaseDeployResult{
		App: c.appOut(app, nil), Release: releaseOut(release, app.GetString("current_release")),
		DeploymentBumped: bumped, DeploymentID: deploymentID(app),
	}, nil
}

func checkInstalledPrivateManifest(tx core.App, app *core.Record) error {
	if !app.GetBool("developer_private") || app.GetString("current_release") == "" {
		return nil
	}
	release, err := tx.FindRecordById("releases", app.GetString("current_release"))
	if err != nil {
		return err
	}
	return checkPrivateManifest(tx, app, release)
}

// The probe is performed per uploaded app release. A generic ELF with no
// manifest uses the owner's declared settings, never a catalog record.
func checkPrivateManifest(tx core.App, app, release *core.Record) error {
	var manifest dropapi.BetaManifest
	if raw := release.GetString("private_manifest"); raw != "" && raw != "null" {
		var err error
		manifest, err = parseBetaManifest([]byte(raw))
		if err != nil {
			return err
		}
	}
	if manifest.Schema == "" {
		app.Set("required_s3", false)
		return nil
	}
	if app.GetInt("http_port") != manifest.HTTPPort || app.GetInt("hook_port") != manifest.HookPort {
		return Invalid("app", "The app's HTTP and lifecycle ports must match the private release manifest.")
	}
	if app.GetInt("vcpu")*4 < manifest.Minimum.CPUQuarters || app.GetInt("memory_mib") < manifest.Minimum.MemoryMiB || app.GetInt("disk_mib") < manifest.Minimum.DiskMiB {
		return Invalid("app", "The app is smaller than the private release's minimum resources.")
	}
	if app.GetString("crew_enrollment") == "" && (slices.Contains(manifest.Requires, "s3_replica") || slices.Contains(manifest.Requires, "sign_in_email") || slices.Contains(manifest.Requires, "verified_owner_email")) {
		return Invalid("kind", "Create a private app with managed owner services (kind crew) for this release.")
	}
	if slices.Contains(manifest.Requires, "sign_in_email") && !app.GetBool("sign_in_email_enabled") {
		return Invalid("sign_in_email_enabled", "The release requires owner email sign-in.")
	}
	app.Set("required_s3", slices.Contains(manifest.Requires, "s3_replica"))
	if err := allowRequiredStorage(tx, app.GetString("owner"), manifest); err != nil {
		return err
	}
	return nil
}
