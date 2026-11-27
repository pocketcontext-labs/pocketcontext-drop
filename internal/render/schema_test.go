package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The vendored schema is the pinned NibRunner file, byte for byte. Update
// this digest only together with a deliberate schema bump (docs/render.md).
func TestVendoredSchemaDigest(t *testing.T) {
	sum := sha256.Sum256(DesiredSchema)
	if got := hex.EncodeToString(sum[:]); got != "a0d8ed0a2d50b3fbcbbf66be6f341a09ad6d0dc3bb6293c33e31ce6fb83972b8" {
		t.Fatalf("schema/desired.schema.json sha256 %s", got)
	}
	if _, err := loadSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaAcceptsLiveShapes(t *testing.T) {
	// The emptied host and the golden document both pass.
	if err := ValidateSchema([]byte(`{"hostId":"walter-dev","revision":"empty-1","volumes":[],"instances":[],"checkpoints":[],"exports":[]}`)); err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "crew-dev.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchema(golden); err != nil {
		t.Fatal(err)
	}
}

// The evaluator really enforces the schema: each mutation of the golden
// document breaks one rule nibrunner would refuse.
func TestSchemaRefusesBrokenDocuments(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "crew-dev.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, old, new, want string }{
		{"missing revision", `"revision"`, `"revisionX"`, `missing required "revision"`},
		{"stopped with a firing sleep policy", `"desiredState": "on-request"`, `"desiredState": "stopped"`, "want const"},
		{"activation beside idleTimeoutMs", `"appId": "d-crewdev00000001",
      "config"`, `"appId": "d-crewdev00000001", "idleTimeoutMs": 300000,
      "config"`, "must not"},
		{"bad state", `"desiredState": "on-request"`, `"desiredState": "asleep"`, "oneOf"},
		{"idle timeout too short", `"timeoutMs": 300000`, `"timeoutMs": 1000`, "anyOf"},
		{"dot-dot program", `"program": "/app/crew"`, `"program": "/app/../crew"`, "does not match"},
		{"relative working directory", `"workingDirectory": "/app"`, `"workingDirectory": "app"`, "does not match"},
		{"init as executable", `"destinationPath": "/app/crew"`, `"destinationPath": "/sbin/init"`, "oneOf"},
		{"uppercase digest", `"digest": "1ae3`, `"digest": "1AE3`, "oneOf"},
		{"port zero", `"httpPort": 8090`, `"httpPort": 0`, "below minimum"},
		{"fractional memory", `"memoryMib": 512`, `"memoryMib": 512.5`, "want type"},
		{"bad env name", `"HOME":`, `"HO ME":`, "does not match"},
		{"proto env name", `"HOME":`, `"__proto__":`, "must not"},
		{"bad hostname", `"hostname": "crew-dev.bigconfig.space"`, `"hostname": "Crew_dev"`, "does not match"},
		{"tcp check with an unknown kind", `"kind": "tcp"`, `"kind": "udp"`, "oneOf"},
		{"jwt access without fields", `"kind": "public"`, `"kind": "jwt"`, "oneOf"},
		{"no layers", `"layers": [`, `"layers": [], "x": [`, "fewer than"},
		{"negative size", `"sizeBytes": 4294967296`, `"sizeBytes": -1`, "below minimum"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !bytes.Contains(golden, []byte(c.old)) {
				t.Fatalf("golden has no %q", c.old)
			}
			doc := bytes.Replace(golden, []byte(c.old), []byte(c.new), 1)
			err := ValidateSchema(doc)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestMatchGuestPath(t *testing.T) {
	for _, ok := range []string{"/", "/app", "/app/crew", "/app/data/x.db", "/a b", "/.hidden", "/a..b"} {
		if !matchGuestPath(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "app", "//", "/app/", "/./a", "/a/..", "/a/../b", `/a\b`, `/a"b`, "/a'b", "/a\nb", "/a\x00"} {
		if matchGuestPath(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
