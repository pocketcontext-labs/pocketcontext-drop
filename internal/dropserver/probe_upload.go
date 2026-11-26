package dropserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

type manifestUpload struct {
	Owner  string `db:"owner"`
	Digest string `db:"digest"`
}

func betaProbeUpload(c *Ctx, in *dropapi.BetaDigest) (any, error) {
	if err := betaApproved(c.App, in.SHA256); err != nil {
		return nil, err
	}
	token := security.RandomString(dropapi.UploadTokenLength)
	err := waitExec(c.App, `INSERT INTO drop_manifest_uploads(token_hash,owner,digest,expires_at)
      VALUES({:hash},{:owner},{:digest},{:expires}) ON CONFLICT(owner) DO UPDATE SET
      token_hash=excluded.token_hash,digest=excluded.digest,expires_at=excluded.expires_at`, dbx.Params{"hash": tokenHash(token), "owner": c.Auth.Id, "digest": in.SHA256, "expires": time.Now().Add(dropapi.UploadLinkTTL).Unix()})
	c.Event(Event{Detail: map[string]any{"sha256": in.SHA256}})
	return map[string]string{"path": dropapi.UploadRoutePrefix + token}, err
}

func manifestUploadOwner(app core.App, hash string) (manifestUpload, error) {
	var row manifestUpload
	err := app.DB().NewQuery(`SELECT u.owner,u.digest FROM drop_manifest_uploads u JOIN users a ON a.id=u.owner
    WHERE u.token_hash={:hash} AND u.expires_at>unixepoch() AND a.suspended=0 AND a.role='user'`).Bind(dbx.Params{"hash": hash}).One(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return row, uploadLinkInvalid()
	}
	return row, err
}

// probeFile sends validated ELF bytes to the local inspection service. Only the
// NibRunner guest executes the file. A broken socket or service never means a
// completed negative probe.
func probeFile(ctx context.Context, socket string, file *os.File, size int64, digest string) (string, json.RawMessage, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return "", nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(90 * time.Second))
	if _, err = fmt.Fprintf(conn, "%s %d\n", digest, size); err != nil {
		return "", nil, err
	}
	if _, err = io.CopyN(conn, io.NewSectionReader(file, 0, size), size); err != nil {
		return "", nil, err
	}
	var reply struct {
		Outcome  string
		Manifest json.RawMessage
	}
	raw, err := io.ReadAll(io.LimitReader(conn, maxBetaManifest+1025))
	if err != nil || len(raw) > maxBetaManifest+1024 || json.Unmarshal(raw, &reply) != nil || (reply.Outcome != "success" && reply.Outcome != "invalid") {
		return "", nil, errors.New("inspection service unavailable")
	}
	return reply.Outcome, reply.Manifest, nil
}

func (u *uploader) inspect(re *core.RequestEvent, hash string) error {
	row, err := manifestUploadOwner(re.App, hash)
	if err != nil {
		return writeError(re, err)
	}
	select {
	case u.slots <- struct{}{}:
		defer func() { <-u.slots }()
	default:
		return rateLimited(re, "The inspection service is busy.")
	}
	if !u.claim(hash) {
		return writeError(re, Conflict("probe_busy", "Inspection already started."))
	}
	defer u.release(hash)
	max := servicesOf(re.App).ReleaseMaxBytes
	if re.Request.ContentLength > max {
		return writeError(re, releaseTooLarge(max))
	}
	file, size, digest, err := spool(re.Request.Body, max)
	if file != nil {
		defer os.Remove(file.Name())
		defer file.Close()
	}
	if err != nil {
		return writeError(re, err)
	}
	if digest != row.Digest {
		return writeError(re, Invalid("sha256", "The uploaded file differs from the selected file."))
	}
	var attempt string
	err = re.App.RunInTransaction(func(tx core.App) error {
		if _, err := manifestUploadOwner(tx, hash); err != nil {
			return err
		}
		value, err := betaClaim(&Ctx{App: tx}, &dropapi.BetaDigest{SHA256: digest})
		if err != nil {
			return err
		}
		attempt, _ = value.(map[string]any)["attempt"].(string)
		return nil
	})
	if err != nil {
		return writeError(re, err)
	}
	if attempt != "" {
		outcome, manifest, probeErr := probeFile(re.Request.Context(), servicesOf(re.App).ProbeSocket, file, size, digest)
		if probeErr != nil {
			outcome = "retry"
		}
		err = re.App.RunInTransaction(func(tx core.App) error {
			if _, err := manifestUploadOwner(tx, hash); err != nil {
				return err
			}
			_, err := betaComplete(&Ctx{App: tx}, &dropapi.BetaCompleteInput{SHA256: digest, Attempt: attempt, Outcome: outcome, Manifest: manifest})
			return err
		})
		if err != nil {
			return writeError(re, err)
		}
		if probeErr != nil {
			return writeError(re, Conflict("probe_pending", "The inspection service is unavailable. Try again shortly."))
		}
	}
	if err = waitExec(re.App, "DELETE FROM drop_manifest_uploads WHERE token_hash={:hash}", dbx.Params{"hash": hash}); err != nil {
		return writeError(re, err)
	}
	return re.JSON(http.StatusOK, map[string]bool{"inspected": true})
}
