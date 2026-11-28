package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func TestBackupGuestProofPinsConnection(t *testing.T) {
	for _, mode := range []string{"valid", "wrong proof", "connection closed"} {
		t.Run(mode, func(t *testing.T) {
			const token = "synthetic-proof-key"
			calls := make(chan string, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/control/backup-peer" {
					if r.Header.Get("Authorization") != "" {
						t.Error("credential sent before proof")
					}
					key := token
					if mode == "wrong proof" {
						key = "different"
					}
					m := hmac.New(sha256.New, []byte(key))
					fmt.Fprint(m, "drop-backup-peer/v1:", r.Header.Get("X-Drop-Challenge"))
					w.Header().Set("X-Drop-Proof", hex.EncodeToString(m.Sum(nil)))
					if mode == "connection closed" {
						w.Header().Set("Connection", "close")
					}
					return
				}
				calls <- r.Header.Get("Authorization")
			}))
			defer srv.Close()
			address := srv.Listener.Addr().String()
			conn, err := net.Dial("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx := context.Background()
			p, err := authenticateBackupPeer(ctx, conn, address, token)
			if p != nil {
				defer p.transport.CloseIdleConnections()
				res, reqErr := p.request(ctx, "GET", "/control/backup", "", "job", nil)
				err = reqErr
				if res != nil {
					res.Body.Close()
				}
			}
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-calls:
					if got != "Bearer "+token {
						t.Fatal("missing bearer")
					}
				default:
					t.Fatal("missing capture")
				}
			} else {
				if err == nil {
					t.Fatal("unproven connection accepted")
				}
				select {
				case <-calls:
					t.Fatal("credential was sent")
				default:
				}
			}
		})
	}
}
func TestBackupStageBoundsAndCleanup(t *testing.T) {
	dir := t.TempDir()
	a := Agent{cfg: Config{StateDir: dir}}
	f, digest, err := a.stageBackup(strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "abc" || digest != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("incorrect staged archive")
	}
	if _, _, err = a.stageBackup(strings.NewReader("longer"), 3); !errors.Is(err, errArchiveTooLarge) {
		t.Fatalf("unexpected error %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("archive left on disk")
	}
}
func TestBackupTargetMatchesAppliedInstance(t *testing.T) {
	path := t.TempDir() + "/desired.json"
	a := Agent{cfg: Config{Desired: path, AllowedHostSuffix: ".example.test"}}
	j := dropapi.BackupJob{AppID: "d-aaaaaaaaaaaaaaa", InstanceID: "d-aaaaaaaaaaaaaaa-dep-1"}
	for _, c := range []struct{ state, deployment, hostname, want string }{
		{"on-request", j.InstanceID, "crew.example.test", "crew.example.test"},
		{"running", j.InstanceID, "wrong.test", ""},
		{"running", j.InstanceID + "0", "crew.example.test", ""},
		{"stopped", j.InstanceID, "crew.example.test", ""},
	} {
		doc := fmt.Sprintf(`{"instances":[{"appId":%q,"deploymentId":%q,"desiredState":%q,"hostnames":[{"hostname":%q}]}]}`, j.AppID, c.deployment, c.state, c.hostname)
		if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if got := a.backupHostname(j); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// Exercise the same node transfer used by the worker, including durable acknowledgement
// after a guest restart and rejection of archive bytes with an invalid checksum.
func TestBackupTransferRoundTrip(t *testing.T) {
	ctx := context.Background()
	j := dropapi.BackupJob{ID: "bbbbbbbbbbbbbbb", BackupID: "ccccccccccccccc", Kind: "capture", AppID: "d-aaaaaaaaaaaaaaa", SourceApp: "d-aaaaaaaaaaaaaaa", InstanceID: "d-aaaaaaaaaaaaaaa-dep-1"}
	const contents = "synthetic workspace bytes"
	const token = "synthetic guest token"
	var stored, restored string
	var success, done bool
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "node-test-token" {
			t.Error("node authentication missing")
		}
		switch {
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/archive"):
			data, _ := io.ReadAll(r.Body)
			stored = string(data)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/archive"):
			io.WriteString(w, stored)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
			var result dropapi.BackupJobResult
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			success = result.Success
			done = true
		default:
			t.Error("unexpected node route")
		}
	}))
	defer controller.Close()
	guest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/control/backup-peer" {
			m := hmac.New(sha256.New, []byte(token))
			fmt.Fprint(m, "drop-backup-peer/v1:", r.Header.Get("X-Drop-Challenge"))
			w.Header().Set("X-Drop-Proof", hex.EncodeToString(m.Sum(nil)))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Drop-Job") != j.ID {
			t.Error("guest authentication or job binding missing")
		}
		switch r.URL.Path {
		case "/control/backup":
			io.WriteString(w, contents)
		case "/control/restore-status":
			if restored == "" {
				w.WriteHeader(404)
			}
		case "/control/restore":
			if r.Header.Get("X-Drop-Source-App") != j.SourceApp {
				t.Error("source app binding missing")
			}
			data, _ := io.ReadAll(r.Body)
			restored = string(data)
			w.WriteHeader(202)
		default:
			t.Error("unexpected guest route")
		}
	}))
	defer guest.Close()
	connect := func() (*backupPeer, error) {
		addr := guest.Listener.Addr().String()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		return authenticateBackupPeer(ctx, conn, addr, token)
	}
	cfg := Config{Server: controller.URL, Host: "drop.example.test", StateDir: t.TempDir(), Desired: t.TempDir() + "/desired.json", AllowedHostSuffix: ".example.test"}
	a := Agent{client: NewClient(cfg, "", "test"), cfg: cfg}
	a.client.token = "node-test-token"
	host := "crew.example.test"
	a.transferBackup(ctx, j, host, connect)
	if stored != contents {
		t.Fatal("capture failed")
	}
	j.Kind = "restore"
	j.AppID = "d-bbbbbbbbbbbbbbb"
	j.InstanceID = j.AppID + "-dep-1"
	j.Bytes = int64(len(stored))
	doc := fmt.Sprintf(`{"instances":[{"appId":%q,"deploymentId":%q,"desiredState":"running","hostnames":[{"hostname":%q}]}]}`, j.AppID, j.InstanceID, host)
	if err := os.WriteFile(cfg.Desired, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	j.SHA256 = strings.Repeat("0", 64)
	a.transferBackup(ctx, j, host, connect)
	if !done || success || restored != "" {
		t.Fatal("checksum failure did not stop restore")
	}
	done = false
	j.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(stored)))
	a.transferBackup(ctx, j, host, connect)
	if restored != contents || done {
		t.Fatal("staged restore prematurely completed or wrong bytes")
	}
	a.transferBackup(ctx, j, host, connect)
	if !done || !success {
		t.Fatal("restored guest did not confirm success")
	}
}
