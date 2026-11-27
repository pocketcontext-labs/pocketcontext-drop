package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/releasebundle"
)

func TestExtractAndReplaceAssets(t *testing.T) {
	root := t.TempDir()
	elf := droptest.ELF(128, 1)
	sum := sha256.Sum256(elf)
	manifest := releasebundle.Manifest{ArchiveSHA256: strings.Repeat("a", 64), Executable: "bin/program", ExecutableSHA256: hex.EncodeToString(sum[:]), Files: []releasebundle.Member{{Path: "bin/program", Size: int64(len(elf))}, {Path: "web/index", Size: 1}}}
	payload := func(m releasebundle.Manifest, asset byte) *bytes.Buffer {
		var buf bytes.Buffer
		meta, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err = binary.Write(&buf, binary.LittleEndian, uint32(len(meta))); err != nil {
			t.Fatal(err)
		}
		buf.Write(meta)
		buf.Write(elf)
		buf.WriteByte(asset)
		return &buf
	}
	for _, asset := range []byte{'1', '2'} {
		dir, err := releaseDirectory(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = extract(payload(manifest, asset), dir); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(dir, "web", "index"))
		if err != nil || !bytes.Equal(actual, []byte{asset}) {
			t.Fatalf("asset %q, %v", actual, err)
		}
	}
	dir, err := releaseDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ExecutableSHA256 = strings.Repeat("0", 64)
	if _, err = extract(payload(manifest, '3'), dir); err == nil {
		t.Fatal("accepted incorrect member digest")
	}
}

func TestInitialDataAppliedOnce(t *testing.T) {
	root := t.TempDir()
	vol := filepath.Join(root, "volume")
	release := filepath.Join(root, "release")
	if err := os.MkdirAll(filepath.Join(release, "example"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "example", "hello"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := seedData(vol, release, "example"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(vol, "example", "hello")
	if b, err := os.ReadFile(target); err != nil || string(b) != "initial" {
		t.Fatal("seed incomplete", err)
	}
	if err := os.RemoveAll(filepath.Join(vol, "example")); err != nil {
		t.Fatal(err)
	}
	if err := seedData(vol, release, "example"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("deleted seed was recreated")
	}
}
