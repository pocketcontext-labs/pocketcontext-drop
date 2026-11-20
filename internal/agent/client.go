package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// APIError is an answer of the server other than success. Code is the
// server's error code (for example node_disabled); Message is the server's
// own text, which never holds a secret.
type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// Unreachable wraps a transport failure (the server is down or the request
// timed out).
type Unreachable struct{ Err error }

func (e *Unreachable) Error() string { return "the Drop server is not reachable: " + e.Err.Error() }
func (e *Unreachable) Unwrap() error { return e.Err }

// statusOf returns the HTTP status of an APIError, or 0.
func statusOf(err error) int {
	var api *APIError
	if errors.As(err, &api) {
		return api.Status
	}
	return 0
}

func codeOf(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		return api.Code
	}
	return ""
}

// Client talks to the Drop server's agent routes as one node. It is safe for
// concurrent use (the realtime goroutine and the loop share it).
type Client struct {
	base, host, node string
	secret           string
	http             *http.Client
	userAgent        string

	mu        sync.Mutex
	token     string
	nodeID    string
	lastLogin time.Time
	now       func() time.Time
}

// NewClient returns a client; it does not sign in.
func NewClient(cfg Config, secret, version string) *Client {
	return &Client{
		base: cfg.Server, host: cfg.Host, node: cfg.Node, secret: secret,
		userAgent: "drop-agent/" + version,
		http: &http.Client{
			// A followed redirect could send the token elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}
}

// NodeID is the node's record id, known after the first sign-in.
func (c *Client) NodeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodeID
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// newRequest builds a request to the hosted origin: Host is the Drop domain,
// no Origin header.
func (c *Client) newRequest(ctx context.Context, method, path string, body any, token string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Host = c.host
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return req, nil
}

// call sends one JSON request and decodes a 2xx answer into out (when not
// nil). Other answers become *APIError, transport failures *Unreachable.
func (c *Client) call(ctx context.Context, method, path string, body any, token string, out any) error {
	req, err := c.newRequest(ctx, method, path, body, token)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Unreachable{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return &Unreachable{Err: err}
	}
	if len(data) > 8<<20 {
		return errors.New("Drop response exceeds 8 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode}
		var shaped struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &shaped) == nil {
			apiErr.Code, apiErr.Message = shaped.Code, shaped.Message
		}
		if len(apiErr.Message) > 300 {
			apiErr.Message = apiErr.Message[:300]
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 3600 {
			apiErr.RetryAfter = time.Duration(s) * time.Second
		}
		return apiErr
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("malformed answer from %s: %w", path, err)
	}
	return nil
}

type authAnswer struct {
	Token  string `json:"token"`
	Record struct {
		ID             string `json:"id"`
		CollectionName string `json:"collectionName"`
		Name           string `json:"name"`
	} `json:"record"`
}

func (c *Client) takeAuth(a authAnswer) error {
	if a.Token == "" || a.Record.CollectionName != dropapi.NodeCollection || a.Record.ID == "" {
		return errors.New("the sign-in answer is not a node token")
	}
	if a.Record.Name != c.node {
		return fmt.Errorf("signed in as node %q, configured as %q", a.Record.Name, c.node)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodeID != "" && c.nodeID != a.Record.ID {
		return fmt.Errorf("node %q changed its record id; restart the agent", c.node)
	}
	c.token, c.nodeID = a.Token, a.Record.ID
	return nil
}

// Login signs in with the node's name and secret. 400 means a wrong
// (rotated) secret, 403 a disabled node.
func (c *Client) Login(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, PullTimeout)
	defer cancel()
	c.mu.Lock()
	c.lastLogin = c.now()
	c.mu.Unlock()
	var a authAnswer
	if err := c.call(ctx, http.MethodPost, dropapi.NodeAuthRoute, map[string]string{"identity": c.node, "password": c.secret}, "", &a); err != nil {
		return err
	}
	return c.takeAuth(a)
}

// Refresh renews the token; on 401 it signs in again.
func (c *Client) Refresh(ctx context.Context) error {
	var a authAnswer
	callCtx, cancel := context.WithTimeout(ctx, PullTimeout)
	defer cancel()
	err := c.call(callCtx, http.MethodPost, dropapi.NodeRefreshRoute, nil, c.currentToken(), &a)
	if statusOf(err) == http.StatusUnauthorized {
		return c.Login(ctx)
	}
	if err != nil {
		return err
	}
	return c.takeAuth(a)
}

// authed sends an authenticated request; a 401 signs in again (at most once
// every 5 seconds) and retries once.
func (c *Client) authed(ctx context.Context, method, path string, body, out any) error {
	err := c.call(ctx, method, path, body, c.currentToken(), out)
	if statusOf(err) != http.StatusUnauthorized {
		return err
	}
	c.mu.Lock()
	recent := c.now().Sub(c.lastLogin) < 5*time.Second
	c.mu.Unlock()
	if recent {
		return err
	}
	if loginErr := c.Login(ctx); loginErr != nil {
		return loginErr
	}
	return c.call(ctx, method, path, body, c.currentToken(), out)
}

// Desired pulls the node's whole desired document. It never returns a
// partial document: any failure is an error and the caller writes nothing.
func (c *Client) Desired(ctx context.Context) (*dropapi.Desired, error) {
	ctx, cancel := context.WithTimeout(ctx, PullTimeout)
	defer cancel()
	var raw json.RawMessage
	if err := c.authed(ctx, http.MethodGet, dropapi.AgentDesiredRoute, nil, &raw); err != nil {
		return nil, err
	}
	d, err := decodeDesired(raw)
	if err == nil && (d.Node != c.node || d.NodeID != c.NodeID()) {
		return nil, errors.New("desired document is for another node")
	}
	return d, err
}

func decodeDesired(raw []byte) (*dropapi.Desired, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d dropapi.Desired
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("malformed desired document: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, errors.New("unexpected data after desired document")
	}
	if err := checkEnvelope(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Report sends one drop.report/v1.
func (c *Client) Report(ctx context.Context, r dropapi.Report) (*dropapi.ReportResult, error) {
	ctx, cancel := context.WithTimeout(ctx, PullTimeout)
	defer cancel()
	var out dropapi.ReportResult
	if err := c.authed(ctx, http.MethodPost, dropapi.AgentReportRoute, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Subscribe registers the realtime client for nodes/<self>.
func (c *Client) Subscribe(ctx context.Context, clientID string) error {
	ctx, cancel := context.WithTimeout(ctx, PullTimeout)
	defer cancel()
	body := map[string]any{"clientId": clientID, "subscriptions": []string{"nodes/" + c.NodeID()}}
	return c.authed(ctx, http.MethodPost, "/api/realtime", body, nil)
}
