package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestUploadErrorsKeepLinkPrivate(t *testing.T) {
	token := strings.Repeat("SyntheticToken12", 2)
	path := filepath.Join(t.TempDir(), "hello")
	if err := os.WriteFile(path, droptest.ELF(256, 0), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		reply http.HandlerFunc
	}{
		{"dropped connection", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }},
		{"proxy error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "proxy", "message": "unavailable: " + r.URL.Path})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					attempted.Store(true)
					tc.reply(w, r)
					return
				}
				link := dropapi.UploadLink{Path: dropapi.UploadRoutePrefix + token, MaxBytes: dropapi.MaxReleaseBytes}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": link})
			}))
			defer server.Close()
			_, err := newClient(server.URL).UploadRelease("app", path, "", false)
			if !attempted.Load() || err == nil || strings.Contains(err.Error(), token) {
				t.Fatal("a failed upload must return an error without its private URL")
			}
		})
	}
}
