package dropserver

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerNeverReadsSecrets: the server's code holds the secret store only
// as an awsx.SecretWriter. No file of this package names the reading side or
// opens stores itself (serve opens them and hands over the writer).
func TestServerNeverReadsSecrets(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), file, source, parser.SkipObjectResolution); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"awsx.SecretReader", "awsx.SecretStore", "awsx.Stores", "awsx.OpenAWS", "awsx.OpenFakeDir", "awsx.NewMemSecretStore", "awsx.NewSSMSecretStore", ".Get(ctx"} {
			if strings.Contains(string(source), forbidden) {
				t.Errorf("%s names %s", file, forbidden)
			}
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("checked only %d files", checked)
	}
}
