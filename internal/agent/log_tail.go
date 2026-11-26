package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

// Generic apps receive no platform credentials. Send only stdout/stderr from
// this app's current deployment, excluding cron, request and host logs.
func genericLogTail(dir, app, deployment string) string {
	if !appspec.AppIDPattern.MatchString(app) || !appspec.DeploymentIDOK(app, deployment) {
		return ""
	}
	file, err := os.OpenFile(filepath.Join(dir, app+".jsonl"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	start := max(int64(0), info.Size()-16384)
	data, err := io.ReadAll(io.NewSectionReader(file, start, info.Size()-start))
	if err != nil {
		return ""
	}
	var tail strings.Builder
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var entry struct{ AppID, DeploymentID, Kind, Stream, Text string }
		if json.Unmarshal(line, &entry) != nil || entry.AppID != app || entry.DeploymentID != deployment || entry.Kind != "data" || (entry.Stream != "stdout" && entry.Stream != "stderr") {
			continue
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
	return result
}
