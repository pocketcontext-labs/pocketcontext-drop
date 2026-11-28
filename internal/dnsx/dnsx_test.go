package dnsx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const zone = "0123456789abcdef0123456789abcdef"

func TestCloudflareTouchesOnlyManagedRecords(t *testing.T) {
	const token = "test-token-value"
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || !strings.HasPrefix(r.URL.Path, "/zones/"+zone+"/dns_records") {
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Query().Get("comment.exact") == Managed:
			w.Write([]byte(`{"success":true,"result":[{"id":"1","type":"CNAME","name":"a.apps.test","content":"t.cfargotunnel.com","comment":"drop-managed"},{"id":"2","type":"CNAME","name":"b.apps.test","content":"x","comment":"other"}],"result_info":{"total_pages":1}}`))
		case r.Method == "GET":
			w.Write([]byte(`{"success":true,"result":[{"id":"3","type":"A","name":"` + r.URL.Query().Get("name") + `","content":"1.2.3.4"}],"result_info":{"total_pages":1}}`))
		case r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&created)
			w.Write([]byte(`{"success":true,"result":{}}`))
		default:
			w.Write([]byte(`{"success":true,"result":{}}`))
		}
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "token")
	os.WriteFile(file, []byte(token+"\n"), 0o600)
	cf, err := NewCloudflare(zone, file)
	if err != nil {
		t.Fatal(err)
	}
	cf.Base = srv.URL
	ctx := context.Background()
	records, err := cf.Managed(ctx)
	if err != nil || len(records) != 1 || records[0].Name != "a.apps.test" {
		t.Fatalf("managed %v %v", records, err)
	}
	if taken, err := cf.Taken(ctx, "c.apps.test"); err != nil || !taken {
		t.Fatalf("taken %v %v", taken, err)
	}
	if err := cf.Create(ctx, "c.apps.test", Target("t")); err != nil {
		t.Fatal(err)
	}
	if created["comment"] != Managed || created["proxied"] != true || created["type"] != "CNAME" || created["content"] != "t.cfargotunnel.com" {
		t.Fatalf("created %v", created)
	}
	cf.token = "wrong-token-value"
	if _, err := cf.Managed(ctx); err == nil || strings.Contains(err.Error(), "token-value") {
		t.Fatalf("error %v", err)
	}
	if _, err := NewCloudflare("not-a-zone", file); err == nil {
		t.Fatal("bad zone ID accepted")
	}
}
