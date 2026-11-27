// This launcher runs as the tenant inside its guest. The browser appends a gzip
// stream containing a bounded JSON manifest followed by the selected files.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/releasebundle"
)

const maxExpanded = 384 << 20

var envName = regexp.MustCompile("^[A-Z_][A-Z_0-9]*$")

func extract(r io.Reader, dir string) (releasebundle.Manifest, error) {
	m, err := releasebundle.ReadManifest(r)
	if err != nil {
		return m, err
	}
	var total int64
	var found bool
	for _, entry := range m.Files {
		if !releasebundle.ValidPath(entry.Path) || entry.Size < 0 || entry.Size > maxExpanded-total {
			return m, errors.New("invalid release member")
		}
		total += entry.Size
		p := filepath.Join(dir, entry.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return m, err
		}
		mode := os.FileMode(0600)
		if entry.Path == m.Executable {
			mode = 0700
			found = true
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return m, err
		}
		var out io.Writer = f
		hash := sha256.New()
		if entry.Path == m.Executable {
			out = io.MultiWriter(f, hash)
		}
		_, err = io.CopyN(out, r, entry.Size)
		if err == nil && entry.Path == m.Executable && hex.EncodeToString(hash.Sum(nil)) != m.ExecutableSHA256 {
			err = errors.New("executable checksum mismatch")
		}
		closeErr := f.Close()
		if err != nil {
			return m, err
		}
		if closeErr != nil {
			return m, closeErr
		}
	}
	if !found {
		return m, errors.New("release executable is missing")
	}
	file, err := os.Open(filepath.Join(dir, m.Executable))
	if err != nil {
		return m, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return m, err
	}
	if problem := dropapi.ReleaseImageProblem(file, stat.Size()); problem != "" {
		return m, errors.New(problem)
	}
	var end [1]byte
	if n, err := io.ReadFull(r, end[:]); n != 0 || err != io.EOF {
		return m, errors.New("unexpected trailing release data")
	}
	return m, nil
}

func run() error {
	// Generic archives do not request manifest-gated platform capabilities.
	if len(os.Args) > 1 && os.Args[1] == "--drop-manifest" {
		return errors.New("generic release")
	}
	f, err := os.Open(os.Args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := releasebundle.Payload(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	dir, err := releaseDirectory(os.TempDir())
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	m, err := extract(bufio.NewReader(gz), dir)
	if err != nil {
		return err
	}
	// Keep Drop’s conventional ./data path available from the release root.
	if err := os.Symlink("/app/data", filepath.Join(dir, "data")); err != nil {
		return err
	}
	if err := seedData("/app/data", dir, m.Seed); err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	for k, v := range m.Env {
		if !envName.MatchString(k) {
			return errors.New("invalid environment name")
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, m.Executable)
	return syscall.Exec(path, append([]string{path}, m.Args...), os.Environ())
}

// NibRunner retains its writable overlay across starts. Reuse a launcher-owned
// directory so repeated restarts do not accumulate old release assets.
func releaseDirectory(tmp string) (string, error) {
	base := filepath.Join(tmp, ".drop-imported-release")
	marker := filepath.Join(base, "launcher-v1")
	if err := os.Mkdir(base, 0700); err == nil {
		if err := os.WriteFile(marker, []byte("drop-guest-v1"), 0600); err != nil {
			return "", err
		}
	} else if !os.IsExist(err) {
		return "", err
	} else {
		st, err := os.Lstat(base)
		if err != nil || !st.IsDir() {
			return "", errors.New("release directory unavailable")
		}
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != "drop-guest-v1" {
			return "", errors.New("release directory already in use")
		}
	}
	dir := filepath.Join(base, "current")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	return dir, os.Mkdir(dir, 0700)
}

// Move the complete seed into a new volume atomically. Save a receipt even
// when the first release had no seed or the volume already contained data.
func seedData(volume, dir, src string) error {
	const marker = ".drop-release-initialized"

	if err := os.MkdirAll(volume, 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(volume, marker)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(volume)
	if err != nil {
		return err
	}
	empty := true
	for _, e := range entries {
		if e.Name() != "lost+found" {
			empty = false
			break
		}
	}
	if src != "" && empty {
		target := filepath.Base(src)
		if target == marker || target == "lost+found" {
			return errors.New("reserved seed directory")
		}
		// Stage on the NibRunner guest overlay before publishing the seed atomically.
		tmp, err := os.MkdirTemp(dir, ".seed-")
		if err != nil {
			return err
		}
		if err = os.CopyFS(tmp, os.DirFS(filepath.Join(dir, src))); err != nil {
			return err
		}
		if err = os.Rename(tmp, filepath.Join(volume, target)); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(volume, marker), []byte("1"), 0600)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Release startup:", err)
		os.Exit(1)
	}
}
