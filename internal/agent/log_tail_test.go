package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericLogTailMatchesAppAndDeployment(t *testing.T) {
	dir := t.TempDir()
	app := "d-aaaaaaaaaaaaaaa"
	dep := app + "-dep-1"
	lines := []map[string]string{
		{"appId": app, "deploymentId": dep, "kind": "data", "stream": "stdout", "text": "listening"},
		{"appId": "d-bbbbbbbbbbbbbbb", "deploymentId": dep, "kind": "data", "stream": "stdout", "text": "foreign"},
		{"appId": app, "deploymentId": app + "-dep-2", "kind": "data", "stream": "stderr", "text": "other deployment"},
		{"appId": app, "deploymentId": dep, "kind": "cron-data", "stream": "stdout", "text": "cron"},
	}
	var data strings.Builder
	for _, line := range lines {
		if err := json.NewEncoder(&data).Encode(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, app+".jsonl"), []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if got := genericLogTail(dir, app, dep); got != "stdout: listening\n" {
		t.Fatal(got)
	}
	if got := genericLogTail(dir, "../"+app, dep); got != "" {
		t.Fatal(got)
	}
}
