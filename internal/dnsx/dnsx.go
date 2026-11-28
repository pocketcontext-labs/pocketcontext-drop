// Package dnsx manages the per-app DNS records that point app hostnames at
// their node's Cloudflare Tunnel. It touches only CNAME records that carry
// the Managed comment: every other record in the zone (a wildcard, the
// control plane's own name) is left alone, and a name that already has an
// unmanaged record is never taken over.
package dnsx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Managed is the comment on every record Drop owns.
const Managed = "drop-managed"

// Target is the CNAME content that routes a hostname to a tunnel.
func Target(tunnelID string) string { return tunnelID + ".cfargotunnel.com" }

// Record is one managed CNAME.
type Record struct {
	ID      string
	Name    string
	Content string
}

// Provider is the zone's DNS API, or a fake in tests.
type Provider interface {
	// Managed lists the zone's CNAME records that carry the Managed comment.
	Managed(ctx context.Context) ([]Record, error)
	// Taken reports whether name has any record that Drop does not manage.
	Taken(ctx context.Context, name string) (bool, error)
	Create(ctx context.Context, name, content string) error
	Update(ctx context.Context, id, content string) error
	Delete(ctx context.Context, id string) error
}

var zoneIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Cloudflare is the Cloudflare v4 DNS API for one zone.
type Cloudflare struct {
	ZoneID string
	token  string
	Base   string // https://api.cloudflare.com/client/v4 when empty
	Client *http.Client
}

// NewCloudflare reads the API token from tokenFile. The token needs only
// Zone DNS Edit on the zone.
func NewCloudflare(zoneID, tokenFile string) (*Cloudflare, error) {
	if !zoneIDPattern.MatchString(zoneID) {
		return nil, fmt.Errorf("the Cloudflare zone ID is 32 lower-case hex characters")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("could not read the Cloudflare API token file")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("the Cloudflare API token file must hold one token")
	}
	return &Cloudflare{ZoneID: zoneID, token: token, Client: &http.Client{Timeout: 30 * time.Second}}, nil
}

type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Comment string `json:"comment"`
}

type cfAnswer struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// call never puts the token in an error.
func (c *Cloudflare) call(ctx context.Context, method, path string, body any) (*cfAnswer, error) {
	base := c.Base
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/zones/"+c.ZoneID+path, in)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare %s: request failed", method)
	}
	defer res.Body.Close()
	var out cfAnswer
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out); err != nil || !out.Success {
		msg := ""
		if len(out.Errors) > 0 {
			msg = fmt.Sprintf(": %d %s", out.Errors[0].Code, out.Errors[0].Message)
		}
		return nil, fmt.Errorf("cloudflare %s: HTTP %d%s", method, res.StatusCode, msg)
	}
	return &out, nil
}

func (c *Cloudflare) list(ctx context.Context, query url.Values) ([]cfRecord, error) {
	var all []cfRecord
	query.Set("per_page", "1000")
	for page := 1; ; page++ {
		query.Set("page", fmt.Sprint(page))
		out, err := c.call(ctx, http.MethodGet, "/dns_records?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var records []cfRecord
		if err := json.Unmarshal(out.Result, &records); err != nil {
			return nil, err
		}
		all = append(all, records...)
		if page >= out.ResultInfo.TotalPages {
			return all, nil
		}
	}
}

func (c *Cloudflare) Managed(ctx context.Context) ([]Record, error) {
	records, err := c.list(ctx, url.Values{"type": {"CNAME"}, "comment.exact": {Managed}})
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range records {
		if r.Type == "CNAME" && r.Comment == Managed {
			out = append(out, Record{ID: r.ID, Name: r.Name, Content: r.Content})
		}
	}
	return out, nil
}

func (c *Cloudflare) Taken(ctx context.Context, name string) (bool, error) {
	records, err := c.list(ctx, url.Values{"name": {name}})
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.Name == name && !(r.Type == "CNAME" && r.Comment == Managed) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Cloudflare) Create(ctx context.Context, name, content string) error {
	_, err := c.call(ctx, http.MethodPost, "/dns_records", map[string]any{
		"type": "CNAME", "name": name, "content": content, "proxied": true, "ttl": 1, "comment": Managed})
	return err
}

func (c *Cloudflare) Update(ctx context.Context, id, content string) error {
	_, err := c.call(ctx, http.MethodPatch, "/dns_records/"+url.PathEscape(id), map[string]any{"content": content, "proxied": true})
	return err
}

func (c *Cloudflare) Delete(ctx context.Context, id string) error {
	_, err := c.call(ctx, http.MethodDelete, "/dns_records/"+url.PathEscape(id), nil)
	return err
}

// Fake is an in-memory zone for tests. Unmanaged holds names that have a
// record Drop does not own.
type Fake struct {
	mu        sync.Mutex
	next      int
	Records   map[string]Record // by ID
	Unmanaged map[string]bool
	Fail      error
}

func NewFake() *Fake { return &Fake{Records: map[string]Record{}, Unmanaged: map[string]bool{}} }

func (f *Fake) Managed(context.Context) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return nil, f.Fail
	}
	var out []Record
	for _, r := range f.Records {
		out = append(out, r)
	}
	return out, nil
}

func (f *Fake) Taken(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Unmanaged[name], f.Fail
}

func (f *Fake) Create(_ context.Context, name, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	f.next++
	id := fmt.Sprint(f.next)
	f.Records[id] = Record{ID: id, Name: name, Content: content}
	return nil
}

func (f *Fake) Update(_ context.Context, id, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	r := f.Records[id]
	r.Content = content
	f.Records[id] = r
	return nil
}

func (f *Fake) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	delete(f.Records, id)
	return nil
}

// Names maps each managed name to its content (tests).
func (f *Fake) Names() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, r := range f.Records {
		out[r.Name] = r.Content
	}
	return out
}
