package dropserver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/droptest"
)

// newApp creates an app with an account's token and returns its id.
func newApp(t *testing.T, h *droptest.Hosted, token, slug string) string {
	t.Helper()
	return appOf(t, h.MustOp(token, "app.create", map[string]any{"name": slug, "slug": slug}))["id"].(string)
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// uploadLink asks for an upload link and returns its path, checking the
// shape of the answer.
func uploadLink(t *testing.T, h *droptest.Hosted, token, app string, extra map[string]any) string {
	t.Helper()
	input := map[string]any{"app": app}
	for k, v := range extra {
		input[k] = v
	}
	link := h.MustOp(token, "release.upload_link", input)
	path, _ := link["path"].(string)
	if !strings.HasPrefix(path, dropapi.UploadRoutePrefix) || !dropapi.UploadTokenPattern.MatchString(strings.TrimPrefix(path, dropapi.UploadRoutePrefix)) {
		t.Fatalf("link path %q", path)
	}
	if link["url"] != "https://"+droptest.Domain+path || link["method"] != "PUT" || num(link["max_bytes"]) < 1<<20 ||
		!strings.Contains(link["example"].(string), "curl -fT FILE '"+link["url"].(string)+"'") {
		t.Fatalf("link %v", link)
	}
	expires, err := time.Parse(time.RFC3339, link["expires_at"].(string))
	if err != nil || expires.Before(time.Now().Add(14*time.Minute)) || expires.After(time.Now().Add(16*time.Minute)) {
		t.Fatalf("expires_at %v: %v", link["expires_at"], err)
	}
	return path
}

type uploadAnswer struct {
	Status int
	Result dropapi.UploadResult
	Error  dropapi.Error
	Raw    string
}

func upload(t *testing.T, h *droptest.Hosted, path string, body []byte) uploadAnswer {
	t.Helper()
	w := h.Upload(path, body)
	out := uploadAnswer{Status: w.Code, Raw: w.Body.String()}
	target := any(&out.Result)
	if w.Code != http.StatusOK {
		target = &out.Error
	}
	if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("upload answer %d %s: %v", w.Code, w.Body, err)
	}
	return out
}

func wantUploadError(t *testing.T, a uploadAnswer, status int, code string) {
	t.Helper()
	if a.Status != status || a.Error.Code != code || a.Error.Message == "" {
		t.Fatalf("want %d %s, got %d %s", status, code, a.Status, a.Raw)
	}
}

// TestReleaseUploadAndDeploy: a link takes one upload, the bytes land at
// releases/<sha256> in the store, a second upload of the same bytes reuses
// the release, activate and release.deploy set the current release and bump
// the deployment, and neither the token nor the URL is kept anywhere.
func TestReleaseUploadAndDeploy(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	user, token := h.User("user@example.test", "user")
	app := newApp(t, h, token, "hello")
	empty := h.MustOp(token, "release.list", map[string]any{"app": "hello"})
	if len(empty["releases"].([]any)) != 0 || empty["current_release"] != "" {
		t.Fatalf("new app releases %v", empty)
	}

	if link := h.MustOp(token, "release.upload_link", map[string]any{"app": "hello"}); num(link["max_bytes"]) != dropapi.MaxReleaseBytes {
		t.Fatalf("max_bytes %v", link["max_bytes"])
	}
	path := uploadLink(t, h, token, "hello", map[string]any{"note": "  first build  "})
	first := droptest.ELF(4096, 1)
	a := upload(t, h, path, first)
	if a.Status != 200 || !a.Result.Created || a.Result.Deployed || a.Result.Release.SHA256 != sha(first) ||
		a.Result.Release.S3Key != "releases/"+sha(first) || a.Result.Release.SizeBytes != 4096 || a.Result.Release.Note != "first build" ||
		a.Result.Release.App != app || a.Result.Release.CreatedBy != user.Id || a.Result.Release.Current || a.Result.DeploymentID != "d-"+app+"-dep-1" {
		t.Fatalf("first upload %d %s", a.Status, a.Raw)
	}
	info, err := h.Releases.Head(context.Background(), "releases/"+sha(first))
	if err != nil || info.Size != 4096 || info.SHA256 != sha(first) {
		t.Fatalf("store head %+v %v", info, err)
	}
	wantUploadError(t, upload(t, h, path, first), 404, dropapi.CodeUploadLinkInvalid)

	// The same bytes again: the app's release is reused, the link still used.
	again := upload(t, h, uploadLink(t, h, token, "hello", nil), first)
	if again.Status != 200 || again.Result.Created || again.Result.Release.ID != a.Result.Release.ID {
		t.Fatalf("second upload %s", again.Raw)
	}
	// Another app of the same account with the same bytes gets its own row,
	// sharing the stored object.
	other := newApp(t, h, token, "hello-two")
	shared := upload(t, h, uploadLink(t, h, token, "hello-two", nil), first)
	if shared.Status != 200 || !shared.Result.Created || shared.Result.Release.App != other || shared.Result.Release.S3Key != a.Result.Release.S3Key {
		t.Fatalf("shared upload %s", shared.Raw)
	}

	for _, e := range events(t, h, "release.upload") {
		if strings.Contains(e.GetString("detail"), `"stored"`) {
			t.Fatal("upload audit contains global object status")
		}
	}
	// activate deploys the new release.
	second := droptest.ELF(8192, 2)
	activated := upload(t, h, uploadLink(t, h, token, "hello", map[string]any{"activate": true}), second)
	if activated.Status != 200 || !activated.Result.Created || !activated.Result.Deployed || !activated.Result.Release.Current ||
		activated.Result.DeploymentID != "d-"+app+"-dep-2" {
		t.Fatalf("activated upload %s", activated.Raw)
	}
	got := appOf(t, h.MustOp(token, "app.get", map[string]any{"app": "hello"}))
	if got["current_release"] != activated.Result.Release.ID || num(got["deployment_seq"]) != 2 {
		t.Fatalf("app after activate %v", got)
	}
	list := h.MustOp(token, "release.list", map[string]any{"app": "hello"})
	releases := list["releases"].([]any)
	if len(releases) != 2 || list["current_release"] != activated.Result.Release.ID ||
		releases[0].(map[string]any)["id"] != activated.Result.Release.ID || releases[0].(map[string]any)["current"] != true ||
		releases[1].(map[string]any)["current"] != false {
		t.Fatalf("release list %v", list)
	}

	// release.deploy back to the first release bumps; deploying it again does not.
	back := h.MustOp(token, "release.deploy", map[string]any{"app": "hello", "release": a.Result.Release.ID})
	if back["deployment_bumped"] != true || back["deployment_id"] != "d-"+app+"-dep-3" || appOf(t, back)["current_release"] != a.Result.Release.ID {
		t.Fatalf("deploy %v", back)
	}
	same := h.MustOp(token, "release.deploy", map[string]any{"app": "hello", "release": a.Result.Release.ID})
	if same["deployment_bumped"] != false || same["deployment_id"] != "d-"+app+"-dep-3" {
		t.Fatalf("deploy again %v", same)
	}
	// A release of another app, or no release, is a 404.
	wantError(t, h.Op(token, "release.deploy", map[string]any{"app": "hello", "release": shared.Result.Release.ID}), 404, dropapi.CodeNotFound)
	wantError(t, h.Op(token, "release.deploy", map[string]any{"app": "hello", "release": "nope"}), 404, dropapi.CodeNotFound)

	// Events: links (without the token), uploads (by the link's account) and deploys.
	if e := events(t, h, "release.upload_link"); len(e) != 5 || e[0].GetString("actor") != user.Id || e[0].GetString("app") != app {
		t.Fatalf("link events %v", e)
	}
	uploads := events(t, h, "release.upload")
	if len(uploads) != 4 || uploads[0].GetString("actor") != user.Id || uploads[0].GetString("app") != app {
		t.Fatalf("upload events %v", uploads)
	}
	var detail map[string]any
	if err := uploads[3].UnmarshalJSONField("detail", &detail); err != nil || detail["deployed"] != true || detail["sha256"] != sha(second) {
		t.Fatalf("upload event detail %v %v", detail, err)
	}
	if e := events(t, h, "release.deploy"); len(e) != 2 {
		t.Fatalf("deploy events %v", e)
	}
	secretPart := strings.TrimPrefix(path, dropapi.UploadRoutePrefix)
	if strings.Contains(dumpTables(t, h.App.DB()), secretPart) {
		t.Fatal("an upload token is stored")
	}
	// Replaying a link request never returns the token again.
	h.MustOp(token, "release.upload_link", map[string]any{"app": "hello"})
	r := h.Op(token, "release.upload_link", map[string]any{"app": "hello"}, "link-1")
	if r.Status != 200 {
		t.Fatalf("keyed link %s", r.Raw)
	}
	wantError(t, h.Op(token, "release.upload_link", map[string]any{"app": "hello"}, "link-1"), 409, dropapi.CodeIdempotencyNoResult)
}

// TestUploadRefusals: what is not a linux/amd64 executable, too large, empty
// or sent to a bad, expired or used link is refused, and a refused body
// leaves the link usable.
func TestUploadRefusals(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{ReleaseMaxBytes: 1 << 20})
	_, token := h.User("user@example.test", "user")
	newApp(t, h, token, "hello")
	path := uploadLink(t, h, token, "hello", nil)
	if link := h.MustOp(token, "release.upload_link", map[string]any{"app": "hello"}); num(link["max_bytes"]) != 1<<20 {
		t.Fatalf("max_bytes %v", link["max_bytes"])
	}
	arm := droptest.ELF(4096, 3)
	arm[18] = 0xB7
	object := droptest.ELF(4096, 4)
	object[16] = 1
	bigEndian := droptest.ELF(4096, 5)
	bigEndian[5] = 2
	for name, body := range map[string][]byte{
		"text":              []byte("#!/bin/sh\necho hello\n"),
		"arm64":             arm,
		"object":            object,
		"big-endian":        bigEndian,
		"short":             droptest.ELF(64, 6)[:40],
		"header only":       droptest.ELF(4096, 6)[:64],
		"truncated segment": droptest.ELF(4096, 6)[:3000],
		"32-bit":            append([]byte("\x7fELF\x01"), make([]byte, 100)...),
		"not-at-all":        bytes.Repeat([]byte{0}, 200),
		"elf-in-body":       append([]byte("prefix"), droptest.ELF(200, 7)...),
	} {
		a := upload(t, h, path, body)
		wantUploadError(t, a, 400, dropapi.CodeInvalidRelease)
		if name == "arm64" && !strings.Contains(a.Error.Message, "arm64") {
			t.Errorf("arm64 message %q", a.Error.Message)
		}
	}
	wantUploadError(t, upload(t, h, path, nil), 400, dropapi.CodeInvalidRelease)
	big := droptest.ELF(1<<20+1, 8)
	wantUploadError(t, upload(t, h, path, big), 413, dropapi.CodeReleaseTooLarge)
	if _, err := h.Releases.Head(context.Background(), "releases/"+sha(big)); !errors.Is(err, awsx.ErrNotFound) {
		t.Fatalf("a refused upload was stored: %v", err)
	}
	// The link still works for a good body exactly at the cap.
	if a := upload(t, h, path, droptest.ELF(1<<20, 9)); a.Status != 200 {
		t.Fatalf("upload at the cap %s", a.Raw)
	}

	// Malformed tokens never reach the handler; unknown ones get the same 404.
	for _, p := range []string{dropapi.UploadRoutePrefix + "short", dropapi.UploadRoutePrefix + strings.Repeat("a", 31) + "-"} {
		if w := h.Upload(p, droptest.ELF(100, 1)); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	wantUploadError(t, upload(t, h, dropapi.UploadRoutePrefix+strings.Repeat("a", 32), droptest.ELF(100, 1)), 404, dropapi.CodeUploadLinkInvalid)

	// An expired link.
	expired := uploadLink(t, h, token, "hello", nil)
	setLinkField(t, h, expired, "expires_at", time.Now().Add(-time.Second).UTC())
	wantUploadError(t, upload(t, h, expired, droptest.ELF(100, 10)), 404, dropapi.CodeUploadLinkInvalid)

	// A link of an archived app, or of an account suspended since.
	archived := uploadLink(t, h, token, "hello", nil)
	h.MustOp(token, "app.archive", map[string]any{"app": "hello"})
	wantUploadError(t, upload(t, h, archived, droptest.ELF(100, 11)), 409, dropapi.CodeAppArchived)
	wantError(t, h.Op(token, "release.upload_link", map[string]any{"app": "hello"}), 409, dropapi.CodeAppArchived)
	h.MustOp(token, "app.restore", map[string]any{"app": "hello"})
	suspendedUser, suspendedToken := h.User("later@example.test", "user")
	newApp(t, h, suspendedToken, "later")
	suspendedLink := uploadLink(t, h, suspendedToken, "later", nil)
	h.MustOp(h.Token(h.Owner()), "user.set", map[string]any{"user": suspendedUser.Id, "suspended": true})
	wantUploadError(t, upload(t, h, suspendedLink, droptest.ELF(100, 12)), 404, dropapi.CodeUploadLinkInvalid)

	// Input checks of the operations.
	wantError(t, h.Op(token, "release.upload_link", map[string]any{"app": "hello", "note": strings.Repeat("n", 501)}), 400, dropapi.CodeInvalidInput)
	wantError(t, h.Op(token, "release.upload_link", map[string]any{"app": "hello", "note": "two\nlines"}), 400, dropapi.CodeInvalidInput)
	wantError(t, h.Op(token, "release.upload_link", map[string]any{"app": "hello", "file": "x"}), 400, dropapi.CodeInvalidInput)
	wantError(t, h.Op(token, "release.list", map[string]any{"app": "hello", "limit": 501}), 400, dropapi.CodeInvalidInput)
}

func setLinkField(t *testing.T, h *droptest.Hosted, path, field string, value any) {
	t.Helper()
	sum := sha256.Sum256([]byte(strings.TrimPrefix(path, dropapi.UploadRoutePrefix)))
	link, err := h.App.FindFirstRecordByData("upload_links", "token_hash", hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	link.Set(field, value)
	if err := h.App.Save(link); err != nil {
		t.Fatal(err)
	}
}

// TestUploadAbovePocketBaseBodyLimit: the upload route lifts PocketBase's
// global 32 MB body limit up to the 90 MiB release cap.
func TestUploadAbovePocketBaseBodyLimit(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("user@example.test", "user")
	newApp(t, h, token, "hello")
	body := droptest.ELF(40<<20, 1)
	if a := upload(t, h, uploadLink(t, h, token, "hello", nil), body); a.Status != 200 || a.Result.Release.SizeBytes != 40<<20 {
		t.Fatalf("40 MiB upload: %d %.300s", a.Status, a.Raw)
	}
}

// TestReleaseIsolation: another account's app is a 404 for every release
// operation; admins list any app's releases but change only their own.
func TestReleaseIsolation(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, alice := h.User("alice@example.test", "user")
	_, bob := h.User("bob@example.test", "user")
	admin := h.Token(h.Owner())
	newApp(t, h, alice, "alice-app")
	a := upload(t, h, uploadLink(t, h, alice, "alice-app", nil), droptest.ELF(1000, 1))
	if a.Status != 200 {
		t.Fatal(a.Raw)
	}
	for _, c := range []struct {
		op    string
		input map[string]any
	}{
		{"release.list", map[string]any{"app": "alice-app"}},
		{"release.upload_link", map[string]any{"app": "alice-app"}},
		{"release.deploy", map[string]any{"app": "alice-app", "release": a.Result.Release.ID}},
	} {
		wantError(t, h.Op(bob, c.op, c.input), 404, dropapi.CodeNotFound)
	}
	if list := h.MustOp(admin, "release.list", map[string]any{"app": "alice-app"}); len(list["releases"].([]any)) != 1 {
		t.Fatalf("admin list %v", list)
	}
	wantError(t, h.Op(admin, "release.upload_link", map[string]any{"app": "alice-app"}), 403, dropapi.CodeForbidden)
	wantError(t, h.Op(admin, "release.deploy", map[string]any{"app": "alice-app", "release": a.Result.Release.ID}), 403, dropapi.CodeForbidden)
	// Bob cannot deploy Alice's release on his own app either.
	newApp(t, h, bob, "bob-app")
	wantError(t, h.Op(bob, "release.deploy", map[string]any{"app": "bob-app", "release": a.Result.Release.ID}), 404, dropapi.CodeNotFound)
	// Release records are not public: the operations are the way in.
	if w := h.Request("GET", "/api/collections/releases/records", alice, ""); w.Code != 404 {
		t.Fatalf("releases records: %d", w.Code)
	}
}

// TestUploadIsSingleUseUnderConcurrency: parallel uploads to one link give
// exactly one release.
func TestUploadIsSingleUseUnderConcurrency(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	_, token := h.User("user@example.test", "user")
	newApp(t, h, token, "hello")
	path := uploadLink(t, h, token, "hello", nil)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = h.Upload(path, droptest.ELF(1<<20, byte(i))).Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 404, 409:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if n, _ := h.App.CountRecords("releases"); ok != 1 || n != 1 {
		t.Fatalf("codes %v, %d releases", codes, n)
	}
}

// TestUploadRateLimit: one client address gets a few attempts at once.
func TestUploadRateLimit(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{})
	limited := 0
	for range 10 {
		r := httptest.NewRequest(http.MethodPut, "https://"+droptest.Domain+dropapi.UploadRoutePrefix+strings.Repeat("b", 32), bytes.NewReader([]byte("x")))
		r.TLS, r.Host, r.RemoteAddr = nil, droptest.Domain, "203.0.113.9:5000"
		w := httptest.NewRecorder()
		h.Server.Public().ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests {
			limited++
			if w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), dropapi.CodeRateLimited) {
				t.Fatalf("429 answer %v %s", w.Header(), w.Body)
			}
		} else if w.Code != 404 {
			t.Fatalf("status %d", w.Code)
		}
	}
	if limited != 4 {
		t.Fatalf("%d of 10 limited, want 4", limited)
	}
}

// failingBlobs refuses every upload.
type failingBlobs struct{}

func (failingBlobs) PutIfAbsent(context.Context, string, io.ReadSeeker, int64, string) (bool, error) {
	return false, errors.New("store refused")
}
func (failingBlobs) Head(context.Context, string) (awsx.BlobInfo, error) {
	return awsx.BlobInfo{}, awsx.ErrNotFound
}

// TestReleaseStoreFailures: a failing store answers 502 and keeps the link;
// a server without a store refuses links with 503.
func TestReleaseStoreFailures(t *testing.T) {
	h := droptest.NewHosted(t, droptest.Options{Releases: failingBlobs{}})
	_, token := h.User("user@example.test", "user")
	newApp(t, h, token, "hello")
	path := uploadLink(t, h, token, "hello", nil)
	wantUploadError(t, upload(t, h, path, droptest.ELF(100, 1)), 502, dropapi.CodeReleaseStoreFailed)
	if n, _ := h.App.CountRecords("releases"); n != 0 {
		t.Fatal("a release was recorded")
	}
	wantUploadError(t, upload(t, h, path, droptest.ELF(100, 1)), 502, dropapi.CodeReleaseStoreFailed)

	none := droptest.NewHosted(t, droptest.Options{NoReleases: true})
	_, token = none.User("user@example.test", "user")
	newApp(t, none, token, "hello")
	wantError(t, none.Op(token, "release.upload_link", map[string]any{"app": "hello"}), 503, dropapi.CodeReleasesUnavailable)
	if list := none.MustOp(token, "release.list", map[string]any{"app": "hello"}); len(list["releases"].([]any)) != 0 {
		t.Fatalf("list %v", list)
	}
}
