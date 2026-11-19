// Command hello is Drop's test release: a tiny HTTP server built as a static
// linux/amd64 executable from this source, never committed as a binary.
// Tests build it reproducibly with
//
//	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -buildid=" -o hello ./tests/fixtures/hello
//
// (tests/e2e.py does this in a temporary folder). It listens on
// NIBRUN_HTTP_PORT, else PORT, else 8080, and answers every path with a short
// text naming itself and the time of its first start, kept in
// NIBRUN_DATA_DIR/first-start when that directory is set, so a redeploy that
// keeps the app's volume shows the same first start.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	port := os.Getenv("NIBRUN_HTTP_PORT")
	if port == "" {
		port = os.Getenv("PORT")
	}
	if port == "" {
		port = "8080"
	}
	firstStart := time.Now().UTC().Format(time.RFC3339)
	if dir := os.Getenv("NIBRUN_DATA_DIR"); dir != "" {
		marker := filepath.Join(dir, "first-start")
		if data, err := os.ReadFile(marker); err == nil {
			firstStart = string(data)
		} else if err := os.WriteFile(marker, []byte(firstStart), 0o600); err != nil {
			log.Printf("hello: cannot write %s: %v", marker, err)
		}
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "hello from a Drop release\nfirst start: %s\n", firstStart)
	})
	log.Printf("hello: listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
