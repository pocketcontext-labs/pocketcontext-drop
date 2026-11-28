package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

var backupIDPattern = regexp.MustCompile("^[a-z0-9]{15}$")

func (a *Agent) backupLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		q, cancel := context.WithTimeout(ctx, PullTimeout)
		var jobs dropapi.BackupJobsResult
		err := a.client.authed(q, http.MethodGet, dropapi.AgentBackupJobsRoute, nil, &jobs)
		cancel()
		if err != nil {
			continue
		}
		for _, j := range jobs.Jobs {
			if ctx.Err() != nil {
				return
			}
			if !backupIDPattern.MatchString(j.ID) || !backupIDPattern.MatchString(j.BackupID) || !appspec.AppIDPattern.MatchString(j.AppID) || !appspec.AppIDPattern.MatchString(j.SourceApp) || !appspec.DeploymentIDOK(j.AppID, j.InstanceID) {
				continue
			}
			q, cancel = context.WithTimeout(ctx, 10*time.Minute)
			a.runBackupJob(q, j)
			cancel()
		}
	}
}

// backupHostname fences the job against the applied deployment. Derive
// the hostname from the applied instance and never include credentials.
func (a *Agent) backupHostname(j dropapi.BackupJob) string {
	var d struct {
		Instances []struct {
			AppID, DeploymentID, DesiredState string
			Hostnames                         []struct{ Hostname string }
		}
	}
	raw, err := os.ReadFile(a.cfg.Desired)
	if err != nil || json.Unmarshal(raw, &d) != nil {
		return ""
	}
	for _, i := range d.Instances {
		if i.AppID != j.AppID || i.DeploymentID != j.InstanceID || (i.DesiredState != "running" && i.DesiredState != "on-request") || len(i.Hostnames) == 0 {
			continue
		}
		host := i.Hostnames[0].Hostname
		if a.cfg.AllowedHostSuffix == "" || !hostPattern.MatchString(host) || !strings.HasSuffix(host, a.cfg.AllowedHostSuffix) {
			return ""
		}
		return host
	}
	return ""
}

// wakeBackupTarget routes a HEAD through the local NibRunner ingress.
func wakeBackupTarget(ctx context.Context, host string) {
	q, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(q, http.MethodHead, "http://127.0.0.1/", nil)
	if err != nil {
		return
	}
	req.Host = host
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	res, err := tr.RoundTrip(req)
	if err == nil {
		res.Body.Close()
	}
}

func (a *Agent) runBackupJob(ctx context.Context, j dropapi.BackupJob) {
	if j.Kind == "capture" && j.AppID != j.SourceApp {
		return
	}
	if j.Kind != "capture" && (j.Kind != "restore" || j.Bytes < 1 || j.Bytes > dropapi.MaxBackupBytes || !appspec.SHA256Pattern.MatchString(j.SHA256)) {
		return
	}
	host := a.backupHostname(j)
	if host == "" {
		return
	}
	a.transferBackup(ctx, j, host, func() (*backupPeer, error) {
		return a.backupPeer(ctx, j.AppID, j.InstanceID)
	})
}

func (a *Agent) transferBackup(ctx context.Context, j dropapi.BackupJob, host string, connect func() (*backupPeer, error)) {
	peer, err := connect()
	if err != nil {
		if errors.Is(err, errBackupGuestAsleep) {
			wakeBackupTarget(ctx, host)
		}
		return
	}
	defer peer.transport.CloseIdleConnections()
	path := dropapi.AgentBackupJobsRoute + "/" + j.ID
	if j.Kind == "capture" {
		a.captureBackup(ctx, peer, j, path)
		return
	}
	res, err := peer.request(ctx, http.MethodGet, "/control/restore-status", "", j.ID, nil)
	if err != nil {
		return
	}
	status := res.StatusCode
	io.Copy(io.Discard, io.LimitReader(res.Body, 8192))
	res.Body.Close()
	switch status {
	case http.StatusOK:
		a.client.authed(ctx, http.MethodPost, path+"/result", dropapi.BackupJobResult{Success: true}, nil)
		return
	case http.StatusNotFound:
	default:
		a.backupGuestError(ctx, path, status)
		return
	}
	req, err := a.client.newRequest(ctx, http.MethodGet, path+"/archive", nil, a.client.currentToken())
	if err != nil {
		return
	}
	archive, err := a.client.http.Do(req)
	if err != nil {
		return
	}
	defer archive.Body.Close()
	if archive.StatusCode != http.StatusOK {
		return
	}
	f, digest, err := a.stageBackup(archive.Body, j.Bytes)
	if errors.Is(err, errArchiveTooLarge) {
		a.backupGuestError(ctx, path, 422)
		return
	}
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || digest != j.SHA256 || info.Size() != j.Bytes {
		a.backupGuestError(ctx, path, 422)
		return
	}
	// Reauthenticate after a potentially long download.
	peer.transport.CloseIdleConnections()
	if a.backupHostname(j) != host {
		return
	}
	target, err := connect()
	if err != nil {
		return
	}
	defer target.transport.CloseIdleConnections()
	applied, err := target.request(ctx, http.MethodPost, "/control/restore", j.SourceApp, j.ID, f)
	if err == nil {
		applied.Body.Close()
		a.backupGuestError(ctx, path, applied.StatusCode)
	}
}

func (a *Agent) captureBackup(ctx context.Context, peer *backupPeer, j dropapi.BackupJob, path string) {
	res, err := peer.request(ctx, http.MethodGet, "/control/backup", "", j.ID, nil)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		a.backupGuestError(ctx, path, res.StatusCode)
		return
	}
	f, _, err := a.stageBackup(res.Body, dropapi.MaxBackupBytes)
	if err != nil {
		if errors.Is(err, errArchiveTooLarge) {
			a.backupGuestError(ctx, path, 413)
		}
		return
	}
	defer f.Close()
	req, err := a.client.newRequest(ctx, http.MethodPut, path+"/archive", nil, a.client.currentToken())
	if err != nil {
		return
	}
	info, err := f.Stat()
	if err != nil {
		return
	}
	req.Body = io.NopCloser(f)
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/zip")
	reply, err := a.client.http.Do(req)
	if err == nil {
		reply.Body.Close()
	}
}

func (a *Agent) backupGuestError(ctx context.Context, path string, status int) {
	switch status {
	case 400, 403, 413, 422:
		code := "guest_rejected"
		if status == http.StatusRequestEntityTooLarge {
			code = "archive_too_large"
		}
		a.client.authed(ctx, http.MethodPost, path+"/result", dropapi.BackupJobResult{Error: code}, nil)
	}
}

var errArchiveTooLarge = errors.New("backup exceeds size limit")

// Unlink immediately so even a killed worker cannot leave customer data behind.
func (a *Agent) stageBackup(r io.Reader, limit int64) (*os.File, string, error) {
	f, err := os.CreateTemp(a.cfg.StateDir, ".backup-*")
	if err != nil {
		return nil, "", err
	}
	if err = os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = errArchiveTooLarge
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, hex.EncodeToString(h.Sum(nil)), nil
}
