package dropserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

var readinessClient = &http.Client{
	Timeout:       4 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func crewURLReady(ctx context.Context, domain, slug, hostname string, client *http.Client) bool {
	if !ValidAppDomain(domain) || !slugPattern.MatchString(slug) || hostname != slug+"."+domain {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/api/bundle/auth-mode", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	const limit = 4096
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	var mode struct {
		Mode string `json:"mode"`
	}
	return err == nil && len(body) <= limit && json.Unmarshal(body, &mode) == nil && mode.Mode == "email"
}

func betaURLReady(ctx context.Context, domain, slug, hostname string, client *http.Client) bool {
	if !ValidAppDomain(domain) || !slugPattern.MatchString(slug) || hostname != slug+"."+domain {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+hostname+"/", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}
