package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestWorkerKeepsGuestAndHostFailuresDistinct(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "nibrunnerd")
	t.Setenv("PATH", dir)
	data := []byte("synthetic inspection payload")
	request := fmt.Sprintf("%x %d\n%s", sha256.Sum256(data), len(data), data)
	for _, example := range []struct{ command, want string }{
		{"printf '%s' '{\"schema\":\"drop.manifest/v1\"}'", "success"},
		{"exit 3", "invalid"},
		{"exit 1", ""},
	} {
		// The adapter may invoke only NibRunner's isolated command. A storage
		// or guest-boot failure must remain retriable on the control plane.
		script := "#!/bin/sh\n[ \"$1\" = probe-manifest ] && [ \"$2\" = --config ] && [ \"$4\" = --binary ] && [ -f \"$5\" ] && [ \"$6\" = --sha256 ] || exit 1\n" + example.command + "\n"
		if err := os.WriteFile(program, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := ManifestProbe(bytes.NewBufferString(request), &out)
		if example.want == "" {
			if err == nil || out.Len() != 0 {
				t.Fatal("host failure yielded a cacheable receipt")
			}
			continue
		}
		var reply struct {
			Outcome  string
			Manifest json.RawMessage
		}
		if err != nil || json.Unmarshal(out.Bytes(), &reply) != nil || reply.Outcome != example.want || (len(reply.Manifest) > 0) != (example.want == "success") {
			t.Fatal(out.String(), err)
		}
	}
	for _, invalid := range []string{"no header", fmt.Sprintf("%x %d\n", sha256.Sum256(data), 100<<20)} {
		var out bytes.Buffer
		if ManifestProbe(bytes.NewBufferString(invalid), &out) == nil || out.Len() > 0 {
			t.Fatal("invalid input produced a receipt")
		}
	}
}
