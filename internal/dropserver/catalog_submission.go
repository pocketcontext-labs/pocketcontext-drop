package dropserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"regexp"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

var githubCommit = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}/commit/[0-9a-f]{40}$`)

func init() {
	register("developer.submit", submitCommit)
	register("catalog.submissions", listCatalogSubmissions)
}

func recordCommit(c *Ctx, url string) error {
	if !githubCommit.MatchString(url) || strings.Contains(url, "/./") || strings.Contains(url, "/../") {
		return Invalid("commit_url", "Supply the full HTTPS GitHub commit URL with a 40-character lowercase SHA.")
	}
	return waitExec(c.App, "INSERT INTO drop_source_submissions(commit_url,submitted_by) VALUES({:u},{:a}) ON CONFLICT DO NOTHING", dbx.Params{"u": url, "a": c.Auth.Id})
}

func submitCommit(c *Ctx, in *dropapi.CommitSubmission) (any, error) {
	if err := recordCommit(c, in.CommitURL); err != nil {
		return nil, err
	}
	c.Event(Event{Detail: map[string]any{"commit_url": in.CommitURL}})
	return map[string]string{"commit_url": in.CommitURL}, nil
}

func listCatalogSubmissions(c *Ctx, _ *dropapi.MeInput) (any, error) {
	rows := []struct {
		CommitURL   string `db:"commit_url" json:"commit_url"`
		SubmittedBy string `db:"submitted_by" json:"submitted_by"`
	}{}
	err := c.App.DB().NewQuery("SELECT commit_url,submitted_by FROM drop_source_submissions s WHERE NOT EXISTS(SELECT 1 FROM drop_official_releases r WHERE r.commit_url=s.commit_url) ORDER BY submitted_at DESC").All(&rows)
	return map[string]any{"submissions": rows}, err
}

func storeCatalogBlob(ctx context.Context, store awsx.BlobStore, file *os.File, digest string, size int64) error {
	publicKey, err := awsx.ReleaseKey(digest)
	if err != nil {
		return err
	}
	created, err := store.PutIfAbsent(ctx, publicKey, file, size, digest)
	if err != nil {
		return Conflict("store_unavailable", "The catalog release could not be stored. Retry the upload.")
	}
	if !created {
		// An existing key skips PutIfAbsent's byte checks. Verify its contents
		// before linking it to an officially reviewed catalog entry.
		existing, info, err := awsx.OpenRelease(ctx, store, publicKey)
		if err != nil {
			return Conflict("store_unavailable", "The existing catalog executable could not be read. Retry the upload.")
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
