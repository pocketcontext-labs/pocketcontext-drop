package hostservices

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func TestUnavailableBudgetPreventsUpstreamExchange(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++; w.WriteHeader(200) }))
	defer server.Close()
	counter, _ := OpenUsage("")
	p := Proxy{Usage: counter, Budget: func(context.Context, dropapi.StorageBudget) error { return errors.New("offline") }}
	for _, op := range []Op{OpHeadObject, OpPutObject, OpListObjectsV2, OpDeleteObject} {
		req, _ := http.NewRequest("GET", server.URL, nil)
		if _, err := p.exchange(req, App{AppID: testApp, DeploymentID: testDep}, op, 1024); err == nil {
			t.Fatal("ungated", op)
		}
	}
	if d, ok := counter.Snapshot(testDep); !ok || d.Counters.BudgetRefusals != 4 {
		t.Fatalf("missing budget denial counter %+v", d)
	}
	if count != 0 {
		t.Fatal("S3 reached while budget unavailable")
	}
}

func TestBudgetMarkerAndBoundedObservation(t *testing.T) {
	var events []dropapi.StorageBudget
	var upstreamMarker string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamMarker = r.Header.Get("User-Agent")
		io.WriteString(w, "fixture")
	}))
	defer server.Close()
	p := Proxy{Budget: func(_ context.Context, in dropapi.StorageBudget) error { events = append(events, in); return nil }}
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("User-Agent", "tenant")
	resp, err := p.exchange(req, App{AppID: testApp, DeploymentID: testDep}, OpGetObject, 7)
	if err != nil {
		t.Fatal(err)
	}
	// A disconnected guest need not have read every byte S3 sent.
	resp.Body.Close()
	if len(events) != 2 || events[0].Action != "reserve" || events[1].Action != "observed" || events[1].Bytes != 7 || events[1].Token != events[0].Token || upstreamMarker != dropapi.StorageMarker+events[0].Token {
		t.Fatalf("reservation lifecycle %+v", events)
	}
}

func TestOneReservationNeverFollowsS3Redirect(t *testing.T) {
	var followed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/original" {
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
			return
		}
		followed.Add(1)
	}))
	defer srv.Close()
	p := Proxy{Upstream: Upstream{Client: srv.Client()}, Budget: func(context.Context, dropapi.StorageBudget) error { return nil }}
	req, _ := http.NewRequest("GET", srv.URL+"/original", nil)
	resp, err := p.exchange(req, App{AppID: testApp, DeploymentID: testDep}, OpGetObject, 65536)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || followed.Load() != 0 {
		t.Fatalf("redirect status %d, target calls %d", resp.StatusCode, followed.Load())
	}
}

func TestBudgetTransportNeverReplaysOnDroppedConnection(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/warm" {
			io.WriteString(w, "ready")
			return
		}
		attempts.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	var grants int
	p := Proxy{Upstream: Upstream{Client: srv.Client()}, Budget: func(_ context.Context, in dropapi.StorageBudget) error {
		if in.Action == "reserve" {
			grants++
		}
		return nil
	}}
	app := App{AppID: testApp, DeploymentID: testDep}
	req, _ := http.NewRequest("GET", srv.URL+"/warm", nil)
	resp, err := p.exchange(req, app, OpGetObject, 65536)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	req, _ = http.NewRequest("GET", srv.URL+"/drop", nil)
	if _, err := p.exchange(req, app, OpGetObject, 65536); err == nil {
		t.Fatal("dropped response succeeded")
	}
	if attempts.Load() != 1 || grants != 2 {
		t.Fatalf("S3 saw %d attempts for the second of %d grants", attempts.Load(), grants)
	}
}

func TestBudgetTransportUsesFreshHTTP1ConnectionOnHTTP2Origin(t *testing.T) {
	peers := make(chan string, 2)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("unexpected S3 protocol %q", r.Proto)
		}
		peers <- r.RemoteAddr
		io.WriteString(w, "one")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	p := Proxy{Upstream: Upstream{Client: srv.Client()}, Budget: func(context.Context, dropapi.StorageBudget) error { return nil }}
	for range 2 {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := p.exchange(req, App{AppID: testApp, DeploymentID: testDep}, OpGetObject, 3)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if <-peers == <-peers {
		t.Fatal("S3 connection reused across grants")
	}
}
