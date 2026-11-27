package hostservices

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
