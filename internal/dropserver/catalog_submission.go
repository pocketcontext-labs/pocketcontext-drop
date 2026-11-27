package dropserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/privateblob"
)

func init() {
	register("developer.submit", submitPrivateRelease)
	register("catalog.submissions", listCatalogSubmissions)
	register("catalog.accept", acceptCatalogSubmission)
}

// The only private app metadata an operator receives through the review queue
// belongs to an immutable release that the owner explicitly submitted.
type catalogSubmission struct {
	Accepted    bool   `db:"accepted" json:"-"`
	App         string `db:"app" json:"app"`
	Release     string `db:"release" json:"release"`
	SubmittedBy string `db:"submitted_by" json:"submitted_by"`
	Product     string `db:"product" json:"product"`
	Version     string `db:"version" json:"version"`
	Digest      string `db:"digest" json:"sha256"`
	Size        int64  `db:"size" json:"size_bytes"`
}

const submissionQuery = `SELECT a.id app,s.release,s.submitted_by,s.product,s.version,s.digest,r.size_bytes size,s.accepted
	FROM drop_catalog_submissions s JOIN releases r ON r.id=s.release AND r.sha256=s.digest
	JOIN apps a ON a.id=r.app AND a.owner=s.submitted_by AND a.developer_private=1 AND a.deleting=0`

func submitPrivateRelease(c *Ctx, in *dropapi.ReleaseDeployInput) (any, error) {
	a, err := c.findApp(in.App, true)
	if err != nil {
		return nil, err
	}
	if a.GetString("owner") != c.Auth.Id || !a.GetBool("developer_private") {
		return nil, NotFound("App")
	}
	r, err := c.App.FindRecordById("releases", in.Release)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.GetString("app") != a.Id) {
		return nil, NotFound("Release")
	}
	if err != nil {
		return nil, err
	}
	if a.GetBool("archived") || a.GetString("current_release") != r.Id {
		return nil, Conflict("release_not_deployed", "Submit the private app's currently deployed release.")
	}
	m, _, err := workspaceManifest([]byte(r.GetString("private_manifest")))
	if err != nil {
		return nil, err
	}
	digest := r.GetString("sha256")
	if err := betaApproved(c.App, digest); err != nil {
		return nil, err
	}
	s := catalogSubmission{App: a.Id, Release: r.Id, SubmittedBy: c.Auth.Id, Product: m.App, Version: m.Version, Digest: digest, Size: int64(r.GetInt("size_bytes"))}
	err = waitExec(c.App, `INSERT INTO drop_catalog_submissions(release,submitted_by,product,version,digest)
		VALUES({:r},{:u},{:p},{:v},{:d}) ON CONFLICT(release) DO NOTHING`, dbx.Params{"r": s.Release, "u": s.SubmittedBy, "p": s.Product, "v": s.Version, "d": s.Digest})
	c.Event(Event{App: a.Id, Detail: map[string]any{"release": r.Id, "sha256": s.Digest}})
	return s, err
}

func listCatalogSubmissions(c *Ctx, _ *dropapi.MeInput) (any, error) {
	rows := []catalogSubmission{}
	err := c.App.DB().NewQuery(submissionQuery + " WHERE s.accepted=0 ORDER BY s.submitted_at,s.release").All(&rows)
	return map[string]any{"submissions": rows}, err
}

func submittedCandidate(app core.App, in *dropapi.AcceptSubmission) (catalogSubmission, error) {
	var s catalogSubmission
	err := app.DB().NewQuery(submissionQuery + " WHERE a.id={:a} AND s.release={:r} AND s.digest={:d}").Bind(dbx.Params{"a": in.App, "r": in.Release, "d": in.SHA256}).One(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return s, NotFound("Submitted candidate")
	}
	if err != nil {
		return s, err
	}
	return s, nil
}

// Approval is linearized before exposing any plaintext in the shared store.
// A database write transaction is never held during a release download or upload.
func prepareCatalogAcceptance(re *core.RequestEvent, spec dropapi.Spec, in *dropapi.AcceptSubmission) error {
	s, err := submittedCandidate(re.App, in)
	if err != nil || s.Accepted {
		return err
	}
	a, err := re.App.FindRecordById("apps", s.App)
	if err != nil {
		return err
	}
	r, err := re.App.FindRecordById("releases", s.Release)
	if err != nil {
		return err
	}
	services := servicesOf(re.App)
	store, ok := services.Releases.(awsx.BlobStore)
	if !ok {
		return releasesUnavailable()
	}
	if s.Size <= 0 || s.Size > services.ReleaseMaxBytes {
		return releaseTooLarge(services.ReleaseMaxBytes)
	}
	key, err := privateblob.Key(a.GetString("private_release_master"), s.Digest)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(re.Request.Context(), uploadStoreTimeout)
	defer cancel()
	file, err := verifiedPrivateBlob(ctx, store, r.GetString("s3_key"), key, s.Digest, s.Size)
	if err != nil {
		return err
	}
	defer file.Close()
	err = re.App.RunInTransaction(func(tx core.App) error {
		c, err := authorizeOp(tx, re, spec)
		if err != nil {
			return err
		}
		fresh, err := submittedCandidate(tx, in)
		if err != nil {
			return err
		}
		if fresh != s {
			return Conflict("candidate_changed", "The submitted candidate has changed.")
		}
		m, _, err := workspaceManifest([]byte(r.GetString("private_manifest")))
		if err != nil {
			return err
		}
		if m.App != s.Product || m.Version != s.Version {
			return Conflict("candidate_changed", "The candidate manifest has changed.")
		}
		if err = checkOfficialCandidate(tx, m, s.Digest); err != nil {
			return err
		}
		return waitExec(tx, `UPDATE drop_catalog_submissions SET disclosure_approved_by={:actor}
			WHERE release={:release} AND disclosure_approved_by=''`, dbx.Params{"actor": c.Auth.Id, "release": s.Release})
	})
	if err != nil {
		return err
	}
	return storeCatalogBlob(ctx, store, file, s.Digest, s.Size)
}

func acceptCatalogSubmission(c *Ctx, in *dropapi.AcceptSubmission) (any, error) {
	s, err := submittedCandidate(c.App, in)
	if err != nil {
		return nil, err
	}
	if s.Accepted {
		c.Event(Event{Detail: map[string]any{"release": s.Release, "sha256": s.Digest}})
		return s, nil
	}
	r, err := c.App.FindRecordById("releases", s.Release)
	if err != nil {
		return nil, err
	}
	raw := []byte(r.GetString("private_manifest"))
	m, minimum, err := workspaceManifest(raw)
	if err != nil {
		return nil, err
	}
	if m.App != s.Product || m.Version != s.Version {
		return nil, Conflict("candidate_changed", "The candidate manifest has changed.")
	}
	if err = stageOfficial(c.App, m, raw, minimum, s.Digest, s.Size); err != nil {
		return nil, err
	}
	err = waitExec(c.App, "UPDATE drop_catalog_submissions SET accepted=1 WHERE release={:r}", dbx.Params{"r": s.Release})
	c.Event(Event{Detail: map[string]any{"release": s.Release, "submitted_by": s.SubmittedBy, "sha256": s.Digest, "product": s.Product, "version": s.Version}})
	return s, err
}

func verifiedPrivateBlob(ctx context.Context, store awsx.BlobStore, source, key, digest string, size int64) (*os.File, error) {
	body, _, err := awsx.OpenRelease(ctx, store, source)
	if err != nil {
		return nil, Conflict("store_unavailable", "The submitted release could not be read. Retry acceptance.")
	}
	defer body.Close()
	file, err := os.CreateTemp("", "drop-catalog-release-*")
	if err != nil {
		return nil, err
	}

	// Linux keeps an unlinked file available through its descriptor. Do this
	// before decryption so even a process exit leaves no private ELF on disk.
	if err = os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, err
	}
	hash := sha256.New()
	if err = privateblob.Open(io.MultiWriter(file, hash), body, key, size); err != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
		file.Close()
		return nil, Conflict("candidate_corrupt", "The private executable could not be verified against its submitted digest.")
	}
	return file, nil
}

func storeCatalogBlob(ctx context.Context, store awsx.BlobStore, file *os.File, digest string, size int64) error {
	publicKey, err := awsx.ReleaseKey(digest)
	if err != nil {
		return err
	}
	created, err := store.PutIfAbsent(ctx, publicKey, file, size, digest)
	if err != nil {
		return Conflict("store_unavailable", "The catalog release could not be stored. Retry acceptance.")
	}
	if !created {
		// An existing key skips PutIfAbsent's byte checks. Verify its contents
		// before linking it to an officially reviewed catalog entry.
		existing, info, err := awsx.OpenRelease(ctx, store, publicKey)
		if err != nil {
			return Conflict("store_unavailable", "The existing catalog executable could not be read. Retry acceptance.")
		}
		defer existing.Close()
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(existing, size+1))
		if err != nil || info.Size != size || n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
			return Conflict("candidate_corrupt", "The catalog executable does not match the submitted digest.")
		}
	}
	return nil
}
