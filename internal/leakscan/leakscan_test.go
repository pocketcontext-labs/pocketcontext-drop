package leakscan

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFindsEveryForm(t *testing.T) {
	dir := t.TempDir()
	secret := "synthetic-sentinel-0001"
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"clean.log":           "nothing to see",
		"nested/state.json":   `{"cache":"` + base64.StdEncoding.EncodeToString([]byte(secret)) + `"}`,
		"nested/reported.txt": "prefix " + secret + " suffix",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sentinels := []Sentinel{{"secret", secret}, {"other", "never-present-value"}}
	findings, err := Scan(sentinels, Path(dir), Text("answer", "ok"), Text("mcp", "x"+secret))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range findings {
		if strings.Contains(f.String(), secret) {
			t.Fatal("a finding prints the value")
		}
		got[filepath.Base(f.Source)+"/"+f.Label] = true
	}
	if len(findings) != 3 || !got["state.json/secret"] || !got["reported.txt/secret"] || !got["mcp/secret"] {
		t.Fatalf("findings %v", findings)
	}
	if _, err := Scan(sentinels, Path(filepath.Join(dir, "missing"))); err == nil {
		t.Fatal("a missing path scanned clean")
	}
	if _, err := Scan([]Sentinel{{"short", "abc"}}); err == nil {
		t.Fatal("a short sentinel was accepted")
	}
}
