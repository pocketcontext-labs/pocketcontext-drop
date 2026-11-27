package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

func loadInput(t *testing.T, name string) Input {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in Input
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return in
}

func mustRender(t *testing.T, in Input) *Output {
	t.Helper()
	out, err := Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func generic(t *testing.T, data []byte) any {
	t.Helper()
	g, err := toGeneric(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

const crewID = "d-crewdev00000001"

func TestGolden(t *testing.T) {
	out := mustRender(t, loadInput(t, "crew-dev.input.json"))
	path := filepath.Join("testdata", "crew-dev.golden.json")
	if *update {
		if err := os.WriteFile(path, out.Document, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Document, want) {
		t.Fatalf("output differs from %s (run go test -run TestGolden -update to inspect)\n%s", path, out.Document)
	}
	if err := ValidateSchema(want); err != nil {
		t.Fatal(err)
	}
	if hash, err := ContentHash(want); err != nil || hash != out.ContentHash {
		t.Fatalf("ContentHash(golden) = %s, %v; want %s", hash, err, out.ContentHash)
	}
	if !strings.HasPrefix(out.Revision, "drop-1-") || out.Revision != "drop-1-"+out.ContentHash[:12] {
		t.Fatalf("revision %q, hash %s", out.Revision, out.ContentHash)
	}
	if len(out.Rejected) != 0 || len(out.Tombstoned) != 0 {
		t.Fatalf("rejected %v, tombstoned %v", out.Rejected, out.Tombstoned)
	}
}

func TestDeferredBillingRevision(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	in.Apps[0].DeferUntilRequest = true
	if out := mustRender(t, in); len(out.Rejected) != 1 || out.Rejected[0].Field != "deferUntilRequest" {
		t.Fatal("agent accepted deferred activation without runner support")
	}
	in.Limits.DeferUntilRequest = true
	out := mustRender(t, in)
	if len(out.Rejected) != 0 || !bytes.Contains(out.Document, []byte(`"deferUntilRequest": true`)) {
		t.Fatal("missing deferred activation")
	}
	in.Previous, in.Apps = out.Document, nil
	tombstone := mustRender(t, in)
	if !slices.Equal(tombstone.Tombstoned, []string{crewID}) || bytes.Contains(tombstone.Document, []byte("deferUntilRequest")) {
		t.Fatal("stopped tombstone retained deferred activation")
	}
}

// The golden entry is the live crew-dev entry (sanitized copy attached to the
// M8 task), renamed to a Drop id, with its staged objectKey and explicit host storage.
func TestGoldenMatchesLiveEntry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "crew-dev.live.json"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.NewReplacer(
		`"crew-dev-dep-1"`, `"`+crewID+`-dep-1"`,
		`"crew-dev-vol"`, `"`+crewID+`-vol"`,
		`"crew-dev"`, `"`+crewID+`"`,
		`"crew-0.30.1-drop"`, `"1ae3cb5471536bead6b9395b86d154c64db1233d6f56ae4896c58f3fcdc98715"`,
	).Replace(string(data))
	live := generic(t, []byte(renamed)).(map[string]any)

	golden, err := os.ReadFile(filepath.Join("testdata", "crew-dev.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := generic(t, golden).(map[string]any)
	if doc["hostId"] != "walter-dev" {
		t.Fatalf("hostId %v", doc["hostId"])
	}
	for _, key := range []string{"checkpoints", "exports"} {
		if l, _ := doc[key].([]any); l == nil || len(l) != 0 {
			t.Fatalf("%s = %v, want []", key, doc[key])
		}
	}
	insts, vols := doc["instances"].([]any), doc["volumes"].([]any)
	if len(insts) != 1 || len(vols) != 1 {
		t.Fatalf("want one instance and one volume")
	}
	if !reflect.DeepEqual(insts[0], live["instance"]) {
		t.Fatalf("instance differs from the live entry:\n got %s\nwant %s", js(insts[0]), js(live["instance"]))
	}
	// The current NibRunner schema spells the original host backend explicitly.
	live["volume"].(map[string]any)["storage"] = nil
	if !reflect.DeepEqual(vols[0], live["volume"]) {
		t.Fatalf("volume differs from the live entry:\n got %s\nwant %s", js(vols[0]), js(live["volume"]))
	}
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// randomApp builds a valid app from r.
func randomApp(r *rand.Rand, i int) App {
	id := fmt.Sprintf("d-%015d", r.Int63n(1e15))
	if i >= 0 {
		id = fmt.Sprintf("d-app%012d", i)
	}
	sha := fmt.Sprintf("%064x", r.Uint64())
	states := []string{"running", "on-request", "stopped"}
	app := App{
		AppID:          id,
		DeploymentID:   fmt.Sprintf("%s-dep-%d", id, 1+r.Intn(1000)),
		DesiredState:   states[r.Intn(3)],
		IdleTimeoutMs:  int64(minIdleMs + r.Intn(maxIdleMs-minIdleMs)),
		Volume:         Volume{VolumeID: id + "-vol", SizeBytes: int64(minDiskMib+r.Intn(maxDiskMib-minDiskMib)) * mib},
		Release:        Release{SHA256: sha, S3Key: "releases/" + sha, SizeBytes: 1 + r.Int63n(maxReleaseBytes)},
		ExecutableName: []string{"server", "crew", "app-1"}[r.Intn(3)],
		HTTPPort:       minPort + r.Intn(maxPort-minPort),
		Args:           []string{"--flag", fmt.Sprint(r.Intn(100))}[:r.Intn(3)],
		Resources:      Resources{VCPU: 1 + r.Intn(2), MemoryMib: minMemoryMib + r.Intn(maxMemoryMib-minMemoryMib)},
		HealthCheck:    HealthCheck{Kind: "tcp"},
		Hostnames:      []Hostname{{Hostname: fmt.Sprintf("h%d-%s.bigconfig.space", r.Intn(1e6), id[2:]), Kind: "platform"}},
		Env:            map[string]string{},
	}
	if r.Intn(2) == 0 {
		app.HealthCheck = HealthCheck{Kind: "http", Path: "/up"}
	}
	if r.Intn(2) == 0 {
		app.Hostnames = append(app.Hostnames, Hostname{Hostname: "a-" + id[2:] + ".bigconfig.space", Kind: "custom"})
	}
	for k := 0; k < r.Intn(5); k++ {
		app.Env[fmt.Sprintf("VAR_%d", k)] = fmt.Sprintf("value <%d> & ${NIBRUN_HTTP_PORT}", r.Intn(1000))
	}
	for k := 0; k < r.Intn(3); k++ {
		name := fmt.Sprintf("SECRET_%d", k)
		app.Secrets = append(app.Secrets, Secret{Name: name, Parameter: "/drop/apps/" + id[2:] + "/" + name, Version: 1 + int64(r.Intn(5)), Value: fmt.Sprintf("s3cr3t-%d", r.Int63())})
	}
	return app
}

func baseInput(apps ...App) Input {
	return Input{HostID: "walter-dev", DesiredSeq: 7, Apps: apps, Limits: Limits{AllowedHostSuffix: ".bigconfig.space", SSMPrefix: "/drop"}}
}

func randomInput(r *rand.Rand) Input {
	n := r.Intn(6)
	apps := make([]App, n)
	for i := range apps {
		apps[i] = randomApp(r, i)
	}
	return baseInput(apps...)
}

// assertNeverAbsent fails if any desiredState in doc is absent, and checks
// every volume is present.
func assertNeverAbsent(t *testing.T, doc []byte) {
	t.Helper()
	d := generic(t, doc).(map[string]any)
	for _, v := range d["volumes"].([]any) {
		if s := v.(map[string]any)["desiredState"]; s != "present" {
			t.Fatalf("volume desiredState %v", s)
		}
	}
	for _, v := range d["instances"].([]any) {
		if s := v.(map[string]any)["desiredState"]; s == "absent" {
			t.Fatalf("instance desiredState absent")
		}
	}
	if bytes.Contains(doc, []byte(`"absent"`)) {
		t.Fatalf("document contains \"absent\"")
	}
}

// Property: unchanged input gives byte-identical output, whatever the order of
// apps, hostnames or map iteration; and re-rendering on top of the output
// changes nothing but is stable.
func TestPropertyDeterministic(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		in := randomInput(r)
		a := mustRender(t, in)
		if len(a.Rejected) != 0 {
			t.Fatalf("case %d: random app rejected: %v", i, a.Rejected)
		}
		b := mustRender(t, in)
		if !bytes.Equal(a.Document, b.Document) || a.Revision != b.Revision {
			t.Fatalf("case %d: two renders differ", i)
		}
		shuffled := in
		shuffled.Apps = slices.Clone(in.Apps)
		r.Shuffle(len(shuffled.Apps), func(x, y int) { shuffled.Apps[x], shuffled.Apps[y] = shuffled.Apps[y], shuffled.Apps[x] })
		for k := range shuffled.Apps {
			hs := slices.Clone(shuffled.Apps[k].Hostnames)
			slices.Reverse(hs)
			shuffled.Apps[k].Hostnames = hs
		}
		if c := mustRender(t, shuffled); !bytes.Equal(a.Document, c.Document) {
			t.Fatalf("case %d: order changed the output", i)
		}
		again := in
		again.Previous = a.Document
		d := mustRender(t, again)
		if !bytes.Equal(a.Document, d.Document) || len(d.Tombstoned) != 0 {
			t.Fatalf("case %d: rendering over the previous output changed it", i)
		}
		assertNeverAbsent(t, a.Document)
		if err := ValidateSchema(a.Document); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

// Property: removing apps never yields absent; they become stopped tombstones
// with their volumes present, and the revision follows the content.
func TestPropertyNeverAbsent(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 200; i++ {
		in := randomInput(r)
		first := mustRender(t, in)
		next := in
		next.DesiredSeq++
		next.Previous = first.Document
		keep := r.Intn(len(in.Apps) + 1)
		next.Apps = in.Apps[:keep]
		out := mustRender(t, next)
		assertNeverAbsent(t, out.Document)
		var want []string
		for _, app := range in.Apps[keep:] {
			want = append(want, app.AppID)
		}
		slices.Sort(want)
		if !slices.Equal(out.Tombstoned, want) {
			t.Fatalf("case %d: tombstoned %v, want %v", i, out.Tombstoned, want)
		}
		var doc document
		if err := json.Unmarshal(out.Document, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Instances) != len(in.Apps) || len(doc.Volumes) != len(in.Apps) {
			t.Fatalf("case %d: %d instances, %d volumes, want %d", i, len(doc.Instances), len(doc.Volumes), len(in.Apps))
		}
		for _, inst := range doc.Instances {
			if slices.Contains(want, inst.AppID) && (inst.DesiredState != "stopped" || inst.Activation != nil) {
				t.Fatalf("case %d: tombstone %s is %s with activation %v", i, inst.AppID, inst.DesiredState, inst.Activation)
			}
		}
		// A tombstone rendered again stays the same.
		third := next
		third.Previous = out.Document
		if again := mustRender(t, third); !bytes.Equal(again.Document, out.Document) {
			t.Fatalf("case %d: tombstones are not stable", i)
		}
	}
}

// Property: any entry whose appId does not start with d-, in the apps, the keep
// list or the previous document, refuses the whole input with foreign_entries
// listing it.
func TestPropertyForeignRefused(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	foreign := []string{"canary", "crew-dev", "D-upper", "x-d-app", "dd-app", "", "d_app"}
	for i := 0; i < 200; i++ {
		in := randomInput(r)
		good := mustRender(t, in)
		id := foreign[r.Intn(len(foreign))]
		var bad Input
		switch i % 3 {
		case 0:
			app := randomApp(r, -1)
			app.AppID = id
			bad = in
			bad.Apps = append(slices.Clone(in.Apps), app)
		case 1:
			bad = in
			bad.Keep = []string{id}
		case 2:
			bad = in
			bad.Previous = injectForeign(t, good.Document, id)
		}
		out, err := Render(bad)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeForeignEntries || out != nil {
			t.Fatalf("case %d (%q): got %v, %v", i, id, out, err)
		}
		if !slices.Contains(e.IDs, id) {
			t.Fatalf("case %d: IDs %q do not list %q", i, e.IDs, id)
		}
	}
}

// injectForeign adds a foreign instance and volume to a rendered document.
func injectForeign(t *testing.T, doc []byte, id string) []byte {
	t.Helper()
	d := generic(t, doc).(map[string]any)
	d["volumes"] = append(d["volumes"].([]any), map[string]any{"volumeId": id + "-vol", "appId": id, "sizeBytes": json.Number("1073741824"), "desiredState": "present"})
	d["instances"] = append(d["instances"].([]any), map[string]any{"appId": id, "unknownField": true})
	out, err := encode(d, true)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestForeignInLivePrevious(t *testing.T) {
	// The shape of the live document before the cleanup: canary and crew-dev.
	prev := []byte(`{"hostId":"walter-dev","revision":"crew-dev-1",
	  "volumes":[{"volumeId":"canary-vol","appId":"canary","sizeBytes":1073741824,"desiredState":"present"},
	             {"volumeId":"crew-dev-vol","appId":"crew-dev","sizeBytes":4294967296,"desiredState":"present"}],
	  "instances":[{"appId":"canary"},{"appId":"crew-dev"}],
	  "checkpoints":[],"exports":[{"appId":"exp"}]}`)
	in := loadInput(t, "crew-dev.input.json")
	in.Previous = prev
	_, err := Render(in)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeForeignEntries || !slices.Equal(e.IDs, []string{"canary", "crew-dev", "exp"}) {
		t.Fatalf("got %v", err)
	}
	ids, err := ForeignAppIDs(prev)
	if err != nil || !slices.Equal(ids, []string{"canary", "crew-dev", "exp"}) {
		t.Fatalf("ForeignAppIDs = %v, %v", ids, err)
	}
	// The emptied host (revision empty-1) is not foreign and renders.
	in.Previous = []byte(`{"hostId":"walter-dev","revision":"empty-1","volumes":[],"instances":[],"checkpoints":[],"exports":[]}`)
	mustRender(t, in)
	if ids, err := ForeignAppIDs(in.Previous); err != nil || len(ids) != 0 {
		t.Fatalf("ForeignAppIDs(empty) = %v, %v", ids, err)
	}
}

func TestTombstoneOfGolden(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	first := mustRender(t, in)
	in.Apps = nil
	in.DesiredSeq = 2
	in.Previous = first.Document
	out := mustRender(t, in)
	if !slices.Equal(out.Tombstoned, []string{crewID}) {
		t.Fatalf("tombstoned %v", out.Tombstoned)
	}
	var doc document
	if err := json.Unmarshal(out.Document, &doc); err != nil {
		t.Fatal(err)
	}
	inst := doc.Instances[0]
	if inst.DesiredState != "stopped" || inst.Activation != nil || inst.DeploymentID != crewID+"-dep-1" {
		t.Fatalf("tombstone %+v", inst)
	}
	if doc.Volumes[0].DesiredState != "present" {
		t.Fatalf("volume %+v", doc.Volumes[0])
	}
	if out.Revision == first.Revision || !strings.HasPrefix(out.Revision, "drop-2-") {
		t.Fatalf("revision %s after %s", out.Revision, first.Revision)
	}
}

func TestKeepCarriesPreviousEntry(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	first := mustRender(t, in)
	in.Apps = nil
	in.Keep = []string{crewID}
	in.Previous = first.Document
	out := mustRender(t, in)
	if out.ContentHash != first.ContentHash || !bytes.Equal(out.Document, first.Document) {
		t.Fatalf("kept entry changed:\n%s", out.Document)
	}
	// Keep with nothing to keep is reported, and nothing is invented.
	in.Keep = []string{"d-nothing00000000"}
	out = mustRender(t, in)
	if len(out.Rejected) != 1 || out.Rejected[0].Code != CodeKeepMissing {
		t.Fatalf("rejected %v", out.Rejected)
	}
	if !slices.Equal(out.Tombstoned, []string{crewID}) {
		t.Fatalf("tombstoned %v", out.Tombstoned)
	}
}

func TestRejectedAppKeepsPreviousEntry(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	first := mustRender(t, in)
	in.Previous = first.Document
	in.Apps[0].ExecutableName = "../../bin/sh"
	in.Apps[0].DeploymentID = crewID + "-dep-2"
	out := mustRender(t, in)
	if len(out.Rejected) != 1 || out.Rejected[0].Field != "executableName" || len(out.Tombstoned) != 0 {
		t.Fatalf("rejected %v tombstoned %v", out.Rejected, out.Tombstoned)
	}
	if out.ContentHash != first.ContentHash {
		t.Fatalf("a rejected app changed its previous entry")
	}
	// Without a previous entry the rejected app is left out.
	in.Previous = nil
	out = mustRender(t, in)
	if !bytes.Contains(out.Document, []byte(`"instances": []`)) {
		t.Fatalf("rejected app rendered:\n%s", out.Document)
	}
}

func TestVolumeResizeRefused(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	first := mustRender(t, in)
	in.Previous = first.Document
	in.Apps[0].Volume.SizeBytes = 8192 * mib
	out := mustRender(t, in)
	if len(out.Rejected) != 1 || out.Rejected[0].Code != CodeVolumeResize || out.ContentHash != first.ContentHash {
		t.Fatalf("rejected %v", out.Rejected)
	}
}

func TestPreviousDocumentTrailingData(t *testing.T) {
	const prior = `{"hostId":"host","revision":"r","volumes":[],"instances":[],"exports":[],"checkpoints":[]}`
	for _, tail := range []string{"{}", "]", "broken"} {
		raw := []byte(prior + tail)
		if _, err := PreviewPrevious(raw, nil, "REDACTED"); err == nil {
			t.Errorf("preview accepted trailing %q", tail)
		}
		if _, err := parsePrevious(raw, "host"); err == nil {
			t.Errorf("live renderer accepted trailing %q", tail)
		}
	}
}

func TestWholeInputRefusals(t *testing.T) {
	golden := loadInput(t, "crew-dev.input.json")
	first := mustRender(t, golden)
	cases := []struct {
		name string
		mut  func(*Input)
		code string
	}{
		{"bad host id", func(in *Input) { in.HostID = "walter dev" }, CodeInvalidInput},
		{"negative seq", func(in *Input) { in.DesiredSeq = -1 }, CodeInvalidInput},
		{"no suffix", func(in *Input) { in.Limits.AllowedHostSuffix = "" }, CodeInvalidInput},
		{"suffix without dot", func(in *Input) { in.Limits.AllowedHostSuffix = "bigconfig.space" }, CodeInvalidInput},
		{"bad ssm prefix", func(in *Input) { in.Limits.SSMPrefix = "drop/" }, CodeInvalidInput},
		{"duplicate app", func(in *Input) { in.Apps = append(in.Apps, in.Apps[0]) }, CodeInvalidInput},
		{"app and keep", func(in *Input) { in.Keep = []string{crewID} }, CodeInvalidInput},
		{"duplicate hostname", func(in *Input) {
			other := randomApp(rand.New(rand.NewSource(9)), 1)
			other.Hostnames = in.Apps[0].Hostnames
			in.Apps = append(in.Apps, other)
		}, CodeInvalidInput},
		{"previous not json", func(in *Input) { in.Previous = []byte("{") }, CodeInvalidPrevious},
		{"previous other host", func(in *Input) { in.Previous = first.Document; in.HostID = "other-host" }, CodeHostMismatch},
		{"previous unknown field", func(in *Input) {
			in.Previous = bytes.Replace(first.Document, []byte(`"access": {`), []byte(`"access": {"extra": 1, `), 1)
		}, CodeInvalidPrevious},
		{"previous idleTimeoutMs spelling", func(in *Input) {
			in.Previous = bytes.Replace(first.Document, []byte(`"appId": "d-crewdev00000001",`), []byte(`"appId": "d-crewdev00000001", "idleTimeoutMs": 300000,`), 1)
		}, CodeInvalidPrevious},
		{"previous checkpoint", func(in *Input) {
			in.Previous = bytes.Replace(first.Document, []byte(`"checkpoints": []`), []byte(`"checkpoints": [{"checkpointId":"c","volumeId":"d-crewdev00000001-vol","desiredState":"present"}]`), 1)
		}, CodeUnsupportedEntries},
		{"custom volume backend", func(in *Input) {
			in.Previous = bytes.Replace(first.Document, []byte(`"storage": null`), []byte(`"storage": {"bucket":"foreign"}`), 1)
		}, CodeInvalidPrevious},
		{"previous absent volume", func(in *Input) {
			in.Previous = bytes.Replace(first.Document, []byte(`"desiredState": "present"`), []byte(`"desiredState": "absent"`), 1)
		}, CodeUnsupportedEntries},
		{"previous malformed d- id", func(in *Input) {
			in.Previous = bytes.ReplaceAll(first.Document, []byte(crewID), []byte("d-UPPER"))
		}, CodeInvalidPrevious},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := loadInput(t, "crew-dev.input.json")
			c.mut(&in)
			out, err := Render(in)
			var e *Error
			if !errors.As(err, &e) || e.Code != c.code || out != nil {
				t.Fatalf("got %v, %v; want %s", out, err, c.code)
			}
		})
	}
}

// Every 7.3 rule refuses the app alone, naming the field and never a value.
func TestAppValidation(t *testing.T) {
	cases := []struct {
		field string
		mut   func(*App)
	}{
		{"appId", func(a *App) { a.AppID = "d-short" }},
		{"appId", func(a *App) { a.AppID = "d-CREWDEV00000001" }},
		{"deploymentId", func(a *App) { a.DeploymentID = "d-other0000000001-dep-1" }},
		{"deploymentId", func(a *App) { a.DeploymentID = crewID + "-dep-1234567890" }},
		{"deploymentId", func(a *App) { a.DeploymentID = crewID + "-dep-" }},
		{"desiredState", func(a *App) { a.DesiredState = "absent" }},
		{"idleTimeoutMs", func(a *App) { a.IdleTimeoutMs = 59999 }},
		{"idleTimeoutMs", func(a *App) { a.IdleTimeoutMs = 86400001 }},
		{"volume.volumeId", func(a *App) { a.Volume.VolumeID = "d-other0000000001-vol" }},
		{"volume.sizeBytes", func(a *App) { a.Volume.SizeBytes = 255 * mib }},
		{"volume.sizeBytes", func(a *App) { a.Volume.SizeBytes = 16385 * mib }},
		{"volume.sizeBytes", func(a *App) { a.Volume.SizeBytes = 1024*mib + 1 }},
		{"release.sha256", func(a *App) { a.Release.SHA256 = strings.ToUpper(a.Release.SHA256) }},
		{"release.s3Key", func(a *App) { a.Release.S3Key = "other/" + a.Release.SHA256 }},
		{"release.sizeBytes", func(a *App) { a.Release.SizeBytes = 0 }},
		{"release.sizeBytes", func(a *App) { a.Release.SizeBytes = 90*mib + 1 }},
		{"executableName", func(a *App) { a.ExecutableName = "../crew" }},
		{"executableName", func(a *App) { a.ExecutableName = "Crew" }},
		{"executableName", func(a *App) { a.ExecutableName = "" }},
		{"httpPort", func(a *App) { a.HTTPPort = 0 }},
		{"httpPort", func(a *App) { a.HTTPPort = 65536 }},
		{"args", func(a *App) { a.Args = make([]string, 65) }},
		{"args[0]", func(a *App) { a.Args = []string{"a\x00b"} }},
		{"args", func(a *App) { a.Args = []string{strings.Repeat("a", 3000), strings.Repeat("b", 2000)} }},
		{"resources.vcpu", func(a *App) { a.Resources.VCPU = 0 }},
		{"resources.vcpu", func(a *App) { a.Resources.VCPU = 3 }},
		{"resources.memoryMib", func(a *App) { a.Resources.MemoryMib = 127 }},
		{"resources.memoryMib", func(a *App) { a.Resources.MemoryMib = 4097 }},
		{"healthCheck.kind", func(a *App) { a.HealthCheck.Kind = "boot-completed" }},
		{"healthCheck.path", func(a *App) { a.HealthCheck.Path = "/up" }},
		{"healthCheck.path", func(a *App) { a.HealthCheck = HealthCheck{Kind: "http", Path: "up"} }},
		{"healthCheck.path", func(a *App) { a.HealthCheck = HealthCheck{Kind: "http", Path: "/a b"} }},
		{"hostnames[0]", func(a *App) { a.Hostnames[0].Hostname = "crew.example.com" }},
		{"hostnames[0]", func(a *App) { a.Hostnames[0].Hostname = "bigconfig.space" }},
		{"hostnames[0]", func(a *App) { a.Hostnames[0].Hostname = "Crew.bigconfig.space" }},
		{"hostnames[0]", func(a *App) { a.Hostnames[0].Hostname = "evil.com/.bigconfig.space" }},
		{"hostnames[0]", func(a *App) { a.Hostnames[0].Kind = "other" }},
		{"hostnames[1]", func(a *App) { a.Hostnames = append(a.Hostnames, a.Hostnames[0]) }},
		{"env", func(a *App) { a.Env["home"] = "x" }},
		{"env", func(a *App) { a.Env["HOME"] = "/root" }},
		{"env", func(a *App) { a.Env["PATH"] = "/bin" }},
		{"env", func(a *App) { a.Env["NIBRUN_HTTP_PORT"] = "1" }},
		{"env", func(a *App) { a.Env["DROP_TOKEN"] = "x" }},
		{"env", func(a *App) { a.Env["BIG"] = strings.Repeat("x", 4097) }},
		{"env", func(a *App) { a.Env["NUL"] = "a\x00" }},
		{"env", func(a *App) { a.Env["REF"] = "${NIBRUN_SECRET}" }},
		{"env", func(a *App) {
			for i := 0; i < 63; i++ {
				a.Env[fmt.Sprintf("V%d", i)] = "x"
			}
		}},
		{"secrets[0]", func(a *App) { a.Secrets[0].Name = "NIBRUN_X" }},
		{"secrets[0]", func(a *App) { a.Env[a.Secrets[0].Name] = "dup" }},
		{"secrets[1]", func(a *App) { a.Secrets[1].Name = a.Secrets[0].Name; a.Secrets[1].Parameter = a.Secrets[0].Parameter }},
		{"secrets[0].parameter", func(a *App) { a.Secrets[0].Parameter = "/drop/apps/" + crewID + "/" + a.Secrets[0].Name }},
		{"secrets[0].parameter", func(a *App) { a.Secrets[0].Parameter = "/other/apps/crewdev00000001/" + a.Secrets[0].Name }},
		{"secrets[0].version", func(a *App) { a.Secrets[0].Version = 0 }},
		{"secrets[0]", func(a *App) { a.Secrets[0].Value = "${NIBRUN_TOKEN}" }},
	}
	for _, c := range cases {
		in := loadInput(t, "crew-dev.input.json")
		c.mut(&in.Apps[0])
		out, err := Render(in)
		if err != nil {
			t.Fatalf("%s: whole input refused: %v", c.field, err)
		}
		if len(out.Rejected) != 1 || out.Rejected[0].Field != c.field || out.Rejected[0].Code != CodeInvalidApp {
			t.Fatalf("%s: rejected %v", c.field, out.Rejected)
		}
		msg := out.Rejected[0].Error()
		for _, secret := range []string{"<secret:", "/root", "/bin", strings.Repeat("x", 10)} {
			if strings.Contains(msg, secret) {
				t.Fatalf("%s: error %q leaks a value", c.field, msg)
			}
		}
		if !bytes.Contains(out.Document, []byte(`"instances": []`)) {
			t.Fatalf("%s: rejected app rendered", c.field)
		}
	}
	// The node's allocatable bounds one app.
	in := loadInput(t, "crew-dev.input.json")
	in.Limits.AllocatableMemoryMib = 256
	if out := mustRender(t, in); len(out.Rejected) != 1 || out.Rejected[0].Field != "resources.memoryMib" {
		t.Fatalf("allocatable: %v", out.Rejected)
	}
	in = loadInput(t, "crew-dev.input.json")
	in.Apps[0].DesiredState = "running"
	in.Apps[0].IdleTimeoutMs = 0
	in.Apps[0].Env["URL"] = "https://${NIBRUN_HOSTNAME}:${NIBRUN_HTTP_PORT}/"
	out := mustRender(t, in)
	if len(out.Rejected) != 0 || bytes.Contains(out.Document, []byte("activation")) {
		t.Fatalf("running app: %v\n%s", out.Rejected, out.Document)
	}
}

func TestRenderGuestHTTPPort(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	for _, port := range []int{1, 80, 1023, 1024, 65535} {
		in.Apps[0].HTTPPort = port
		out := mustRender(t, in)
		if len(out.Rejected) != 0 {
			t.Fatalf("port %d rejected: %v", port, out.Rejected)
		}
		var doc document
		if err := json.Unmarshal(out.Document, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Instances) != 1 || doc.Instances[0].Config.HTTPPort != port {
			t.Fatalf("expected one instance configured on port %d", port)
		}
	}
}

func TestCanonicalForm(t *testing.T) {
	out := mustRender(t, loadInput(t, "crew-dev.input.json"))
	doc := out.Document
	if !bytes.HasSuffix(doc, []byte("}\n")) {
		t.Fatal("no trailing newline")
	}
	if bytes.Contains(doc, []byte("\\u003c")) || !bytes.Contains(doc, []byte("Crew <crew@example.com>")) {
		t.Fatal("HTML escaping in the document")
	}
	re, err := canonicalIndent(generic(t, doc))
	if err != nil || !bytes.Equal(re, doc) {
		t.Fatalf("re-canonicalizing changed the document")
	}
	// Formatting and revision do not change the content hash.
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		t.Fatal(err)
	}
	other := bytes.Replace(compact.Bytes(), []byte(out.Revision), []byte("hand-1"), 1)
	if h, err := ContentHash(other); err != nil || h != out.ContentHash {
		t.Fatalf("ContentHash(compact, other revision) = %s, %v", h, err)
	}
}

func TestCrewDeliveryFollowsAppliedInstance(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	in.Apps[0].HTTPPort = 8080
	in.Apps[0].Env = nil
	in.Apps[0].Secrets = nil
	in.Apps[0].HookPort = 8081
	in.Apps[0].ManagedCrew = true
	in.Apps[0].CrewFreshEnrollment = true
	in.Apps[0].AWSRegion = "eu-north-1"
	in.Apps[0].CrewReplicaBucket = "crew-example"
	in.Apps[0].CrewOwnerEmail = "owner+crew@example.test"
	in.Apps[0].SignInEmailEnabled = true
	current := mustRender(t, in)
	golden := filepath.Join("testdata", "crew-managed.golden.json")
	if *update {
		if err := os.WriteFile(golden, current.Document, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil || !bytes.Equal(want, current.Document) {
		t.Fatalf("managed Crew output differs from %s: %v", golden, err)
	}
	instanceOf := func(out *Output) instance {
		t.Helper()
		var doc document
		if err := json.Unmarshal(out.Document, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Instances) != 1 {
			t.Fatalf("missing instance: %+v", out.Rejected)
		}
		return doc.Instances[0]
	}
	inst := instanceOf(current)
	if inst.Config.Hooks == nil || inst.Config.Hooks.Port != 8081 {
		t.Fatal("missing hook listener")
	}
	for name, want := range map[string]string{"AWS_REGION": "eu-north-1", "CREW_DROP_APP_ID": inst.AppID, "CREW_DROP_BUCKET": "crew-example", "POCKETCONTEXT_DOMAIN": in.Apps[0].Hostnames[0].Hostname, "CREW_DROP_BOOTSTRAP": "1"} {
		if got := inst.Config.Command.Environment[name]; got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if inst.SignInEmail == nil || inst.SignInEmail.Recipient != in.Apps[0].CrewOwnerEmail || inst.Config.Command.Environment["DROP_OWNER_EMAIL"] != inst.SignInEmail.Recipient {
		t.Fatal("host and guest owner differ")
	}
	in.Previous = current.Document
	in.Apps[0].CrewOwnerEmail = "Owner <untrusted@example.test>"
	rejected := mustRender(t, in)
	if len(rejected.Rejected) != 1 || !bytes.Equal(rejected.Document, current.Document) {
		t.Fatal("malformed update replaced applied binding")
	}
	in.Apps[0].CrewOwnerEmail = inst.SignInEmail.Recipient
	in.Apps[0].SignInEmailEnabled = false
	disabled := instanceOf(mustRender(t, in))
	if disabled.SignInEmail != nil || disabled.Config.Command.Environment["DROP_OWNER_EMAIL"] != inst.SignInEmail.Recipient {
		t.Fatal("disabled binding or guest owner incorrect")
	}
	in.Apps[0].SignInEmailEnabled = true
	in.Apps[0].DesiredState = "stopped"
	if instanceOf(mustRender(t, in)).SignInEmail != nil {
		t.Fatal("stopped app retained mail")
	}
	in.Apps = nil
	dead := instanceOf(mustRender(t, in))
	if dead.SignInEmail != nil || dead.DesiredState != "stopped" {
		t.Fatal("tombstone retained mail")
	}
}

// TestReplicaSyncInterval: a managed Crew app's sync interval reaches the
// guest as DROP_REPLICA_SYNC_INTERVAL; out of range, or on an app that is not
// managed Crew, the app is rejected alone.
func TestReplicaSyncInterval(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	in.Apps[0].HTTPPort = 8080
	in.Apps[0].Env = nil
	in.Apps[0].Secrets = nil
	in.Apps[0].HookPort = 8081
	in.Apps[0].ManagedCrew = true
	in.Apps[0].AWSRegion = "eu-north-1"
	in.Apps[0].CrewReplicaBucket = "crew-example"
	in.Apps[0].CrewOwnerEmail = "owner+crew@example.test"
	envOf := func(in Input) (map[string]string, int) {
		t.Helper()
		out := mustRender(t, in)
		var doc document
		if err := json.Unmarshal(out.Document, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Instances) != 1 {
			return nil, len(out.Rejected)
		}
		return doc.Instances[0].Config.Command.Environment, len(out.Rejected)
	}
	if env, _ := envOf(in); env["DROP_REPLICA_SYNC_INTERVAL"] != "" {
		t.Fatal("an unset interval reached the guest")
	}
	in.Apps[0].ReplicaSyncSeconds = 10
	if env, _ := envOf(in); env["DROP_REPLICA_SYNC_INTERVAL"] != "10s" {
		t.Fatalf("interval %q", env["DROP_REPLICA_SYNC_INTERVAL"])
	}
	for _, bad := range []int{-1, 301} {
		in.Apps[0].ReplicaSyncSeconds = bad
		if _, rejected := envOf(in); rejected != 1 {
			t.Errorf("%d s accepted", bad)
		}
	}
	in.Apps[0].ReplicaSyncSeconds = 10
	in.Apps[0].ManagedCrew = false
	if _, rejected := envOf(in); rejected != 1 {
		t.Error("an interval on an app that is not managed Crew accepted")
	}
}

func TestHostS3ReplacesWorkloadIdentity(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	in.Apps[0].HTTPPort, in.Apps[0].Env, in.Apps[0].Secrets = 8080, nil, nil
	in.Apps[0].ManagedCrew, in.Apps[0].AWSRegion, in.Apps[0].CrewReplicaBucket = true, "eu-north-1", "crew-example"
	in.Apps[0].CrewOwnerEmail = "owner@example.test"
	in.Apps[0].HostS3 = true
	out := mustRender(t, in)
	var doc document
	if err := json.Unmarshal(out.Document, &doc); err != nil || len(doc.Instances) != 1 {
		t.Fatalf("render: %v %+v", err, out.Rejected)
	}
	inst := doc.Instances[0]
	if inst.HostServices == nil || !inst.HostServices.S3 {
		t.Fatal("host S3 not enabled on the instance")
	}
	env := inst.Config.Command.Environment
	for _, name := range []string{"AWS_WORKLOAD_IDENTITY", "AWS_ROLE_SESSION_NAME"} {
		if _, ok := env[name]; ok {
			t.Fatalf("%s rendered beside host S3", name)
		}
	}
	if env["CREW_DROP_BUCKET"] != "crew-example" || env["AWS_REGION"] != "eu-north-1" {
		t.Fatal("bucket or region missing")
	}
	in.Previous = out.Document
	in.Apps = nil
	var dead document
	_ = json.Unmarshal(mustRender(t, in).Document, &dead)
	if dead.Instances[0].HostServices != nil || dead.Instances[0].DesiredState != "stopped" {
		t.Fatal("tombstone kept host services")
	}
	plain := loadInput(t, "crew-dev.input.json")
	plain.Apps[0].HostS3 = true
	if r := mustRender(t, plain).Rejected; len(r) != 1 || r[0].Field != "hostS3" {
		t.Fatalf("host S3 on an app that is not managed Crew: %+v", r)
	}
}

func TestOnlyExplicitDeletionPurgesKnownVolume(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	first, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	id := in.Apps[0].AppID
	in.Apps = nil
	in.Previous = first.Document
	in.Deleted = []string{id}
	purged, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodePrevious(purged.Document)
	if err != nil || len(doc.Instances) != 0 || len(doc.Volumes) != 1 || doc.Volumes[0].DesiredState != "absent" {
		t.Fatalf("invalid explicit purge: %v", err)
	}
	in.Previous = purged.Document
	in.Deleted = nil
	next, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	assertNeverAbsent(t, next.Document)
}

func TestPrivateRecipientDoesNotExposeOwnerAddress(t *testing.T) {
	app := App{CrewOwnerEmail: "owner@example.test", PrivateOwnerEmail: true, SignInEmailEnabled: true}
	inst := renderInstance(app)
	if inst.SignInEmail == nil || inst.SignInEmail.Recipient != app.CrewOwnerEmail {
		t.Fatal("missing email service")
	}
	if _, ok := renderEnvironment(app)["DROP_OWNER_EMAIL"]; ok {
		t.Fatal("mail capability also granted verified owner email")
	}
	app.SignInEmailEnabled = false
	if inst := renderInstance(app); inst.SignInEmail != nil {
		t.Fatal("undeclared email service")
	}
}

func TestVolumeResizeRequiresMatchingLocalOutcome(t *testing.T) {
	in := loadInput(t, "crew-dev.input.json")
	source := mustRender(t, in)
	v := &in.Apps[0].Volume
	fromBytes := v.SizeBytes
	request := &LocalResize{RequestID: "0123456789abcdef0123456789abcdef", FromBytes: fromBytes}
	in.Previous = source.Document
	v.LocalResize = request
	v.SizeBytes = 8192 * mib
	if len(mustRender(t, in).Rejected) != 1 {
		t.Fatal("accepted unsupported runner")
	}
	in.Limits.LocalVolumeResize = true
	sent := mustRender(t, in)
	if len(sent.Rejected) != 0 {
		t.Fatal(sent.Rejected)
	}
	in.Previous = sent.Document
	if len(mustRender(t, in).Rejected) != 0 {
		t.Fatal("request replay failed")
	}
	v.LocalResize = nil
	v.SizeBytes = fromBytes
	if len(mustRender(t, in).Rejected) != 1 {
		t.Fatal("cleared without receipt")
	}
	in.Limits.ResizeReceipts = map[string]ObservedResize{v.VolumeID: {ResizeReceipt: ResizeReceipt{RequestID: request.RequestID, Outcome: "refused"}, SizeBytes: fromBytes}}
	if len(mustRender(t, in).Rejected) != 0 {
		t.Fatal("confirmed refusal failed to restore old target")
	}
	in.Limits.ResizeReceipts[v.VolumeID] = ObservedResize{ResizeReceipt: ResizeReceipt{RequestID: request.RequestID, Outcome: "applied"}, SizeBytes: 8192 * mib}
	v.SizeBytes = 8192 * mib
	if len(mustRender(t, in).Rejected) != 0 {
		t.Fatal("applied disk cannot finish")
	}
}
