package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/buildinfo"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Releases: `drop release list|upload|deploy`. An upload asks for a one-time
// upload link (release.upload_link) and PUTs the file to its path on the same
// server connection (the private socket of a hosted server, the local server
// or a --url forward), so the token never leaves this process.

// uploadTimeout bounds one release upload.
const uploadTimeout = 10 * time.Minute

func runRelease(c *Client, sub string, a *args) (any, error) {
	switch sub {
	case "list":
		if err := a.need(2, "release list APP [--limit N]"); err != nil {
			return nil, err
		}
		limit, err := a.intFlag("limit", 0)
		if err != nil {
			return nil, err
		}
		return c.Op("release.list", dropapi.ReleaseListInput{App: a.pos[1], Limit: limit}, "")
	case "upload":
		if err := a.need(3, "release upload APP FILE [--activate] [--note TEXT]"); err != nil {
			return nil, err
		}
		return c.UploadRelease(a.pos[1], a.pos[2], a.flags["note"], a.bools["activate"])
	case "deploy":
		if err := a.need(3, "release deploy APP RELEASE_ID"); err != nil {
			return nil, err
		}
		return c.Op("release.deploy", dropapi.ReleaseDeployInput{App: a.pos[1], Release: a.pos[2]}, "")
	}
	return nil, fail(2, "usage: drop release list|upload|deploy ...")
}

// checkReleaseFile opens a release file after checking that it is a regular
// file of 1..max bytes containing a standalone linux/amd64 ELF executable.
func checkReleaseFile(path string, max int64) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fail(2, "%v", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fail(2, "%s is not a regular file", path)
	}
	if info.Size() == 0 || info.Size() > max {
		f.Close()
		return nil, 0, fail(2, "%s is %d bytes; a release is 1 byte to %d MiB", path, info.Size(), max>>20)
	}
	if problem := dropapi.ReleaseImageProblem(f, info.Size()); problem != "" {
		f.Close()
		return nil, 0, fail(2, "%s: %s", path, problem)
	}
	return f, info.Size(), nil
}

// UploadRelease uploads an executable as a new release of app through a
// one-time link and prints the upload's answer.
func (c *Client) UploadRelease(app, path, note string, activate bool) (any, error) {
	f, size, err := checkReleaseFile(path, dropapi.MaxReleaseBytes)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := c.Op("release.upload_link", dropapi.ReleaseUploadLinkInput{App: app, Note: note, Activate: activate}, "")
	if err != nil {
		return nil, err
	}
	var link dropapi.UploadLink
	encoded, _ := json.Marshal(raw)
	if err := json.Unmarshal(encoded, &link); err != nil {
		return nil, fail(1, "invalid upload link answer")
	}
	token, ok := strings.CutPrefix(link.Path, dropapi.UploadRoutePrefix)
	if !ok || !dropapi.UploadTokenPattern.MatchString(token) {
		return nil, fail(1, "invalid upload link answer")
	}
	if size > link.MaxBytes {
		return nil, fail(2, "%s is %d bytes; this server takes releases of at most %d MiB", path, size, link.MaxBytes>>20)
	}
	req, err := http.NewRequest(http.MethodPut, c.Base+link.Path, f)
	if err != nil {
		return nil, fail(1, "invalid upload request URL")
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "Drop/"+buildinfo.Version)
	client := *c.http
	client.Timeout = uploadTimeout
	resp, err := client.Do(req)
	if err != nil {
		return nil, fail(1, "upload connection failed; check the release list before retrying")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fail(1, "incomplete upload response; check the release list before retrying")
	}
	var parsed any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&parsed); err != nil {
		return nil, fail(1, "invalid upload response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		code, _ := dig(parsed, "code").(string)
		exit := 1
		if resp.StatusCode == http.StatusConflict {
			exit = 4
		}
		detail := strings.ReplaceAll(code+": "+message(parsed), token, "[redacted]")
		return nil, fail(exit, "upload: HTTP %d %s", resp.StatusCode, detail)
	}
	return parsed, nil
}
