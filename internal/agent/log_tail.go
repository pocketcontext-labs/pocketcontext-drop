package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Generic apps receive no platform credentials. Send only stdout/stderr from
// this app's current deployment, excluding cron, request and host logs.
func genericOutput(dir, app, deployment string) (string, *dropapi.FirstRun) {
	if !appspec.AppIDPattern.MatchString(app) || !appspec.DeploymentIDOK(app, deployment) {
		return "", nil
	}
	file, err := os.OpenFile(filepath.Join(dir, app+".jsonl"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", nil
	}
	start := max(int64(0), info.Size()-16384)
	data, err := io.ReadAll(io.NewSectionReader(file, start, info.Size()-start))
	if err != nil {
		return "", nil
	}
	var tail strings.Builder
	var setup *dropapi.FirstRun
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var entry struct{ AppID, DeploymentID, Kind, Stream, Text, ObservedAt string }
		if json.Unmarshal(line, &entry) != nil || entry.AppID != app || entry.DeploymentID != deployment {
			continue
		}
		if entry.Kind == "restart" {
			setup = nil
			continue
		}
		if entry.Kind != "data" || (entry.Stream != "stdout" && entry.Stream != "stderr") {
			continue
		}
		if action := parseStartupOutput(entry.Text, entry.ObservedAt); action != nil {
			setup = action
		}
		tail.WriteString(entry.Stream + ": ")
		tail.WriteString(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return -1
			}
			return r
		}, entry.Text))
		tail.WriteByte('\n')
	}
	result := tail.String()
	if len(result) > 1024 {
		result = result[len(result)-1024:]
		for !utf8.ValidString(result) {
			result = result[1:]
		}
	}
	return result, setup
}

var pbInstaller = regexp.MustCompile(`/_/#/pbinstall/([A-Za-z0-9_-]+\.([A-Za-z0-9_-]+)\.[A-Za-z0-9_-]+)(?:[[:space:]]|$)`)
var zcPair = regexp.MustCompile(`POST /pair with header X-Pairing-Code: ([A-Za-z0-9]{32})(?:[[:space:]]|$)`)

// Read only the token or code from the guest's output. In particular, never
// follow or return a hostname printed by an executable.
func parseStartupOutput(text, observedAt string) *dropapi.FirstRun {
	observed, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return nil
	}
	if match := pbInstaller.FindStringSubmatch(text); match != nil && len(match[1]) <= 768 {
		// The expiration is advisory; PocketBase validates the token when used.
		payload, err := base64.RawURLEncoding.DecodeString(match[2])
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if err == nil && json.Unmarshal(payload, &claims) == nil {
			until := time.Unix(claims.Exp, 0).UTC()
			if until.After(observed) && !until.After(observed.Add(31*time.Minute)) {
				return &dropapi.FirstRun{Kind: "pocketbase_installer", Value: match[1], ExpiresAt: until.Format(time.RFC3339)}
			}
		}
	}
	if match := zcPair.FindStringSubmatch(text); match != nil {
		return &dropapi.FirstRun{Kind: "zeroclaw_pairing", Value: match[1], ExpiresAt: observed.Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	}
	return nil
}
