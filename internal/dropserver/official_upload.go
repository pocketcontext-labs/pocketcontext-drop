package dropserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"slices"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
)

type candidateLink struct {
	Owner   string `db:"owner"`
	Product string `db:"product"`
	Version string `db:"version"`
}

func loadCandidateLink(app core.App, hash string) (candidateLink, error) {
	var link candidateLink
	err := app.DB().NewQuery(`SELECT l.owner,l.product,l.version FROM drop_official_uploads l JOIN users u ON u.id=l.owner
	WHERE token_hash={:hash} AND expires_at>unixepoch() AND u.suspended=0 AND u.role='admin'`).Bind(dbx.Params{"hash": hash}).One(&link)
	return link, err
}

func (u *uploader) candidateOrProbe(re *core.RequestEvent, hash string) error {
	link, err := loadCandidateLink(re.App, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return u.inspect(re, hash)
	}
	if err != nil {
		return writeError(re, err)
	}
	if !u.claim(hash) {
		return writeError(re, Conflict("upload_in_progress", "The candidate is already uploading."))
	}
	defer u.release(hash)
	select {
	case u.slots <- struct{}{}:
		defer func() { <-u.slots }()
	default:
		return rateLimited(re, "The release checker is busy.")
	}
	services := servicesOf(re.App)
	if services.Releases == nil {
		return writeError(re, releasesUnavailable())
	}
	if re.Request.ContentLength > services.ReleaseMaxBytes {
		return writeError(re, releaseTooLarge(services.ReleaseMaxBytes))
	}
	file, size, digest, err := spool(re.Request.Body, services.ReleaseMaxBytes)
	if file != nil {
		defer os.Remove(file.Name())
		defer file.Close()
	}
	if err != nil {
		return writeError(re, err)
	}
	outcome, raw, err := probeFile(re.Request.Context(), services.ProbeSocket, file, size, digest)
	if err != nil {
		return writeError(re, Conflict("probe_pending", "The release checker is unavailable. Retry this upload."))
	}
	m, err := parseBetaManifest(raw)
	if outcome != "success" || err != nil || m.App != link.Product || m.Version != link.Version || m.HookPort == 0 || !slices.Contains(m.Optional, "s3_replica") || !slices.Contains(m.Requires, "persistent_volume") || !slices.Contains(m.Requires, "sign_in_email") || !slices.Contains(m.Requires, "verified_owner_email") {
		return writeError(re, Invalid("manifest", "The executable must declare this product, version, lifecycle hooks, owner sign-in, persistent volume and optional backup storage."))
	}
	minimum := max(m.Minimum.CPUQuarters, (m.Minimum.MemoryMiB+255)/256)
	if minimum > 8 || m.Minimum.DiskMiB > 10240 {
		return writeError(re, Invalid("minimum", "The manifest exceeds the workspace resource allowance."))
	}
	key, err := awsx.ReleaseKey(digest)
	if err != nil {
		return writeError(re, err)
	}
	releaseBlobMu.Lock()
	defer releaseBlobMu.Unlock()
	err = re.App.RunInTransaction(func(tx core.App) error {
		fresh, err := loadCandidateLink(tx, hash)
		if err != nil || fresh != link {
			return uploadLinkInvalid()
		}
		if err := betaApproved(tx, digest); err != nil {
			return err
		}
		var collision bool
		p := dbx.Params{"product": link.Product, "version": link.Version, "digest": digest, "size": size, "minimum": minimum}
		if err := tx.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_official_releases WHERE product={:product} AND version={:version} AND digest!={:digest})").Bind(p).Row(&collision); err != nil {
			return err
		}
		if collision {
			return Conflict("version_exists", "This version already identifies another executable.")
		}
		ctx, cancel := context.WithTimeout(re.Request.Context(), uploadStoreTimeout)
		defer cancel()
		if _, err = services.Releases.PutIfAbsent(ctx, key, file, size, digest); err != nil {
			return Conflict("store_unavailable", "The release store is unavailable. Retry this upload.")
		}
		if err := waitExec(tx, "INSERT INTO drop_official_releases(digest,product,version,size,min_size) VALUES({:digest},{:product},{:version},{:size},{:minimum}) ON CONFLICT(digest) DO NOTHING", p); err != nil {
			return err
		}
		if err := waitExec(tx, "INSERT INTO drop_beta_catalog(digest,approved) VALUES({:digest},1) ON CONFLICT DO NOTHING", p); err != nil {
			return err
		}
		if err := waitExec(tx, `INSERT INTO drop_beta_probes(digest,contract,state,manifest) VALUES({:d},{:v},'valid',{:m})
		ON CONFLICT(digest,contract) DO UPDATE SET state='valid',manifest=excluded.manifest,attempt='',expires_at=0`, dbx.Params{"d": digest, "v": betaProbeContract, "m": string(raw)}); err != nil {
			return err
		}
		if err := waitExec(tx, "DELETE FROM drop_official_uploads WHERE token_hash={:hash}", dbx.Params{"hash": hash}); err != nil {
			return err
		}
		return WriteEvent(tx, EventRecord{Actor: link.Owner, Action: "catalog.checked", Detail: map[string]any{"product": m.App, "version": m.Version, "sha256": digest}})
	})
	if err != nil {
		return writeError(re, err)
	}
	return re.JSON(http.StatusOK, map[string]any{"sha256": digest, "product": m.App, "version": m.Version, "size_bytes": size})
}
