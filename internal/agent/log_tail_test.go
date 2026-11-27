package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if got, _ := genericOutput(dir, app, dep); got != "stdout: listening\n" {
		t.Fatal(got)
	}
	if got, _ := genericOutput(dir, "../"+app, dep); got != "" {
		t.Fatal(got)
	}
}

func TestPrivateStartupActionSurvivesLongOutputFromSameDeployment(t *testing.T) {
	dir := t.TempDir()
	app := "d-aaaaaaaaaaaaaaa"
	dep := app + "-dep-3"
	now := time.Now().UTC().Truncate(time.Second)
	key := strings.Repeat("a", 32)
	jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Add(30*time.Minute).Unix()))) + ".signature"
	for _, tc := range []struct{ kind, line, value string }{
		{"pocketbase_installer", "http://untrusted.example/_/#/pbinstall/" + jwt, jwt},
		{"zeroclaw_pairing", "Send: POST /pair with header X-Pairing-Code: " + key, key},
	} {
		var data strings.Builder
		for _, rec := range []struct{ deployment, stream, text string }{
			{dep, "stdout", tc.line}, {dep, "stderr", strings.Repeat("x", 2800)},
			{app + "-dep-2", "stdout", "Send: POST /pair with header X-Pairing-Code: " + strings.Repeat("b", 32)},
			{dep, "network", "Send: POST /pair with header X-Pairing-Code: " + strings.Repeat("c", 32)},
		} {
			err := json.NewEncoder(&data).Encode(map[string]string{"appId": app, "deploymentId": rec.deployment, "kind": "data", "stream": rec.stream, "text": rec.text, "observedAt": now.Format(time.RFC3339)})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, app+".jsonl"), []byte(data.String()), 0600); err != nil {
			t.Fatal(err)
		}
		tail, setup := genericOutput(dir, app, dep)
		if setup == nil || setup.Kind != tc.kind || setup.Value != tc.value || !setup.Available(now) || strings.Contains(tail, tc.value) {
			t.Fatal("unexpected private output or setup action")
		}
	}
}
