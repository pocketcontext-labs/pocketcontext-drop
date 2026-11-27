package dropserver

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/privateblob"
	"golang.org/x/sys/unix"
)

const buildCacheKey = "dropInspectedPrivateBuilds"

func init() {
	register("developer.build_upload", privateBuildUpload)
	register("developer.build_import", privateBuildImport)
	register("developer.build_create", privateBuildCreate)
}

// Keep only the latest inspected executable for each developer. Linux unlinks
// the file immediately, so a restart releases all temporary storage.
type inspectedBuild struct {
	file               *os.File
	digest, accountKey string
	size               int64
	manifest           dropapi.BetaManifest
	raw                []byte
	deadline           time.Time
	timer              *time.Timer
}

type privateBuildCache struct {
	mu      sync.Mutex
	entries map[string]*inspectedBuild
}

func (cache *privateBuildCache) put(owner string, build *inspectedBuild) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if old := cache.entries[owner]; old != nil {
		old.timer.Stop()
		old.file.Close()
	} else if len(cache.entries) >= 16 {
		return Conflict("inspection_busy", "Private build storage is busy. Retry shortly.")
	}
	cache.entries[owner] = build
	build.deadline = time.Now().Add(dropapi.UploadLinkTTL)
	build.timer = time.AfterFunc(time.Until(build.deadline), func() {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if cache.entries[owner] == build {
			delete(cache.entries, owner)
			build.file.Close()
		}
	})
	return nil
}

func (cache *privateBuildCache) get(owner, accountKey, digest string) (inspectedBuild, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	build := cache.entries[owner]
	if build == nil || build.digest != digest || build.accountKey != accountKey || !time.Now().Before(build.deadline) {
		return inspectedBuild{}, Conflict("inspection_expired", "Inspect this executable again before creating the app.")
	}
	fd, err := unix.FcntlInt(build.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return inspectedBuild{}, err
	}
	copy := *build
	copy.file = os.NewFile(uintptr(fd), "inspected-private-build")
	return copy, nil
}

func privateBuildUpload(c *Ctx, _ *dropapi.MeInput) (any, error) {
	if servicesOf(c.App).Releases == nil {
		return nil, releasesUnavailable()
	}
	token := security.RandomString(dropapi.UploadTokenLength)
	path := dropapi.UploadRoutePrefix + token
	expires := time.Now().Add(dropapi.UploadLinkTTL)
	err := waitExec(c.App, `INSERT INTO drop_private_build_links(owner,token_hash,account_key,expires_at)
		VALUES({:o},{:t},{:k},{:e}) ON CONFLICT(owner) DO UPDATE SET token_hash=excluded.token_hash,
		account_key=excluded.account_key,expires_at=excluded.expires_at`, dbx.Params{"o": c.Auth.Id, "t": tokenHash(token), "k": tokenHash(c.Auth.TokenKey()), "e": expires.Unix()})
	return dropapi.UploadLink{Path: path, URL: c.publicURL() + path, Method: http.MethodPut, ExpiresAt: expires.UTC().Format(time.RFC3339), MaxBytes: servicesOf(c.App).ReleaseMaxBytes}, err
}

type buildLink struct {
	Owner      string `db:"owner"`
	AccountKey string `db:"account_key"`
}

func checkBuildOwner(app core.App, link buildLink) error {
	account, err := app.FindRecordById("users", link.Owner)
	if errors.Is(err, sql.ErrNoRows) {
		return uploadLinkInvalid()
	}
	if err != nil {
		return err
	}
	if account.GetBool("suspended") || !isDeveloper(account) || tokenHash(account.TokenKey()) != link.AccountKey {
		return uploadLinkInvalid()
	}
	return nil
}

func (u *uploader) privateBuildOrCandidate(re *core.RequestEvent, hash string) error {
	var link buildLink
	err := re.App.DB().NewQuery("SELECT owner,account_key FROM drop_private_build_links WHERE token_hash={:h} AND expires_at>unixepoch()").Bind(dbx.Params{"h": hash}).One(&link)
	if errors.Is(err, sql.ErrNoRows) {
		return u.candidateOrProbe(re, hash)
	}
	if err != nil {
		return writeError(re, err)
	}
	if err := checkBuildOwner(re.App, link); err != nil {
		return writeError(re, err)
	}
	if !u.claim(hash) {
		return writeError(re, Conflict("upload_in_progress", "This executable is already uploading."))
	}
	defer u.release(hash)
	select {
	case u.slots <- struct{}{}:
		defer func() { <-u.slots }()
	default:
		return rateLimited(re, "The release checker is busy.")
	}
	result, err := inspectPrivateBuild(re.Request.Context(), re.App, link, hash, re.Request.Body)
	if err != nil {
		return writeError(re, err)
	}
	return re.JSON(http.StatusOK, result)
}

func privateBuildImport(c *Ctx, in *dropapi.ImportURLInput) (any, error) {
	link := buildLink{Owner: c.Auth.Id, AccountKey: tokenHash(c.Auth.TokenKey())}
	return downloadReleaseURL(c, in, func(data []byte, _ string) (any, error) {
		return inspectPrivateBuild(c.Request.Context(), c.App, link, "", bytes.NewReader(data))
	})
}

func inspectPrivateBuild(ctx context.Context, app core.App, link buildLink, hash string, body io.Reader) (any, error) {
	services := servicesOf(app)
	if services.Releases == nil {
		return nil, releasesUnavailable()
	}
	file, size, digest, err := spool(body, services.ReleaseMaxBytes)
	if file != nil {
		defer func() {
			if file != nil {
				file.Close()
			}
		}()
		if unlinkErr := os.Remove(file.Name()); unlinkErr != nil {
			return nil, unlinkErr
		}
	}
	if err != nil {
		return nil, err
	}
	outcome, raw, err := probeFile(ctx, services.ProbeSocket, file, size, digest)
	if err != nil {
		return nil, Conflict("probe_pending", "The private release checker is unavailable. Retry the inspection.")
	}
	if outcome != "success" {
		return nil, Invalid("manifest", "The executable must implement --drop-manifest.")
	}
	m, minimum, err := workspaceManifest(raw)
	if err != nil {
		return nil, err
	}
	build := &inspectedBuild{file: file, size: size, digest: digest, accountKey: link.AccountKey, manifest: m, raw: raw}
	err = app.RunInTransaction(func(tx core.App) error {
		if err := checkBuildOwner(tx, link); err != nil {
			return err
		}
		if hash != "" {
			res, err := tx.DB().NewQuery("DELETE FROM drop_private_build_links WHERE token_hash={:h} AND expires_at>unixepoch()").Bind(dbx.Params{"h": hash}).Execute()
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				return uploadLinkInvalid()
			}
		}
		if err := WriteEvent(tx, EventRecord{Actor: link.Owner, Action: "developer.build_checked"}); err != nil {
			return err
		}
		cache := app.Store().Get(buildCacheKey).(*privateBuildCache)
		if err := cache.put(link.Owner, build); err != nil {
			return err
		}
		file = nil // cache now owns the descriptor
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"sha256": digest, "product": m.App, "version": m.Version, "minimum_multiplier": minimum}, nil
}

func privateBuildCreate(c *Ctx, in *dropapi.PrivateBuildCreate) (any, error) {
	if !awsx.ValidSHA256(in.SHA256) {
		return nil, Invalid("sha256", "Choose an inspected executable.")
	}
	cache := c.App.Store().Get(buildCacheKey).(*privateBuildCache)
	build, err := cache.get(c.Auth.Id, tokenHash(c.Auth.TokenKey()), in.SHA256)
	if err != nil {
		return nil, err
	}
	defer build.file.Close()
	if in.Multiplier < 1 || in.Multiplier > 8 {
		return nil, Invalid("multiplier", "Choose a multiplier between 1 and 8.")
	}
	m := build.manifest
	kind, features := manifestWorkspaceRuntime(m)
	cpu, mem, disk := (in.Multiplier+3)/4, in.Multiplier*256, in.Multiplier*2048
	raw, err := createApp(c, &dropapi.AppCreateInput{
		Name: in.Name, Slug: "dev-" + core.GenerateDefaultRandomId(), Kind: kind,
		ExecutableName: m.App, HTTPPort: &m.HTTPPort, HookPort: &m.HookPort,
		HealthKind: m.Health, Env: map[string]string{"PORT": strconv.Itoa(m.HTTPPort)},
		VCPU: &cpu, MemoryMiB: &mem, DiskMiB: &disk,
	}, features)
	if err != nil {
		return nil, err
	}
	a, err := c.App.FindRecordById("apps", raw.(dropapi.AppResult).App.ID)
	if err != nil {
		return nil, err
	}
	a.Set("private_tiered", true)
	if err := c.App.Save(a); err != nil {
		return nil, err
	}
	if err := betaPlacement(c, a); err != nil {
		return nil, err
	}
	a, err = c.App.FindRecordById("apps", a.Id)
	if err != nil {
		return nil, err
	}
	blob, storedHash, err := sealPrivateRelease(build.file, build.size, a.GetString("private_release_master"), build.digest)
	if err != nil {
		return nil, err
	}
	defer blob.Close()
	key, err := awsx.ReleaseKey(storedHash)
	if err != nil {
		return nil, err
	}
	r, _, err := findOrCreateRelease(c.App, a, build.digest, key, build.size, m.Version, c.Auth.Id)
	if err != nil {
		return nil, err
	}
	r.Set("private_manifest", build.raw)
	if err := c.App.Save(r); err != nil {
		return nil, err
	}
	if err := checkPrivateManifest(c.App, a, r); err != nil {
		return nil, err
	}
	if _, err := setCurrentRelease(c.App, a, r); err != nil {
		return nil, err
	}
	if err := c.App.Save(a); err != nil {
		return nil, err
	}
	services := servicesOf(c.App)
	if services.Releases == nil {
		return nil, releasesUnavailable()
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), uploadStoreTimeout)
	defer cancel()
	if _, err := services.Releases.PutIfAbsent(ctx, key, blob, privateblob.StoredSize(build.size), storedHash); err != nil {
		return nil, Conflict("store_unavailable", "The release store is unavailable. Retry creating the app.")
	}
	c.Event(Event{App: a.Id, Detail: map[string]any{"release": r.Id, "sha256": build.digest}})
	return dropapi.AppResult{App: c.appOut(a, nil)}, nil
}
