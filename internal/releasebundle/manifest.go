// Package releasebundle reads the archive provenance and file index carried by a generic guest executable.
package releasebundle

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode"
)

var ErrUnbundled = errors.New("plain executable")

type Member struct {
	Path string
	Size int64
}
type Manifest struct {
	ArchiveSHA256    string
	ExecutableSHA256 string
	Executable       string
	Args             []string
	Env              map[string]string
	Seed             string
	Files            []Member
}

func ValidPath(s string) bool {
	return fs.ValidPath(s) && s != "." && len(s) <= 1024 && !strings.ContainsRune(s, '\\') && strings.IndexFunc(s, unicode.IsControl) < 0
}

func Payload(f *os.File) (*gzip.Reader, error) {
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size < 8 {
		return nil, ErrUnbundled
	}
	var trailer [8]byte
	if _, err := f.ReadAt(trailer[:], size-8); err != nil {
		return nil, err
	}
	if string(trailer[4:]) != "DRP1" {
		return nil, ErrUnbundled
	}
	length := int64(binary.LittleEndian.Uint32(trailer[:4]))
	if length < 1 || length > size-8 {
		return nil, errors.New("invalid release trailer")
	}
	return gzip.NewReader(io.NewSectionReader(f, size-8-length, length))
}
func ReadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil || n == 0 || n > 1<<20 {
		return m, errors.New("invalid release manifest")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	if !ValidPath(m.Executable) || len(m.Files) > 4096 || len(m.Files) == 0 || (m.Seed != "" && !ValidPath(m.Seed)) {
		return m, errors.New("invalid release paths")
	}
	for _, digest := range []string{m.ArchiveSHA256, m.ExecutableSHA256} {
		if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 || digest != strings.ToLower(digest) {
			return m, errors.New("invalid release digest")
		}
	}
	return m, nil
}
