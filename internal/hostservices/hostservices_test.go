package hostservices

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testApp    = "d-000000000000001"
	testDep    = "d-000000000000001-dep-1"
	testBucket = "drop-replica"
)

func TestParseRequestAllowsOnlyTheAppsOperations(t *testing.T) {
	p := "/" + testBucket + "/" + testApp
	cases := []struct {
		method, target string
		header         map[string]string
		op             Op
		code           string
	}{
		{"GET", p + "/files/abc?x-id=GetObject", nil, OpGetObject, ""},
		{"HEAD", p + "/files/abc", nil, OpHeadObject, ""},
		{"PUT", p + "/files/abc?x-id=PutObject", nil, OpPutObject, ""},
		{"DELETE", p + "/crew-drop/x", nil, OpDeleteObject, ""},
		{"POST", "/" + testBucket + "?delete", nil, OpDeleteObjects, ""},
		{"GET", "/" + testBucket + "?list-type=2&prefix=" + testApp + "%2Fcrew-drop%2F&max-keys=1", nil, OpListObjectsV2, ""},
		{"POST", p + "/s/1.ltx?uploads", nil, OpCreateUpload, ""},
		{"PUT", p + "/s/1.ltx?partNumber=2&uploadId=u1&x-id=UploadPart", nil, OpUploadPart, ""},
		{"POST", p + "/s/1.ltx?uploadId=u1", nil, OpCompleteUpload, ""},
		{"DELETE", p + "/s/1.ltx?uploadId=u1", nil, OpAbortUpload, ""},
		// Refused.
		{"GET", "/" + testBucket + "/d-000000000000002/files/abc", nil, "", "AccessDenied"},
		{"GET", "/other-bucket/" + testApp + "/x", nil, "", "AccessDenied"},
		{"GET", p + "/../d-000000000000002/x", nil, "", "AccessDenied"},
		{"GET", p + "/a/%2e%2e/b", nil, "", "AccessDenied"},
		{"GET", p + "//x", nil, "", "AccessDenied"},
		{"GET", p + "/x%0a", nil, "", "AccessDenied"},
		{"GET", "/" + testBucket + "/" + testApp + "x/y", nil, "", "AccessDenied"},
		{"GET", "/" + testBucket + "?list-type=2&prefix=d-000000000000002%2F", nil, "", "AccessDenied"},
		{"GET", "/" + testBucket + "?list-type=2", nil, "", "AccessDenied"},
		{"GET", "/" + testBucket + "?list-type=2&prefix=" + testApp + "%2F&start-after=d-000000000000002", nil, "", "AccessDenied"},
		{"GET", p + "/x?acl", nil, "", "NotImplemented"},
		{"GET", p + "/x?versionId=abc", nil, "", "NotImplemented"},
		{"DELETE", p + "/x?versionId=abc", nil, "", "NotImplemented"},
		{"PUT", p + "/x?tagging", nil, "", "NotImplemented"},
		{"PUT", p + "/x", map[string]string{"X-Amz-Copy-Source": "/drop-replica/d-000000000000002/x"}, "", "NotImplemented"},
		{"GET", "/" + testBucket + "?list-type=1", nil, "", "NotImplemented"},
		{"GET", "/" + testBucket, nil, "", "NotImplemented"},
		{"PUT", "/" + testBucket, nil, "", "NotImplemented"},
		{"POST", p + "/x?select&select-type=2", nil, "", "NotImplemented"},
		{"PATCH", p + "/x", nil, "", "MethodNotAllowed"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:9000"+c.target, nil)
		for k, v := range c.header {
			r.Header.Set(k, v)
		}
		req, e := parseRequest(r, testBucket, testApp+"/")
		switch {
		case c.code == "" && (e != nil || req.op != c.op):
			t.Errorf("%s %s: got %v %v, want %s", c.method, c.target, req, e, c.op)
		case c.code != "" && (e == nil || e.code != c.code):
			t.Errorf("%s %s: got %v %v, want %s", c.method, c.target, req, e, c.code)
		}
	}
}

func TestDeleteListStaysUnderThePrefixAndNeverNamesAVersion(t *testing.T) {
	ok := `<Delete><Object><Key>` + testApp + `/a</Key></Object><Object><Key>` + testApp + `/b</Key></Object><Quiet>true</Quiet></Delete>`
	if _, keys, e := readDeleteKeys(strings.NewReader(ok), testApp+"/"); e != nil || len(keys) != 2 {
		t.Fatalf("valid list: %v %v", keys, e)
	}
	for _, body := range []string{
		`<Delete><Object><Key>d-000000000000002/a</Key></Object></Delete>`,
		`<Delete><Object><Key>` + testApp + `/a</Key><VersionId>v1</VersionId></Object></Delete>`,
		`<Delete></Delete>`,
		`not xml`,
	} {
		if _, _, e := readDeleteKeys(strings.NewReader(body), testApp+"/"); e == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestSignedChunksAreDecodedToTheirData(t *testing.T) {
	body := "5;chunk-signature=aaaa\r\nhello\r\n6;chunk-signature=bbbb\r\n world\r\n0;chunk-signature=cccc\r\n\r\n"
	got, err := io.ReadAll(&exactReader{r: newChunkDecoder(strings.NewReader(body)), n: 11})
	if err != nil || string(got) != "hello world" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := io.ReadAll(&exactReader{r: newChunkDecoder(strings.NewReader("5;x\r\nhel")), n: 5}); err == nil {
		t.Fatal("truncated chunk accepted")
	}
	r := httptest.NewRequest("PUT", "/b/k", strings.NewReader(body))
	r.Header.Set("X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER")
	if _, e := preparePayload(r); e == nil {
		t.Fatal("signed trailer accepted")
	}
}

func TestUsageSequenceGoesUpOnlyWithChangesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u, err := OpenUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	ref := u.deployment(testApp, testDep)
	ref.request(ClassWrite, 100)
	ref.reject()
	u.bytesOut(testApp, testDep, 40, now)
	if err := u.Flush(now); err != nil {
		t.Fatal(err)
	}
	if err := u.Flush(now); err != nil {
		t.Fatal(err)
	}
	f, err := ReadUsageFile(path)
	if err != nil || len(f.Deployments) != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	d := f.Deployments[0]
	if d.Seq != 1 || d.Counters.WriteRequests != 1 || d.Counters.RejectedRequests != 1 || d.Counters.BytesIn != 100 || d.Counters.BytesOut != 40 {
		t.Fatalf("counters %+v", d)
	}
	again, err := OpenUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	again.deployment(testApp, testDep).request(ClassRead, 0)
	_ = again.Flush(now)
	f, _ = ReadUsageFile(path)
	if f.Deployments[0].Seq != 2 || f.Deployments[0].Counters.ReadRequests != 1 || f.Deployments[0].Counters.WriteRequests != 1 {
		t.Fatalf("after restart %+v", f.Deployments[0])
	}
	if again.monthOut(testApp, now) != 40 || again.monthOut(testApp, now.AddDate(0, 1, 0)) != 0 {
		t.Fatal("monthly download total")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("usage file mode %v", st.Mode())
	}
}

func TestRegistryNeedsBothFilesToAgree(t *testing.T) {
	dir := t.TempDir()
	desired, apps := filepath.Join(dir, "desired.json"), filepath.Join(dir, "host-services.json")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	instance := func(state, dep string, s3 bool) string {
		hs := ""
		if s3 {
			hs = `,"hostServices":{"s3":true}`
		}
		return `{"instances":[{"appId":"` + testApp + `","deploymentId":"` + dep + `","desiredState":"` + state + `"` + hs + `}]}`
	}
	if err := WriteAppsFile(apps, []AppEntry{{AppID: testApp, DeploymentID: testDep, Bucket: testBucket, Region: "eu-north-1", Limits: FileLimits{RPS: 5}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		doc  string
		dep  string
		want bool
	}{
		{instance("on-request", testDep, true), testDep, true},
		{instance("on-request", testDep, true), testApp + "-dep-2", false},
		{instance("on-request", testDep, false), testDep, false},
		{instance("stopped", testDep, true), testDep, false},
		{instance("running", testApp+"-dep-2", true), testDep, false},
		{"not json", testDep, false},
	} {
		write(desired, c.doc)
		r := &Registry{DesiredPath: desired, AppsPath: apps}
		app, ok := r.Lookup(testApp, c.dep)
		if ok != c.want || (ok && (app.Bucket != testBucket || app.Limits.RPS != 5)) {
			t.Errorf("%s %s: %v %+v", c.doc, c.dep, ok, app)
		}
	}
	write(desired, instance("running", testDep, true))
	os.Remove(apps)
	if _, ok := (&Registry{DesiredPath: desired, AppsPath: apps}).Lookup(testApp, testDep); ok {
		t.Fatal("served without the agent's apps file")
	}
}

func TestErrorsAreS3Documents(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, refuse(http.StatusServiceUnavailable, "SlowDown", "Please reduce your request rate."))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), "<Code>SlowDown</Code>") {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
}
