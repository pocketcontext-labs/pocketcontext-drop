// Package leakscan finds secret values where they must never be: log files,
// databases, events, node reports, HTTP answers, MCP output and the agent's
// files. Tests give it sentinels (synthetic secret values with a label) and
// sources, and fail on any finding; a finding names the source and the
// sentinel's label, never the value. Each sentinel is also looked for as
// standard and URL base64 and as hex, so an encoded copy is caught too.
//
// Sources:
//
//   - Text and Bytes: an answer, an MCP result, a captured log.
//   - Path: a file, or a directory walked recursively (logs, state files,
//     database files read as raw bytes, which also catches freed pages).
//   - DB: every value of every table of an open database, row by row.
//
// The node agent's state file (M9, /var/lib/drop-agent/state.json) is one
// more Path source: the agent's tests add
//
//	leakscan.Require(t, sentinels, leakscan.Path(stateFile))
//
// next to their other sources (tests/leak_scan.py is the same scan for the
// Python end-to-end tests).
//
// It is test support only; nothing in the binary imports it.
package leakscan

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/dbx"
)

// Sentinel is a value that must not appear, with a label for reports.
type Sentinel struct {
	Label, Value string
}

// Finding is one sentinel found in one source.
type Finding struct {
	Source, Label string
}

func (f Finding) String() string { return f.Label + " in " + f.Source }

// Source is something to scan.
type Source interface {
	scan(add func(source string, data []byte)) error
}

type textSource struct {
	name string
	data []byte
}

func (s textSource) scan(add func(string, []byte)) error { add(s.name, s.data); return nil }

// Text scans a string, named for reports.
func Text(name, text string) Source { return textSource{name, []byte(text)} }

// Bytes scans bytes, named for reports.
func Bytes(name string, data []byte) Source { return textSource{name, data} }

type pathSource string

func (p pathSource) scan(add func(string, []byte)) error {
	return filepath.WalkDir(string(p), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		add(path, data)
		return nil
	})
}

// Path scans a file, or every regular file below a directory, as raw bytes.
// A missing path is an error: a scan of nothing proves nothing.
func Path(path string) Source { return pathSource(path) }

type dbSource struct {
	name string
	db   dbx.Builder
}

func (s dbSource) scan(add func(string, []byte)) error {
	var tables []string
	if err := s.db.NewQuery("SELECT name FROM sqlite_master WHERE type = 'table'").Column(&tables); err != nil {
		return err
	}
	for _, table := range tables {
		rows, err := s.db.NewQuery("SELECT * FROM `" + strings.ReplaceAll(table, "`", "``") + "`").Rows()
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		columns, _ := rows.Columns()
		var out bytes.Buffer
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			for _, v := range values {
				if b, ok := v.([]byte); ok {
					out.Write(b)
				} else {
					fmt.Fprint(&out, v)
				}
				out.WriteByte('\n')
			}
		}
		rows.Close()
		add(s.name+" table "+table, out.Bytes())
	}
	return nil
}

// DB scans every value of every table of a database.
func DB(name string, db dbx.Builder) Source { return dbSource{name, db} }

// variants are the forms a value is looked for in.
func variants(value string) [][]byte {
	v := []byte(value)
	return [][]byte{v, []byte(base64.StdEncoding.EncodeToString(v)), []byte(base64.RawURLEncoding.EncodeToString(v)), []byte(hex.EncodeToString(v))}
}

// Scan returns every sentinel found in every source. Empty sentinel values
// are refused: they would match everything.
func Scan(sentinels []Sentinel, sources ...Source) ([]Finding, error) {
	for _, s := range sentinels {
		if len(s.Value) < 8 {
			return nil, fmt.Errorf("sentinel %s is shorter than 8 bytes", s.Label)
		}
	}
	var findings []Finding
	add := func(source string, data []byte) {
		for _, s := range sentinels {
			for _, v := range variants(s.Value) {
				if bytes.Contains(data, v) {
					findings = append(findings, Finding{source, s.Label})
					break
				}
			}
		}
	}
	for _, src := range sources {
		if err := src.scan(add); err != nil {
			return findings, err
		}
	}
	return findings, nil
}

// Require fails the test on any finding or scan error.
func Require(t testing.TB, sentinels []Sentinel, sources ...Source) {
	t.Helper()
	findings, err := Scan(sentinels, sources...)
	if err != nil {
		t.Fatalf("leak scan: %v", err)
	}
	for _, f := range findings {
		t.Errorf("secret leak: %s", f)
	}
}
