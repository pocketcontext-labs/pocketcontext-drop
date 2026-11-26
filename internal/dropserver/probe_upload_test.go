package dropserver_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

func TestSelfServiceManifestProbe(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "probe.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := droptest.NewHosted(t, droptest.Options{ProbeSocket: socket, ReleaseMaxBytes: 8192})
	_, user := h.User("probe@example.test", "user")
	body := droptest.ELF(4096, 4)
	digest := sha(body)
	ref := map[string]any{"sha256": digest}
	admin := h.Token(h.Owner())
	wantError(t, h.Op(admin, "beta.probe_upload", ref), 403, dropapi.CodeForbidden)
	path := h.MustOp(user, "beta.probe_upload", ref)["path"].(string)
	wantUploadError(t, upload(t, h, path, droptest.ELF(8193, 4)), 413, dropapi.CodeReleaseTooLarge)
	wantUploadError(t, upload(t, h, path, droptest.ELF(4096, 5)), 400, dropapi.CodeInvalidInput)
	done := make(chan error, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		reader := bufio.NewReader(c)
		header, e := reader.ReadString('\n')
		if e != nil || header != fmt.Sprintf("%s %d\n", digest, len(body)) {
			done <- fmt.Errorf("bad inspection header")
			return
		}
		buf := make([]byte, len(body))
		if _, e = io.ReadFull(reader, buf); e != nil || !bytes.Equal(buf, body) {
			done <- fmt.Errorf("wrong inspection bytes")
			return
		}
		done <- json.NewEncoder(c).Encode(map[string]any{"outcome": "success", "manifest": json.RawMessage(validCrewManifest)})
	}()
	if w := h.Upload(path, body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	wantUploadError(t, upload(t, h, path, body), 404, dropapi.CodeUploadLinkInvalid)
	profile := h.MustOp(user, "beta.inspect", ref)
	if profile["generic"] != false || num(profile["suggested_multiplier"]) != 4 {
		t.Fatal(profile)
	}
}
