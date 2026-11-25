package usage

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got (as indented JSON) with testdata/name, or rewrites it.
func golden(t *testing.T, name string, got any) {
	t.Helper()
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, data) {
		t.Errorf("%s differs (rerun with -update to accept):\n%s", name, data)
	}
}

type hourRow struct {
	App, Hour string
	Counts
}

// The fixture is synthetic, in the exact layout of S3 server access logs
// (documentation addresses, a made-up account and request ids).
func TestParseLogGolden(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "accesslog.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := ParseLog(f)
	if err != nil {
		t.Fatal(err)
	}
	rows := []hourRow{}
	for k, c := range s.Hours {
		rows = append(rows, hourRow{App: k.App, Hour: k.Hour, Counts: *c})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].App != rows[j].App {
			return rows[i].App < rows[j].App
		}
		return rows[i].Hour < rows[j].Hour
	})
	golden(t, "accesslog.golden.json", map[string]any{
		"lines": s.Lines, "malformed": s.Malformed, "unattributed": s.Unattributed, "ignored": s.Ignored, "hours": rows,
	})
	// Spot checks that do not depend on the golden file.
	a := s.Hours[HourKey{"d-aaaaaaaaaaaaaaa", "2026-11-25T14:00:00Z"}]
	if a == nil || a.BytesIn != 1048576 || a.Deletes != 1 || a.Status5xx != 1 {
		t.Fatalf("app A 14:00: %+v", a)
	}
	b := s.Hours[HourKey{"d-bbbbbbbbbbbbbbb", "2026-11-25T14:00:00Z"}]
	if b == nil || b.Denied != 2 || b.Deletes != 2 {
		t.Fatalf("app B 14:00: %+v", b)
	}
	if late := s.Hours[HourKey{"d-aaaaaaaaaaaaaaa", "2026-11-25T15:00:00Z"}]; late == nil || late.ClassB != 2 {
		t.Fatalf("app A 15:00 (one record in +0100): %+v", late)
	}
	if s.Malformed != 6 {
		t.Fatalf("malformed %d, want 6", s.Malformed)
	}
}

func TestParseLineStrict(t *testing.T) {
	good := `0000 bucket [25/Nov/2026:14:05:01 +0000] 192.0.2.1 - REQ REST.GET.OBJECT d-aaaaaaaaaaaaaaa/k "GET /d-aaaaaaaaaaaaaaa/k HTTP/1.1" 200 - 5 5 1 1 "-" "-" -`
	l, err := ParseLine(good)
	if err != nil || l.Status != 200 || l.BytesSent != 5 || l.Key != "d-aaaaaaaaaaaaaaa/k" || !l.Time.Equal(time.Date(2026, 11, 25, 14, 5, 1, 0, time.UTC)) {
		t.Fatalf("good line: %+v %v", l, err)
	}
	for _, bad := range []string{
		"",
		strings.Replace(good, " 200 ", "  200 ", 1),       // empty field
		" " + good,                                        // leading space
		good + " ",                                        // trailing space
		strings.Replace(good, `"-" -`, `"-"-`, 1),         // no space after a quote
		strings.Replace(good, "[25/Nov", "25/Nov", 1),     // no bracket
		strings.Replace(good, "REST.GET.OBJECT", "-", 1),  // no operation
		strings.Replace(good, " 200 ", " 999 ", 1),        // bad status
		strings.Replace(good, " 5 5 ", " 5 x ", 1),        // bad size
		strings.Replace(good, "-0000]", "+0000]", 1) + `"`, // stray quote
	} {
		if _, err := ParseLine(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseLogLongLine(t *testing.T) {
	good := `0000 bucket [25/Nov/2026:14:05:01 +0000] 192.0.2.1 - REQ REST.GET.OBJECT d-aaaaaaaaaaaaaaa/k "GET /d-aaaaaaaaaaaaaaa/k HTTP/1.1" 200 - 5 5 1 1 "-" "-" -`
	input := good + "\n" + strings.Repeat("x", 200<<10) + "\n" + good
	s, err := ParseLog(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if s.Lines != 3 || s.Malformed != 1 || s.Hours[HourKey{"d-aaaaaaaaaaaaaaa", "2026-11-25T14:00:00Z"}].ClassB != 2 {
		t.Fatalf("summary %+v", s)
	}
}

func TestClassify(t *testing.T) {
	for op, want := range map[string]Class{
		"REST.PUT.OBJECT": ClassA, "REST.COPY.OBJECT": ClassA, "REST.POST.UPLOADS": ClassA, "REST.POST.UPLOAD": ClassA,
		"REST.PUT.PART": ClassA, "REST.GET.BUCKET": ClassA, "REST.GET.BUCKETVERSIONS": ClassA, "REST.GET.UPLOADS": ClassA,
		"REST.GET.OBJECT": ClassB, "REST.HEAD.OBJECT": ClassB, "REST.GET.BUCKETPOLICY": ClassB, "REST.OPTIONS.PREFLIGHT": ClassB,
		"REST.DELETE.OBJECT": Delete, "BATCH.DELETE.OBJECT": Delete, "REST.DELETE.UPLOAD": Delete,
		"S3.EXPIRE.OBJECT": Ignored, "S3.CREATE.DELETEMARKER": Ignored, "REST.COPY.OBJECT_GET": Ignored,
	} {
		if got := Classify(op); got != want {
			t.Errorf("%s: %v, want %v", op, got, want)
		}
	}
}

func TestInventoryGolden(t *testing.T) {
	mf, err := os.Open(filepath.Join("testdata", "inventory-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer mf.Close()
	m, err := ParseManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	csv, err := os.ReadFile(filepath.Join("testdata", "inventory.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(csv)
	w.Close()
	inv := NewInventory()
	if err := inv.AddFile(&gz, m.FileSchema); err != nil {
		t.Fatal(err)
	}
	golden(t, "inventory.golden.json", inv)
	b := inv.Apps["d-bbbbbbbbbbbbbbb"]
	if b == nil || b.CurrentBytes != 5242880+4096 || b.NoncurrentBytes != 7000 || b.Objects != 2 || b.Versions != 4 {
		t.Fatalf("app B: %+v", b)
	}
	if d := ManifestDay("inventory/replicas-example/daily-all-versions/2026-11-25T01-00Z/manifest.json"); d != "2026-11-25" {
		t.Fatalf("day %q", d)
	}
	for _, k := range []string{"inventory/x/data/manifest.json", "inventory/x/2026-11-25T01-00Z/manifest.checksum"} {
		if ManifestDay(k) != "" {
			t.Errorf("%s is a manifest", k)
		}
	}
	if _, err := ParseManifest(strings.NewReader(`{"fileFormat":"ORC","fileSchema":"Key, IsLatest, Size"}`)); err == nil {
		t.Error("ORC accepted")
	}
	if _, err := ParseManifest(strings.NewReader(`{"fileFormat":"CSV","fileSchema":"Bucket, Key"}`)); err == nil {
		t.Error("a schema without versions or sizes accepted")
	}
}

func TestLimitsAndEstimate(t *testing.T) {
	l := Effective(Override{BytesOut: 5 * GB})
	if l.RequestsA != 100_000 || l.Hard(MetricRequestsA) != 1_000_000 || l.BytesOut != 5*GB || l.StoredBytes != GB {
		t.Fatalf("limits %+v", l)
	}
	p := Period{Counts: Counts{ClassA: 60_000, Deletes: 40_000, ClassB: 2_000_000, BytesOut: GB}, Stored: 11 * GB}
	for metric, want := range map[string]string{MetricRequestsA: LevelSoft, MetricRequestsB: LevelSoft, MetricBytesOut: LevelOK, MetricStored: LevelHard} {
		if got := Level(p.Value(metric), l, metric); got != want {
			t.Errorf("%s: %s, want %s", metric, got, want)
		}
	}
	e := EstimateCost(p)
	if e.ClassA != 0.3 || e.ClassB != 0.8 || e.Egress != 0.09 || e.Storage != 0.253 || e.Total != 1.443 {
		t.Fatalf("estimate %+v", e)
	}
}
