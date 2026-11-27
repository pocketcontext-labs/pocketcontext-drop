package hostservices

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

type Budget func(context.Context, dropapi.StorageBudget) error

// A grant pays for exactly one physical S3 exchange. Go's pooled transport
// may otherwise replay a GET or HEAD after a reused connection drops before
// response headers arrive. Fresh HTTP/1.1 connections prevent such replays.
// Keep endpoint TLS roots, dialers and timeout configuration from the service.
func (p *Proxy) upstreamClient() *http.Client {
	p.upstreamOnce.Do(func() {
		base := p.Upstream.Client
		if base == nil {
			base = http.DefaultClient
		}
		rt := base.Transport
		if rt == nil {
			rt = http.DefaultTransport
		}
		if standard, ok := rt.(*http.Transport); ok {
			tr := standard.Clone()
			tr.DisableKeepAlives = true
			tr.ForceAttemptHTTP2 = false
			tr.Protocols = new(http.Protocols)
			tr.Protocols.SetHTTP1(true)
			// Clone can inherit an explicit HTTP/2 ALPN advertisement.
			if tr.TLSClientConfig != nil {
				tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
			}
			rt = tr
		}
		// Injected transports for offline tests perform a single RoundTrip.
		// Redirect policy belongs to the funded exchange in every case.
		p.upstreamHTTP = &http.Client{Transport: rt, Timeout: base.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	})
	return p.upstreamHTTP
}

// exchange reserves a request and its maximum response bytes before S3 sees
// it. Quota inventory calls use the same path. The node supplies the marker,
// which is visible in provider access logs, never the guest.
func (p *Proxy) exchange(out *http.Request, app App, op Op, limit int64) (*http.Response, error) {
	if p.Budget == nil {
		return nil, errors.New("storage budget service unavailable")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	grant := dropapi.StorageBudget{Schema: dropapi.StorageBudgetSchema, Action: "reserve", Token: hex.EncodeToString(id[:]), App: app.AppID, Deployment: app.DeploymentID, Class: op.Class(), Bytes: limit}
	if err := p.Budget(out.Context(), grant); err != nil {
		p.Usage.deployment(app.AppID, app.DeploymentID).update(func(c *Counters) { c.BudgetRefusals++ })
		return nil, err
	}
	out.Header.Set("User-Agent", dropapi.StorageMarker+grant.Token)
	resp, err := p.upstreamClient().Do(out)
	if err != nil {
		// Ambiguous delivery leaves the hold for provider reconciliation.
		return nil, err
	}
	if out.Method != http.MethodHead && resp.ContentLength > limit {
		resp.Body.Close()
		return nil, errors.New("S3 response exceeded its reserved budget")
	}
	expected := resp.ContentLength
	if out.Method == http.MethodHead {
		expected = 0
	}
	resp.Body = &budgetBody{ReadCloser: resp.Body, remaining: limit, expected: expected, observe: func(n int64) {
		grant.Action = "observed"
		grant.Bytes = n
		ctx, cancel := context.WithTimeout(context.WithoutCancel(out.Context()), 5*time.Second)
		defer cancel()
		if err := p.Budget(ctx, grant); err != nil {
			p.logf("host services: %s: storage usage awaits provider settlement", app.AppID)
		}
	}}
	return resp, nil
}

type budgetBody struct {
	io.ReadCloser
	remaining, read, expected int64
	complete                  bool
	observe                   func(int64)
}

func (b *budgetBody) Read(p []byte) (int, error) {
	// Permit a single EOF check at the limit. Excess bytes never reach the guest.
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.complete = true
	}
	if int64(n) > b.remaining {
		return 0, errors.New("S3 response exceeded its reserved budget")
	}
	b.remaining -= int64(n)
	b.read += int64(n)
	return n, err
}
func (b *budgetBody) Close() error {
	err := b.ReadCloser.Close()
	if b.observe != nil {
		// Known lengths bound provider egress even when the guest disconnects.
		// With unknown length retain the full reservation until S3 reports.
		if b.expected >= 0 {
			b.observe(b.expected)
		} else if b.complete {
			b.observe(b.read)
		}
		b.observe = nil
	}
	return err
}
