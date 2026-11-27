package dropserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"
	"golang.org/x/time/rate"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/privateblob"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/releasebundle"
)

// The upload route, PUT /api/drop/upload/<token> (plan 4.2): anonymous, and
// scoped to one link that release.upload_link created. It is on the public
// allowlist (droppolicy.PublicRoute) only for a 32-character token.
//
//  1. A per-client-address limiter and a cap on concurrent uploads (429).
//  2. The token's hash must name an unused, unexpired link of an app that is
//     not archived, created by an account that is not suspended; any other
//     token gets the same 404, so links cannot be probed.
//  3. The body streams to a private temporary file while it is hashed, at
//     most Services.ReleaseMaxBytes (90 MiB) (413 beyond). Its first 64 bytes
//     must be a linux/amd64 ELF header (dropapi.ReleaseHeaderProblem; 400
//     invalid_release, checked before the rest is read).
//  4. The file goes to the release store with PutIfAbsent at
//     releases/<sha256>: an object already there is the same bytes.
//  5. One transaction creates the release (or reuses the app's release of
//     the same bytes), marks the link used, deploys the release when the
//     link says activate, and writes one release.upload event.
//
// A link is used up only by a successful upload: a refused body (too large,
// not an executable) leaves it usable until it expires. Two uploads to one
// link at the same time get 409 upload_in_progress.

const (
	// uploadStoreTimeout bounds the release store call.
	uploadStoreTimeout = 5 * time.Minute
	// maxConcurrentUploads bounds the temporary files on disk at once.
	maxConcurrentUploads = 4
)

type uploader struct {
	mu       sync.Mutex
	clients  map[string]*uploadClient
	inFlight map[string]bool // token hashes being uploaded
	slots    chan struct{}
	now      func() time.Time
}

type uploadClient struct {
	limiter *rate.Limiter
	seen    time.Time
}

func newUploader() *uploader {
	return &uploader{clients: map[string]*uploadClient{}, inFlight: map[string]bool{},
		slots: make(chan struct{}, maxConcurrentUploads), now: time.Now}
}

// allow spends one attempt of a client address: 6 at once, then one every
// 10 seconds. Idle addresses are forgotten after 10 minutes.
func (u *uploader) allow(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if len(u.clients) > 4096 {
		for k, c := range u.clients {
			if now.Sub(c.seen) > 10*time.Minute {
				delete(u.clients, k)
			}
		}
	}
	c := u.clients[host]
	if c == nil {
		c = &uploadClient{limiter: rate.NewLimiter(rate.Every(10*time.Second), 6)}
		u.clients[host] = c
	}
	c.seen = now
	return c.limiter.AllowN(now, 1)
}

func (u *uploader) claim(hash string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.inFlight[hash] {
		return false
	}
	u.inFlight[hash] = true
	return true
}

func (u *uploader) release(hash string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.inFlight, hash)
}

// registerUploadRoute adds PUT /api/drop/upload/{token}. PocketBase's global
// 32 MB body limit is lifted for this route only; the handler enforces the
// release cap itself (and core's public handler PublicBodyLimit above it).
func registerUploadRoute(e *core.ServeEvent) {
	u := newUploader()
	e.Router.PUT(dropapi.UploadRoutePrefix+"{token}", u.serve).Bind(apis.BodyLimit(0)).BindFunc(redactUploadToken)
}

// redactUploadToken keeps the token out of PocketBase's request log, which
// records the URL after the handler returns: the logged path ends in
// [redacted]. (The handler has already read the token from the path value.)
func redactUploadToken(re *core.RequestEvent) error {
	err := re.Next()
	redacted := *re.Request.URL
	redacted.Path, redacted.RawPath, redacted.RawQuery = dropapi.UploadRoutePrefix+"[redacted]", "", ""
	re.Request.URL = &redacted
	return err
}

func uploadLinkInvalid() *dropapi.Error {
	return &dropapi.Error{Status: http.StatusNotFound, Code: dropapi.CodeUploadLinkInvalid,
		Message: "This upload link does not exist, was used or has expired. Ask for a new one."}
}

func releaseTooLarge(max int64) *dropapi.Error {
	return &dropapi.Error{Status: http.StatusRequestEntityTooLarge, Code: dropapi.CodeReleaseTooLarge,
		Message: "A release is at most " + strconv.FormatInt(max>>20, 10) + " MiB.", Data: map[string]any{"max_bytes": max}}
}

func invalidRelease(message string) *dropapi.Error {
	return &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeInvalidRelease, Message: message}
}

func rateLimited(re *core.RequestEvent, message string) error {
	re.Response.Header().Set("Retry-After", "10")
	return writeError(re, &dropapi.Error{Status: http.StatusTooManyRequests, Code: dropapi.CodeRateLimited, Message: message})
}

// usableLink loads the link of a token hash if it can take an upload now.
func usableLink(app core.App, hash string, now time.Time) (*core.Record, error) {
	var links []*core.Record
	if err := app.RecordQuery("upload_links").AndWhere(dbx.HashExp{"token_hash": hash}).Limit(1).All(&links); err != nil {
		return nil, err
	}
	if len(links) == 0 || !links[0].GetDateTime("used_at").IsZero() || !links[0].GetDateTime("expires_at").Time().After(now) {
		return nil, nil
	}
	return links[0], nil
}

func canUploadRelease(tx core.App, app, account *core.Record) bool {
	if app.GetBool("developer_private") {
		return account.GetBool("developer")
	}
	return !hostedAdmin(tx, account)
}

func (u *uploader) serve(re *core.RequestEvent) error {
	re.Response.Header().Set("Cache-Control", "no-store")
	token := re.Request.PathValue("token")
	if !dropapi.UploadTokenPattern.MatchString(token) {
		return writeError(re, uploadLinkInvalid())
	}
	if !u.allow(re.Request.RemoteAddr) {
		return rateLimited(re, "Too many uploads from this address. Try again shortly.")
	}
	hash := tokenHash(token)
	link, err := usableLink(re.App, hash, u.now())
	if err != nil {
		return writeError(re, err)
	}
	if link == nil {
		return u.candidateOrProbe(re, hash)
	}
	app, err := re.App.FindRecordById("apps", link.GetString("app"))
	if err != nil {
		return writeError(re, uploadLinkInvalid())
	}
	creator, err := re.App.FindRecordById("users", link.GetString("created_by"))
	if err != nil || !canUploadRelease(re.App, app, creator) || creator.GetBool("suspended") || app.GetString("owner") != creator.Id {
		return writeError(re, uploadLinkInvalid())
	}
	if app.GetBool("archived") {
		return writeError(re, Conflict(dropapi.CodeAppArchived, "This app is archived. Restore it first."))
	}
	services := servicesOf(re.App)
	if services.Releases == nil {
		return writeError(re, releasesUnavailable())
	}
	max := services.ReleaseMaxBytes
	if re.Request.ContentLength > max {
		return writeError(re, releaseTooLarge(max))
	}
	select {
	case u.slots <- struct{}{}:
		defer func() { <-u.slots }()
	default:
		return rateLimited(re, "The server is busy with other uploads. Try again shortly.")
	}
	if !u.claim(hash) {
		return writeError(re, Conflict(dropapi.CodeUploadInProgress, "An upload to this link is already running."))
	}
	defer u.release(hash)

	file, size, sha, err := spool(re.Request.Body, max)
	if file != nil {
		defer func() {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}()
	}
	if err != nil {
		return writeError(re, err)
	}
	// Imported release metadata is informational. Authorization and storage use
	// the server-computed ELF digest irrespective of any embedded declarations.
	var provenance *releasebundle.Manifest
	if payload, err := releasebundle.Payload(file); err == nil {
		info, err := releasebundle.ReadManifest(payload)
		_ = payload.Close()
		if err == nil {
			provenance = &info
		}
	}
	if err := checkBetaUpload(re.App, app, sha, link.GetBool("activate")); err != nil {
		return writeError(re, err)
	}
	var manifest *dropapi.BetaManifest
	blob, storedSize, storedHash := file, size, sha
	if app.GetBool("developer_private") {
		outcome, raw, err := probeFile(re.Request.Context(), services.ProbeSocket, file, size, sha)
		if err != nil {
			return writeError(re, Conflict("probe_pending", "The private release checker is unavailable. Retry this upload."))
		}
		if outcome == "success" {
			parsed, err := parseBetaManifest(raw)
			if err != nil {
				return writeError(re, err)
			}
			manifest = &parsed
		}
		// Encrypt from the verified bytes, without giving another node a
		// usable copy even if it knows the object key or plaintext digest.
		releaseKey, err := privateblob.Key(app.GetString("private_release_master"), sha)
		if err != nil {
			return writeError(re, err)
		}
		blob, err = os.CreateTemp("", "drop-private-release-*")
		if err != nil {
			return writeError(re, err)
		}
		defer os.Remove(blob.Name())
		defer blob.Close()
		h := sha256.New()
		err = privateblob.Seal(io.MultiWriter(blob, h), io.NewSectionReader(file, 0, size), releaseKey, size)
		if err != nil {
			return writeError(re, err)
		}
		storedSize, storedHash = privateblob.StoredSize(size), hex.EncodeToString(h.Sum(nil))
	}
	key, err := awsx.ReleaseKey(storedHash)
	if err != nil {
		return writeError(re, err)
	}
	ctx, cancel := context.WithTimeout(re.Request.Context(), uploadStoreTimeout)
	defer cancel()
	releaseBlobMu.Lock()
	defer releaseBlobMu.Unlock()
	put := func() error {
		_, err := services.Releases.PutIfAbsent(ctx, key, blob, storedSize, storedHash)
		if err != nil {
			re.App.Logger().Error("Drop release store failed", "key", key, "error", err.Error())
			return &dropapi.Error{Status: http.StatusBadGateway, Code: dropapi.CodeReleaseStoreFailed,
				Message: "The release store did not answer. Nothing changed; the link still works until it expires."}
		}
		return nil
	}
	if !app.GetBool("developer_private") {
		if err := put(); err != nil {
			return writeError(re, err)
		}
	}

	var result dropapi.UploadResult
	err = re.App.RunInTransaction(func(tx core.App) error {
		link, err := tx.FindRecordById("upload_links", link.Id)
		if err != nil {
			return err
		}
		// Valid when the upload started; only a completed upload uses it.
		if !link.GetDateTime("used_at").IsZero() {
			return uploadLinkInvalid()
		}
		app, err := tx.FindRecordById("apps", app.Id)
		if err != nil {
			return err
		}
		freshCreator, err := tx.FindRecordById("users", link.GetString("created_by"))
		if err != nil || !canUploadRelease(tx, app, freshCreator) || freshCreator.GetBool("suspended") || freshCreator.Id != app.GetString("owner") || freshCreator.TokenKey() != creator.TokenKey() {
			return uploadLinkInvalid()
		}
		if app.GetBool("archived") {
			return Conflict(dropapi.CodeAppArchived, "This app is archived. Restore it first.")
		}
		if err := checkBetaUpload(tx, app, sha, link.GetBool("activate")); err != nil {
			return err
		}
		release, created, err := findOrCreateRelease(tx, app, sha, key, size, link.GetString("note"), creator.Id)
		if err != nil {
			return err
		}
		if created && manifest != nil {
			raw, err := json.Marshal(manifest)
			if err != nil {
				return err
			}
			release.Set("private_manifest", raw)
			if err := tx.Save(release); err != nil {
				return err
			}
		}
		if created && provenance != nil {
			release.Set("archive_sha256", provenance.ArchiveSHA256)
			release.Set("executable_sha256", provenance.ExecutableSHA256)
			release.Set("executable_path", provenance.Executable)
			if err := tx.Save(release); err != nil {
				return err
			}
		}
		deployed := false
		if link.GetBool("activate") {
			if err := allowCreditDeployment(tx, app.GetString("owner")); err != nil {
				return err
			}
			if deployed, err = setCurrentRelease(tx, app, release); err != nil {
				return err
			}
			if deployed {
				if err := tx.Save(app); err != nil {
					return err
				}
			}
		}
		if deployed {
			if err := syncDesiredSeqs(tx); err != nil {
				return err
			}
		}
		now, err := types.ParseDateTime(u.now().UTC())
		if err != nil {
			return err
		}
		link.Set("used_at", now)
		link.Set("release", release.Id)
		if err := tx.Save(link); err != nil {
			return err
		}
		if err := WriteEvent(tx, EventRecord{Actor: creator.Id, App: app.Id, Action: "release.upload", Detail: map[string]any{
			"release": release.Id, "sha256": sha, "size_bytes": size, "link": link.Id,
			"created": created, "activate": link.GetBool("activate"), "deployed": deployed,
			"deployment_seq": app.GetInt("deployment_seq"),
		}}); err != nil {
			return err
		}
		if app.GetBool("developer_private") {
			// Revalidate the developer grant before storing encrypted bytes.
			if err := put(); err != nil {
				return err
			}
		}
		result = dropapi.UploadResult{Release: releaseOut(release, app.GetString("current_release")),
			Created: created, Deployed: deployed, DeploymentID: deploymentID(app)}
		return nil
	})
	if err != nil {
		return writeError(re, err)
	}
	return re.JSON(http.StatusOK, result)
}

// findOrCreateRelease returns the app's release of sha, creating it when the
// app has none; created reports which.
func findOrCreateRelease(tx core.App, app *core.Record, sha, key string, size int64, note, creator string) (*core.Record, bool, error) {
	var existing []*core.Record
	if err := tx.RecordQuery("releases").AndWhere(dbx.HashExp{"app": app.Id, "sha256": sha}).Limit(1).All(&existing); err != nil {
		return nil, false, err
	}
	if len(existing) > 0 {
		return existing[0], false, nil
	}
	collection, err := tx.FindCachedCollectionByNameOrId("releases")
	if err != nil {
		return nil, false, err
	}
	release := core.NewRecord(collection)
	release.Set("app", app.Id)
	release.Set("sha256", sha)
	release.Set("size_bytes", size)
	release.Set("s3_key", key)
	release.Set("note", note)
	release.Set("created_by", creator)
	if err := tx.Save(release); err != nil {
		return nil, false, err
	}
	return release, true, nil
}

// spool copies a release body into a private temporary file while hashing
// it, at most max bytes, after checking its ELF header. It returns the open
// file (the caller closes and removes it, also on error), its size and its
// lower-case hex SHA-256.
func spool(body io.Reader, max int64) (*os.File, int64, string, error) {
	header := make([]byte, dropapi.ELFHeaderBytes)
	n, err := io.ReadFull(body, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, 0, "", readFailure(err, max)
	}
	if n == 0 {
		return nil, 0, "", invalidRelease("The upload is empty: send the executable as the request body (curl -fT FILE URL).")
	}
	if problem := dropapi.ReleaseHeaderProblem(header[:n]); problem != "" {
		return nil, 0, "", invalidRelease(problem)
	}
	file, err := os.CreateTemp("", "drop-release-*")
	if err != nil {
		return nil, 0, "", err
	}
	hash := sha256.New()
	out := io.MultiWriter(file, hash)
	if _, err := out.Write(header); err != nil {
		return file, 0, "", err
	}
	copied, err := io.Copy(out, io.LimitReader(body, max-int64(n)+1))
	size := int64(n) + copied
	if err != nil {
		return file, 0, "", readFailure(err, max)
	}
	if size > max {
		return file, 0, "", releaseTooLarge(max)
	}
	if problem := dropapi.ReleaseImageProblem(file, size); problem != "" {
		return file, 0, "", invalidRelease(problem)
	}
	if err := file.Sync(); err != nil {
		return file, 0, "", err
	}
	return file, size, hex.EncodeToString(hash.Sum(nil)), nil
}

// readFailure turns a body read error into an answer: over a body limit is
// 413, anything else (a client that went away) a 400.
func readFailure(err error, max int64) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || errors.Is(err, apis.ErrRequestEntityTooLarge) {
		return releaseTooLarge(max)
	}
	return invalidRelease("The upload did not complete. Try again.")
}

// runUploadLinkPruning deletes unused upload links a day after they expired,
// at start and hourly until ctx ends. Used links stay with their release.
func runUploadLinkPruning(ctx context.Context, app core.App) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := pruneUploadLinks(app, time.Now()); err != nil {
			app.Logger().Error("Upload link pruning failed", "collection", "upload_links")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func pruneUploadLinks(app core.App, now time.Time) error {
	cutoff, err := types.ParseDateTime(now.Add(-24 * time.Hour))
	if err != nil {
		return err
	}
	_, err = app.DB().NewQuery("DELETE FROM upload_links WHERE used_at = '' AND expires_at < {:cutoff}").Bind(dbx.Params{"cutoff": cutoff.String()}).Execute()
	return err
}
