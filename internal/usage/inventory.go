package usage

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// S3 Inventory (CSV): each daily report is a manifest.json under
// <destination prefix>/<source bucket>/<configuration id>/<YYYY-MM-DDTHH-MMZ>/
// that lists gzipped CSV data files. Their columns follow the manifest's
// fileSchema ("Bucket, Key, VersionId, IsLatest, IsDeleteMarker, Size, ...");
// keys are URL-encoded.

// Manifest is the part of an inventory manifest.json Drop reads.
type Manifest struct {
	SourceBucket string `json:"sourceBucket"`
	FileFormat   string `json:"fileFormat"`
	FileSchema   string `json:"fileSchema"`
	Files        []struct {
		Key  string `json:"key"`
		Size int64  `json:"size"`
	} `json:"files"`
}

// manifestDir is the report's folder name: its date and time (UTC).
var manifestDir = regexp.MustCompile(`/(\d{4}-\d{2}-\d{2})T\d{2}-\d{2}Z/manifest\.json$`)

// ManifestDay returns the day (YYYY-MM-DD) of a manifest key, or "" when the
// key is not a dated inventory manifest.
func ManifestDay(key string) string {
	m := manifestDir.FindStringSubmatch(key)
	if m == nil {
		return ""
	}
	return m[1]
}

// maxManifestBytes bounds a manifest.json.
const maxManifestBytes = 4 << 20

// ParseManifest decodes a manifest and checks that Drop can read its files.
func ParseManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	data, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err != nil {
		return m, err
	}
	if len(data) > maxManifestBytes {
		return m, errors.New("inventory manifest too large")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("inventory manifest: %w", err)
	}
	if m.FileFormat != "CSV" {
		return m, fmt.Errorf("inventory manifest: file format %q, want CSV", m.FileFormat)
	}
	if _, err := schemaColumns(m.FileSchema); err != nil {
		return m, err
	}
	return m, nil
}

type columns struct{ key, isLatest, isDeleteMarker, size int }

func schemaColumns(schema string) (columns, error) {
	c := columns{-1, -1, -1, -1}
	for i, name := range strings.Split(schema, ",") {
		switch strings.TrimSpace(name) {
		case "Key":
			c.key = i
		case "IsLatest":
			c.isLatest = i
		case "IsDeleteMarker":
			c.isDeleteMarker = i
		case "Size":
			c.size = i
		}
	}
	if c.key < 0 || c.isLatest < 0 || c.size < 0 {
		return c, fmt.Errorf("inventory schema %q lacks Key, IsLatest or Size (the configuration must include all versions and the Size field)", schema)
	}
	return c, nil
}

// Stored is one app's storage in an inventory report.
type Stored struct {
	CurrentBytes    int64 `json:"current_bytes"`
	NoncurrentBytes int64 `json:"noncurrent_bytes"`
	Objects         int64 `json:"objects"`  // current versions that are not delete markers
	Versions        int64 `json:"versions"` // every version that is not a delete marker
}

// Inventory sums an inventory report per app. Rows whose key names no app
// are counted in Unattributed; rows that do not parse in Malformed.
type Inventory struct {
	Apps         map[string]*Stored
	Rows         int
	Unattributed int
	Malformed    int
}

// NewInventory returns an empty report sum.
func NewInventory() *Inventory { return &Inventory{Apps: map[string]*Stored{}} }

// AddFile reads one gzipped CSV data file of the report.
func (inv *Inventory) AddFile(r io.Reader, schema string) error {
	cols, err := schemaColumns(schema)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("inventory data file: %w", err)
	}
	defer gz.Close()
	cr := csv.NewReader(gz)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	need := max(cols.key, cols.isLatest, cols.isDeleteMarker, cols.size) + 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			var perr *csv.ParseError
			if errors.As(err, &perr) {
				inv.Rows++
				inv.Malformed++
				continue
			}
			return fmt.Errorf("inventory data file: %w", err)
		}
		inv.Rows++
		if len(rec) < need {
			inv.Malformed++
			continue
		}
		key, err := url.QueryUnescape(rec[cols.key])
		if err != nil {
			inv.Malformed++
			continue
		}
		latest, err := strconv.ParseBool(rec[cols.isLatest])
		if err != nil {
			inv.Malformed++
			continue
		}
		marker := false
		if cols.isDeleteMarker >= 0 && rec[cols.isDeleteMarker] != "" {
			if marker, err = strconv.ParseBool(rec[cols.isDeleteMarker]); err != nil {
				inv.Malformed++
				continue
			}
		}
		var size int64
		if rec[cols.size] != "" {
			if size, err = strconv.ParseInt(rec[cols.size], 10, 64); err != nil || size < 0 {
				inv.Malformed++
				continue
			}
		}
		app := appPrefix(key)
		if app == "" {
			inv.Unattributed++
			continue
		}
		if marker {
			continue
		}
		s := inv.Apps[app]
		if s == nil {
			s = &Stored{}
			inv.Apps[app] = s
		}
		s.Versions++
		if latest {
			s.Objects++
			s.CurrentBytes += size
		} else {
			s.NoncurrentBytes += size
		}
	}
}
