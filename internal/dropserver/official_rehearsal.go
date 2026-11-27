package dropserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func init() { register("catalog.rehearse", officialRehearse) }
func officialRehearse(c *Ctx, in *dropapi.OfficialRehearsal) (any, error) {
	u, err := url.Parse(in.EvidenceURL)
	if err != nil || len(in.EvidenceURL) > 1024 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, Invalid("evidence_url", "Supply a permanent HTTPS evidence URL without credentials, query or fragment.")
	}
	if !betaDigestPattern.MatchString(in.SHA256) || !betaDigestPattern.MatchString(in.EvidenceSHA256) || !betaDigestPattern.MatchString(in.BeforeRecords) || !betaDigestPattern.MatchString(in.BeforeFiles) || in.Records < 1 || in.Attachments < 1 || in.BeforeRecords != in.AfterRecords || in.BeforeFiles != in.AfterFiles {
		return nil, Invalid("evidence", "Supply matching before and after digests for records and attachments from a completed rehearsal.")
	}
	var product string
	if err := c.App.DB().NewQuery("SELECT product FROM drop_official_releases WHERE digest={:d} AND published=0 AND archived=0").Bind(dbx.Params{"d": in.SHA256}).Row(&product); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFound("Candidate")
		}
		return nil, err
	}
	if in.Baseline != "" {
		var exists bool
		if err := c.App.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_official_releases WHERE digest={:d} AND product={:p} AND published>0)").Bind(dbx.Params{"d": in.Baseline, "p": product}).Row(&exists); err != nil {
			return nil, err
		}
		if !exists || in.Baseline == in.SHA256 {
			return nil, Invalid("baseline_sha256", "Select a previously published release of this product.")
		}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if err := waitExec(c.App, `INSERT INTO drop_official_rehearsals(candidate,baseline,evidence,receipt,actor) VALUES({:d},{:b},{:e},{:r},{:a}) ON CONFLICT DO NOTHING`, dbx.Params{"d": in.SHA256, "b": in.Baseline, "e": in.EvidenceSHA256, "r": string(raw), "a": c.Auth.Id}); err != nil {
		return nil, err
	}
	c.Event(Event{Detail: map[string]any{"candidate": in.SHA256, "baseline": in.Baseline, "evidence_sha256": in.EvidenceSHA256}})
	return map[string]bool{"recorded": true}, nil
}

func requireRehearsal(app core.App, candidate, baseline string) error {
	var ok bool
	if err := app.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_official_rehearsals WHERE candidate={:d} AND baseline={:b})").Bind(dbx.Params{"d": candidate, "b": baseline}).Row(&ok); err != nil {
		return err
	}
	if !ok {
		return Conflict("rehearsal_required", "Record a successful rehearsal for this exact candidate and installed baseline before proceeding.")
	}
	return nil
}
